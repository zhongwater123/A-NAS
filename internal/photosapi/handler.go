// Package photosapi exposes the photo library under /api/v1/photos. It does
// not authenticate: the caller wraps it so that every request carries the
// Principal confirmed for its session (WithPrincipal) and so that writes have
// passed CSRF checks. The photo service serves it after confirming sessions
// with the Host Agent; in development the Product Service mounts it directly
// (ADR 0011).
package photosapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

const PathPrefix = "/api/v1/photos"

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
		WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
		return
	}
	if _, ok := principalFrom(r); !ok {
		WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) routes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/libraries", h.listLibraries)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/timeline", h.timeline)
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/entries", h.entries)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/uploads", h.upload)
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
	h.mux.HandleFunc("POST "+p+"/assets/{assetID}/copies", h.copyAsset)
	h.mux.HandleFunc("POST "+p+"/assets/{assetID}/restore", h.restoreAsset)
	h.mux.HandleFunc("DELETE "+p+"/trash/{assetID}", h.purgeAsset)
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
}

type itemsResponse[T any] struct {
	Items []T `json:"items"`
}

type timelineResponse struct {
	Items []photos.Asset `json:"items"`
	Next  string         `json:"next,omitempty"`
}

func (h *handler) listLibraries(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	libraries, err := h.service.Libraries(r.Context(), principal)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsResponse[photos.Library]{Items: libraries})
}

// pageLimit reads the optional limit query parameter; 0 means the default.
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
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
	page, err := h.service.Timeline(r.Context(), principal, r.PathValue("libraryID"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
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
	writeJSON(w, http.StatusOK, listing)
}

// upload accepts one multipart "file" part, optionally preceded by a
// "directoryId" part, and streams it straight into the library.
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	reader, err := r.MultipartReader()
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	var directoryID string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
			return
		}
		switch part.FormName() {
		case "directoryId":
			value, readErr := io.ReadAll(io.LimitReader(part, 4097))
			_ = part.Close()
			if readErr != nil || len(value) > 4096 {
				WriteError(w, http.StatusBadRequest, "invalid_request", "directory ID is invalid")
				return
			}
			directoryID = string(value)
		case "file":
			asset, err := h.service.Import(r.Context(), principal, photos.ImportRequest{
				LibraryID: r.PathValue("libraryID"), DirectoryID: directoryID, Name: part.FileName(), Content: part,
			})
			_ = part.Close()
			if err != nil {
				h.fail(w, r, err)
				return
			}
			writeJSON(w, http.StatusCreated, asset)
			return
		default:
			_ = part.Close()
		}
	}
	WriteError(w, http.StatusBadRequest, "file_required", "one file part is required")
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
	writeJSON(w, http.StatusCreated, directory)
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
		WriteError(w, http.StatusBadRequest, "invalid_request", "send name and/or parentId")
		return
	}
	directory, err := h.service.UpdateDirectory(r.Context(), principal, r.PathValue("directoryID"),
		photos.DirectoryUpdate{Name: request.Name, ParentID: request.ParentID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, directory)
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
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) changeAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request changeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.ParentID != nil || (request.Name == nil && request.DirectoryID == nil) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "send name and/or directoryId")
		return
	}
	asset, err := h.service.Update(r.Context(), principal, r.PathValue("assetID"),
		photos.AssetUpdate{Name: request.Name, DirectoryID: request.DirectoryID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) trashAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.Trash(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) restoreAsset(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.Restore(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
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
	writeJSON(w, http.StatusOK, itemsResponse[photos.Asset]{Items: assets})
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
	writeJSON(w, http.StatusOK, emptyTrashResponse{Purged: purged})
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
	writeJSON(w, http.StatusCreated, asset)
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
	defer content.Reader.Close()
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", content.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": content.Name}))
	w.Header().Set("ETag", content.ETag)
	http.ServeContent(w, r, content.Name, time.Time{}, content.Reader)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, photos.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found", "photo library item was not found")
	case errors.Is(err, photos.ErrThumbnailUnavailable):
		WriteError(w, http.StatusNotFound, "thumbnail_unavailable", "the thumbnail is not ready; use the original")
	case errors.Is(err, photos.ErrForbidden):
		WriteError(w, http.StatusForbidden, "forbidden", "this photo library item cannot be changed by you")
	case errors.Is(err, photos.ErrConflict):
		WriteError(w, http.StatusConflict, "conflict", "the item conflicts with its current state or an existing name")
	case errors.Is(err, photos.ErrInvalidName):
		WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, photos.ErrInvalidCursor):
		WriteError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
	case errors.Is(err, photos.ErrUnsupportedType):
		WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "only JPEG and PNG photos are supported")
	case errors.Is(err, photos.ErrTooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, photos.ErrInsufficientSpace):
		WriteError(w, http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "photo request failed", "path", r.URL.Path, "error", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		WriteError(w, http.StatusBadRequest, "invalid_request", "request body must be one JSON object")
		return false
	}
	return true
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError answers with the product API's error shape.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
