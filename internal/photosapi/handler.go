// Package photosapi exposes the photo library under /api/v1/photos. It does
// not authenticate: the caller wraps it so that every request carries the
// Principal confirmed for its session (WithPrincipal) and so that writes have
// passed CSRF checks. The photo service serves it after confirming sessions
// with the Host Agent; in development the Product Service mounts it directly
// (ADR 0011).
package photosapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

const PathPrefix = "/api/v1/photos"

// AIStatusPath reports local AI progress; windows may poll it.
const AIStatusPath = PathPrefix + "/ai"

type principalKey struct{}

// WithPrincipal attaches the caller confirmed by the session lookup.
func WithPrincipal(ctx context.Context, principal photos.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// NewPrincipal builds the photo Principal for a confirmed user and their
// unexpired grants to view members' private libraries.
func NewPrincipal(userID, username string, admin bool, viewings []accounts.LibraryViewing) photos.Principal {
	principal := photos.Principal{UserID: userID, Username: username, Admin: admin}
	for _, viewing := range viewings {
		principal.Viewing = append(principal.Viewing, photos.ViewingGrant{
			GrantID: viewing.GrantID, OwnerUserID: viewing.OwnerUserID, ExpiresAt: viewing.ExpiresAt,
		})
	}
	return principal
}

// NewAssistantPrincipal builds the photo Principal for a member's AI
// assistant (ADR 0024): it reads as the member, never as an administrator or
// through a viewing grant, and changes nothing.
func NewAssistantPrincipal(userID, username string) photos.Principal {
	return photos.Principal{UserID: userID, Username: username, Assistant: true}
}

func principalFrom(r *http.Request) (photos.Principal, bool) {
	principal, ok := r.Context().Value(principalKey{}).(photos.Principal)
	return principal, ok && principal.UserID != ""
}

type handler struct {
	service *photos.Service
	logger  *slog.Logger
	mux     *http.ServeMux
}

// New returns the photo API. A nil service means the photo library is not
// available on this device; every request then answers 503 photos_unavailable.
func New(service *photos.Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{service: service, logger: logger, mux: http.NewServeMux()}
	h.routes()
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.service == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
		return
	}
	principal, ok := principalFrom(r)
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	}
	// The photo Policy already refuses an assistant every change; refusing
	// all but reads here also keeps it from searching by an uploaded image.
	if principal.Assistant && r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpjson.WriteError(w, http.StatusForbidden, "assistant_read_only", "the assistant can only read the photo library")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) routes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/libraries", h.listLibraries)
	h.mux.HandleFunc("GET "+AIStatusPath, h.aiStatus)
	h.mux.HandleFunc("GET "+ActivityPath, h.activity)
	h.mux.HandleFunc("GET "+p+"/search", h.search)
	h.mux.HandleFunc("POST "+p+"/image-searches", h.searchByImage)
	h.mux.HandleFunc("GET "+p+"/image-searches/{searchID}/assets", h.imageSearchResults)
	h.mux.HandleFunc("GET "+p+"/labels", h.labels)
	h.mux.HandleFunc("GET "+p+"/labels/{labelID}/assets", h.labelPhotos)
	h.mux.HandleFunc("GET "+p+"/tags", h.tags)
	h.mux.HandleFunc("GET "+p+"/tags/{name}/assets", h.tagPhotos)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/timeline", h.timeline)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/timeline/months", h.timelineMonths)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/entries", h.entries)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/uploads", h.upload)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/live-photos", h.uploadLive)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/live-videos", h.uploadLiveVideo)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/preview", h.preview)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/view", h.view)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/metadata", h.metadata)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/motion", h.motion)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/motion/play", h.playMotion)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/download", h.download)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/directories", h.createDirectory)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/trash", h.listTrash)
	h.mux.HandleFunc("DELETE "+p+"/libraries/{libraryID}/trash", h.emptyTrash)
	h.mux.HandleFunc("PATCH "+p+"/directories/{directoryID}", h.changeDirectory)
	h.mux.HandleFunc("DELETE "+p+"/directories/{directoryID}", h.deleteDirectory)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}", h.getAsset)
	h.mux.HandleFunc("PATCH "+p+"/assets/{assetID}", h.changeAsset)
	h.mux.HandleFunc("DELETE "+p+"/assets/{assetID}", h.trashAsset)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/original", h.original)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/thumbnail", h.thumbnail)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/similar", h.similar)
	h.mux.HandleFunc("POST "+p+"/assets/{assetID}/copies", h.copyAsset)
	h.mux.HandleFunc("POST "+p+"/assets/{assetID}/restore", h.restoreAsset)
	h.mux.HandleFunc("DELETE "+p+"/trash/{assetID}", h.purgeAsset)
	h.metadataRoutes()
	h.peopleRoutes()
	h.videoRoutes()
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
}

type itemsResponse[T any] struct {
	Items []T `json:"items"`
}

type timelineResponse struct {
	Items []photos.Asset `json:"items"`
	Next  string         `json:"next,omitempty"`
}

type searchResponse struct {
	Items []photos.Asset `json:"items"`
	Next  string         `json:"next,omitempty"`
	// Semantic is false when only names were matched because local AI could
	// not encode the query.
	Semantic bool `json:"semantic"`
	// Closest counts the results, over all pages, in the closest group; the
	// results after them are less related.
	Closest int `json:"closest"`
	// Objects is set when the query is about things the detector finds
	// reliably: the closest group is then the photos it found them in.
	Objects *objectsFound `json:"objects,omitempty"`
	// People are the person names in the query; the results keep to their
	// photos.
	People []string `json:"people,omitempty"`
}

type objectsFound struct {
	Words      []string `json:"words"`
	Detected   int      `json:"detected"`
	Undetected int      `json:"undetected"`
}

func (h *handler) listLibraries(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	libraries, err := h.service.Libraries(r.Context(), principal)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, itemsResponse[photos.Library]{Items: libraries})
}

func (h *handler) aiStatus(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	status, err := h.service.AIStatus(r.Context(), principal)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, status)
}

// pageLimit reads the optional limit query parameter; 0 means the default.
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
		return 0, false
	}
	return limit, true
}

func (h *handler) timeline(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	var page photos.Page
	var err error
	if month := query.Get("month"); month != "" {
		page, err = h.service.TimelineInMonth(r.Context(), principal, r.PathValue("libraryID"), month, query.Get("cursor"), limit)
	} else {
		page, err = h.service.Timeline(r.Context(), principal, r.PathValue("libraryID"), query.Get("cursor"), limit)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

func (h *handler) timelineMonths(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	months, err := h.service.TimelineMonths(r.Context(), principal, r.PathValue("libraryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, itemsResponse[photos.TimelineMonth]{Items: months})
}

type labelsResponse struct {
	Items []photos.LabelCount `json:"items"`
	// Ready is false while AI clusters are not available yet.
	Ready bool `json:"ready"`
}

// labels lists the AI clusters; they are shown on their own and never take
// part in search.
func (h *handler) labels(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	counts, ready, err := h.service.LabelCounts(r.Context(), principal, r.URL.Query().Get("viewing"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, labelsResponse{Items: counts, Ready: ready})
}

func (h *handler) labelPhotos(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.service.LabelPhotos(r.Context(), principal, r.PathValue("labelID"), query.Get("viewing"), query.Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

// tags lists the users' own tags as groups, beside the AI clusters.
func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	counts, err := h.service.TagCounts(r.Context(), principal, r.URL.Query().Get("viewing"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, itemsResponse[photos.TagCount]{Items: counts})
}

func (h *handler) tagPhotos(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.service.TagPhotos(r.Context(), principal, r.PathValue("name"), query.Get("viewing"), query.Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.service.Search(r.Context(), principal, photos.SearchRequest{
		Query: query.Get("q"), Viewing: query.Get("viewing"), Library: query.Get("library"), Cursor: query.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response := searchResponse{Items: page.Assets, Next: page.Next, Semantic: page.Semantic, Closest: page.Closest,
		People: page.People}
	if page.Objects != nil {
		response.Objects = &objectsFound{Words: page.Objects.Words, Detected: page.Objects.Detected, Undetected: page.Objects.Undetected}
	}
	httpjson.Write(w, http.StatusOK, response)
}

type similarResponse struct {
	Items []photos.Asset `json:"items"`
	Next  string         `json:"next,omitempty"`
	// Ready is false while the photo has no vector yet.
	Ready bool `json:"ready"`
}

func (h *handler) similar(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.service.Similar(r.Context(), principal, r.PathValue("assetID"), photos.SimilarRequest{
		Viewing: query.Get("viewing"), Cursor: query.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, similarResponse{Items: page.Assets, Next: page.Next, Ready: page.Ready})
}

type imageSearchResponse struct {
	// ID lists the results under image-searches/{id}/assets for a while.
	ID string `json:"id"`
}

// searchByImage accepts one multipart "file" part, the image to search the
// library by. It is encoded at once and never stored.
func (h *handler) searchByImage(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	reader, err := r.MultipartReader()
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		id, err := h.service.SearchByImage(r.Context(), principal, part)
		_ = part.Close()
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusCreated, imageSearchResponse{ID: id})
		return
	}
	httpjson.WriteError(w, http.StatusBadRequest, "file_required", "one file part is required")
}

func (h *handler) imageSearchResults(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.service.ImageSearchResults(r.Context(), principal, r.PathValue("searchID"), photos.SimilarRequest{
		Viewing: query.Get("viewing"), Cursor: query.Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

func (h *handler) entries(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	listing, err := h.service.ListDirectory(r.Context(), principal, r.PathValue("libraryID"), query.Get("directoryId"), query.Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, listing)
}

// upload accepts one multipart "file" part, optionally preceded by a
// "directoryId" part, and streams it straight into the library.
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	reader, err := r.MultipartReader()
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	var directoryID string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
			return
		}
		switch part.FormName() {
		case "directoryId":
			value, readErr := io.ReadAll(io.LimitReader(part, 4097))
			_ = part.Close()
			if readErr != nil || len(value) > 4096 {
				httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "directory ID is invalid")
				return
			}
			directoryID = string(value)
		case "file":
			result, err := h.service.Upload(r.Context(), principal, photos.ImportRequest{
				LibraryID: r.PathValue("libraryID"), DirectoryID: directoryID, Name: part.FileName(), Content: part,
			})
			_ = part.Close()
			if err != nil {
				h.fail(w, r, err)
				return
			}
			h.uploadResult(w, result)
			return
		default:
			_ = part.Close()
		}
	}
	httpjson.WriteError(w, http.StatusBadRequest, "file_required", "one file part is required")
}

type directoryRequest struct {
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

func (h *handler) createDirectory(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request directoryRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	directory, err := h.service.CreateDirectory(r.Context(), principal, r.PathValue("libraryID"), request.ParentID, request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, directory)
}

// changeRequest renames and/or moves. A present parentId or directoryId of ""
// means the library root, so the fields are pointers.
type changeRequest struct {
	Name        *string `json:"name"`
	ParentID    *string `json:"parentId"`
	DirectoryID *string `json:"directoryId"`
}

func (h *handler) changeDirectory(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request changeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.DirectoryID != nil || (request.Name == nil && request.ParentID == nil) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "send name and/or parentId")
		return
	}
	directory, err := h.service.UpdateDirectory(r.Context(), principal, r.PathValue("directoryID"),
		photos.DirectoryUpdate{Name: request.Name, ParentID: request.ParentID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, directory)
}

func (h *handler) deleteDirectory(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	if err := h.service.DeleteDirectory(r.Context(), principal, r.PathValue("directoryID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.Get(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, asset)
}

func (h *handler) changeAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request changeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.ParentID != nil || (request.Name == nil && request.DirectoryID == nil) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "send name and/or directoryId")
		return
	}
	asset, err := h.service.Update(r.Context(), principal, r.PathValue("assetID"),
		photos.AssetUpdate{Name: request.Name, DirectoryID: request.DirectoryID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, asset)
}

func (h *handler) trashAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.Trash(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, asset)
}

func (h *handler) restoreAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.Restore(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, asset)
}

func (h *handler) purgeAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	if err := h.service.Purge(r.Context(), principal, r.PathValue("assetID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listTrash(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	assets, err := h.service.ListTrash(r.Context(), principal, r.PathValue("libraryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, itemsResponse[photos.Asset]{Items: assets})
}

type emptyTrashResponse struct {
	Purged int `json:"purged"`
}

func (h *handler) emptyTrash(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	purged, err := h.service.EmptyTrash(r.Context(), principal, r.PathValue("libraryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, emptyTrashResponse{Purged: purged})
}

type copyRequest struct {
	LibraryID   string `json:"libraryId"`
	DirectoryID string `json:"directoryId"`
}

func (h *handler) copyAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request copyRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	asset, err := h.service.Copy(r.Context(), principal, r.PathValue("assetID"), request.LibraryID, request.DirectoryID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, asset)
}

// original serves the original inline for the viewer, or as an attachment
// with ?download=1. Range requests are supported.
func (h *handler) original(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	content, err := h.service.Open(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	h.serve(w, r, content, disposition)
}

func (h *handler) thumbnail(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	content, err := h.service.Thumbnail(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serve(w, r, content, "inline")
}

// serve streams content. Responses may be cached privately but must be
// revalidated, so that access ending also ends what the browser shows.
func (h *handler) serve(w http.ResponseWriter, r *http.Request, content photos.Content, disposition string) {
	defer func() { _ = content.Reader.Close() }()
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", content.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": content.Name}))
	w.Header().Set("ETag", content.ETag)
	http.ServeContent(w, r, content.Name, time.Time{}, content.Reader)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var bodyLimit *http.MaxBytesError
	switch {
	case errors.As(err, &bodyLimit):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", photos.ErrTooLarge.Error())
	case errors.Is(err, photos.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "photo library item was not found")
	case errors.Is(err, photos.ErrNotVideo):
		httpjson.WriteError(w, http.StatusNotFound, "not_video", "this photo library item is not a video")
	case errors.Is(err, photos.ErrInvalidStart):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, photos.ErrMediaBusy):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_busy", "正在同时转换播放的视频太多，请稍后再试")
	case errors.Is(err, photos.ErrThumbnailUnavailable):
		httpjson.WriteError(w, http.StatusNotFound, "thumbnail_unavailable", "the thumbnail is not ready; use the original")
	case errors.Is(err, photos.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "this photo library item cannot be changed by you")
	case errors.Is(err, photos.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "conflict", "the item conflicts with its current state or an existing name")
	case errors.Is(err, photos.ErrInvalidName):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, photos.ErrInvalidCursor):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
	case errors.Is(err, photos.ErrInvalidMonth):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_month", "month must be YYYY-MM")
	case errors.Is(err, photos.ErrInvalidQuery):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "search needs a query of 1 to "+strconv.Itoa(photos.MaxQueryRunes)+" characters")
	case errors.Is(err, photos.ErrInvalidMotion):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_motion_photo", err.Error())
	case errors.Is(err, photos.ErrNoLiveStill):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "live_photo_not_found", "这个图库里没有在等这段视频的实况照片：请拖到放着对应照片的图库，并使用未修改的原件")
	case errors.Is(err, mediaworker.ErrUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_unavailable", "媒体解码服务暂不可用，请稍后重试")
	case errors.Is(err, mediaworker.ErrUnreadable):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "unreadable_media", "动态片段无法读取，请选择未修改的原件")
	case errors.Is(err, photos.ErrNoMetadata):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "no_metadata", "视频的拍摄信息还读不了，只有照片的可以")
	case errors.Is(err, photos.ErrNoView):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "no_view", "视频没有可以查看的画面，只有照片有")
	case errors.Is(err, photos.ErrUnsupportedType):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "支持 JPEG、PNG、HEIC 照片与 MP4、MOV 等常见视频；Live Photo 请同时选择照片与同名 MOV")
	case errors.Is(err, photos.ErrTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, photos.ErrInsufficientSpace):
		httpjson.WriteError(w, http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	case errors.Is(err, photos.ErrImageSearchExpired):
		httpjson.WriteError(w, http.StatusNotFound, "search_expired", "the image search is no longer kept; send the image again")
	case errors.Is(err, photos.ErrAIUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "ai_unavailable", "local AI cannot read the image right now; try again later")
	default:
		h.logger.ErrorContext(r.Context(), "photo request failed", "path", r.URL.Path, "error", err)
		httpjson.WriteError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	switch err := httpjson.Decode(w, r, httpjson.Limit, target); {
	case err == nil:
		return true
	case errors.Is(err, httpjson.ErrContentType):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
	default:
		httpjson.WriteDecodeError(w, err, "invalid_request", "request body must be one JSON object")
	}
	return false
}
