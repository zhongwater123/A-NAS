// Package mediaapi exposes the media center under /api/v1/media. It does not
// authenticate: the Product Service wraps it so that every request carries
// the signed-in user (WithUser) and the session token the File Broker needs,
// and so that writes have passed CSRF checks.
package mediaapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/media"
)

const PathPrefix = "/api/v1/media"

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

type handler struct {
	service *media.Service
	logger  *slog.Logger
	mux     *apiroute.Mux
}

// New returns the media API. A nil service answers 503 media_unavailable.
func New(service *media.Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{service: service, logger: logger, mux: apiroute.NewMux()}
	routes := map[string]func(http.ResponseWriter, *http.Request, accounts.User){
		"GET /libraries":                        h.libraries,
		"POST /libraries":                       h.createLibrary,
		"GET /libraries/{id}":                   h.library,
		"PATCH /libraries/{id}":                 h.updateLibrary,
		"DELETE /libraries/{id}":                h.deleteLibrary,
		"POST /libraries/{id}/scan":             h.scanLibrary,
		"GET /home":                             h.home,
		"GET /titles":                           h.titles,
		"GET /shows/{id}":                       h.show,
		"PATCH /shows/{id}":                     h.renameShow,
		"GET /videos/{id}":                      h.video,
		"PATCH /videos/{id}":                    h.renameVideo,
		"GET /videos/{id}/playback":             h.playback,
		"GET /videos/{id}/file":                 h.original,
		"GET /videos/{id}/stream":               h.stream,
		"GET /videos/{id}/subtitles/{track}":    h.subtitle,
		"PUT /videos/{id}/progress":             h.progress,
		"PUT /titles/{id}/watched":              h.watched,
		"GET /favorites":                        h.favorites,
		"PUT /favorites/{id}":                   h.favorite(true),
		"DELETE /favorites/{id}":                h.favorite(false),
		"GET /history":                          h.history,
		"DELETE /history":                       h.clearHistory,
		"DELETE /history/{id}":                  h.removeHistory,
		"GET /collections":                      h.collections,
		"POST /collections":                     h.createCollection,
		"GET /collections/{id}":                 h.collection,
		"PATCH /collections/{id}":               h.renameCollection,
		"DELETE /collections/{id}":              h.deleteCollection,
		"POST /collections/{id}/items":          h.addToCollection,
		"DELETE /collections/{id}/items/{item}": h.removeFromCollection,
		"GET /folders":                          h.folderRoots,
		"GET /folders/{entry}":                  h.folder,
		"GET /artwork/{id}/{kind}":              h.artwork,
	}
	for pattern, route := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(route))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the media API serves beneath PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_unavailable", "the media center is unavailable")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next func(http.ResponseWriter, *http.Request, accounts.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userKey{}).(accounts.User)
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, user)
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, media.ErrNotFound), errors.Is(err, files.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, media.ErrForbidden), errors.Is(err, files.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "you cannot do this")
	case errors.Is(err, media.ErrInvalid):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), media.ErrInvalid.Error()+": "))
	case errors.Is(err, media.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "folders_overlap", "a folder is already part of another library")
	case errors.Is(err, media.ErrBusy):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_busy", "too many videos are being converted")
	case errors.Is(err, media.ErrUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_worker_unavailable", "video conversion is unavailable")
	case errors.Is(err, media.ErrUnreadable):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "media_unreadable", "the video cannot be read")
	case errors.Is(err, files.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "name_conflict", "a file with this name already exists")
	case errors.Is(err, files.ErrInvalidName):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "the file name is invalid")
	case errors.Is(err, files.ErrVolumeUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "volume_unavailable", "the data volume is unavailable")
	case errors.Is(err, context.Canceled):
	default:
		h.logger.ErrorContext(r.Context(), "media request failed", "path", r.URL.Path, "error", err)
		httpjson.WriteError(w, http.StatusInternalServerError, "internal_error", "the request failed")
	}
}

func (h *handler) libraries(w http.ResponseWriter, r *http.Request, user accounts.User) {
	h.service.RefreshStale(r.Context(), user)
	libraries, err := h.service.ListLibraries(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": libraries, "processing": h.service.Processing()})
}

func (h *handler) library(w http.ResponseWriter, r *http.Request, user accounts.User) {
	library, err := h.service.GetLibrary(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, library)
}

func (h *handler) createLibrary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input media.LibraryInput
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	library, err := h.service.CreateLibrary(r.Context(), user, input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, library)
}

func (h *handler) updateLibrary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input media.LibraryInput
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	library, err := h.service.UpdateLibrary(r.Context(), user, r.PathValue("id"), input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, library)
}

func (h *handler) deleteLibrary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.DeleteLibrary(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) scanLibrary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	library, err := h.service.Rescan(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, library)
}

func (h *handler) home(w http.ResponseWriter, r *http.Request, user accounts.User) {
	h.service.RefreshStale(r.Context(), user)
	home, err := h.service.Home(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, home)
}

func integer(values url.Values, name string, fallback int) int {
	value, err := strconv.Atoi(values.Get(name))
	if err != nil {
		return fallback
	}
	return value
}

func (h *handler) titles(w http.ResponseWriter, r *http.Request, user accounts.User) {
	query := r.URL.Query()
	results, err := h.service.Titles(r.Context(), user, media.TitleQuery{
		Category: query.Get("category"), LibraryID: query.Get("library"), Genre: query.Get("genre"), Sort: query.Get("sort"),
		Search: query.Get("q"), Offset: integer(query, "offset", 0), Limit: integer(query, "limit", 0),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, results)
}

func (h *handler) show(w http.ResponseWriter, r *http.Request, user accounts.User) {
	show, err := h.service.Show(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, show)
}

func (h *handler) video(w http.ResponseWriter, r *http.Request, user accounts.User) {
	video, err := h.service.Video(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, video)
}

// capabilities reads the browser's codec support from caps=hevc,av1,...
func capabilities(values url.Values) media.Capabilities {
	var caps media.Capabilities
	for value := range strings.SplitSeq(values.Get("caps"), ",") {
		switch strings.TrimSpace(value) {
		case "hevc":
			caps.HEVC = true
		case "av1":
			caps.AV1 = true
		case "vp9":
			caps.VP9 = true
		case "ac3":
			caps.AC3 = true
		case "eac3":
			caps.EAC3 = true
		case "mkv":
			caps.Matroska = true
		case "webm":
			caps.WebM = true
		}
	}
	return caps
}

func (h *handler) playback(w http.ResponseWriter, r *http.Request, user accounts.User) {
	query := r.URL.Query()
	id := r.PathValue("id")
	playback, err := h.service.Playback(r.Context(), user, id, capabilities(query), integer(query, "audio", -1), query.Get("quality"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	base := PathPrefix + "/videos/" + url.PathEscape(id)
	if playback.Mode == media.ModeDirect {
		playback.URL = base + "/file"
	} else {
		parameters := url.Values{"mode": {playback.Mode}, "audio": {strconv.Itoa(playback.Audio)}, "quality": {playback.Quality}}
		if caps := query.Get("caps"); caps != "" {
			parameters.Set("caps", caps)
		}
		playback.URL = base + "/stream?" + parameters.Encode()
	}
	httpjson.Write(w, http.StatusOK, playback)
}

func (h *handler) original(w http.ResponseWriter, r *http.Request, user accounts.User) {
	content, contentType, err := h.service.Original(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = content.Reader.Close() }()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": content.Name}))
	http.ServeContent(w, r, content.Name, content.ModifiedAt, content.Reader)
}

func (h *handler) stream(w http.ResponseWriter, r *http.Request, user accounts.User) {
	query := r.URL.Query()
	start, _ := strconv.ParseFloat(query.Get("start"), 64)
	stream, err := h.service.Stream(r.Context(), user, r.PathValue("id"), media.StreamRequest{
		Mode: query.Get("mode"), Start: start, Audio: integer(query, "audio", -1), Quality: query.Get("quality"), Caps: capabilities(query),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer stream.Close()
	// A converted stream cannot seek; the player restarts it at a new start.
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(flushingWriter{w}, stream)
}

// flushingWriter sends each chunk as FFmpeg produces it.
type flushingWriter struct{ w http.ResponseWriter }

func (f flushingWriter) Write(data []byte) (int, error) {
	count, err := f.w.Write(data)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return count, err
}

func (h *handler) subtitle(w http.ResponseWriter, r *http.Request, user accounts.User) {
	data, err := h.service.Subtitle(r.Context(), user, r.PathValue("id"), r.PathValue("track"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	_, _ = w.Write(data)
}

func (h *handler) progress(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	progress, err := h.service.SaveProgress(r.Context(), user, r.PathValue("id"), input.Position, input.Duration)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, progress)
}

func (h *handler) watched(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Watched bool `json:"watched"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	if err := h.service.SetWatched(r.Context(), user, r.PathValue("id"), input.Watched); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) favorites(w http.ResponseWriter, r *http.Request, user accounts.User) {
	page, err := h.service.Favorites(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}

func (h *handler) favorite(favorite bool) func(http.ResponseWriter, *http.Request, accounts.User) {
	return func(w http.ResponseWriter, r *http.Request, user accounts.User) {
		if err := h.service.SetFavorite(r.Context(), user, r.PathValue("id"), favorite); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *handler) history(w http.ResponseWriter, r *http.Request, user accounts.User) {
	query := r.URL.Query()
	page, err := h.service.History(r.Context(), user, integer(query, "offset", 0), integer(query, "limit", 0))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}

func (h *handler) clearHistory(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.ClearHistory(r.Context(), user); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeHistory(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.RemoveFromHistory(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) collections(w http.ResponseWriter, r *http.Request, user accounts.User) {
	collections, err := h.service.Collections(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": collections})
}

func (h *handler) createCollection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Name  string   `json:"name"`
		Items []string `json:"items"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	collection, err := h.service.CreateCollection(r.Context(), user, input.Name, input.Items)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, collection)
}

func (h *handler) collection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	detail, err := h.service.CollectionDetail(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

// renameShow renames a show's folder and the episode files that carry its
// title, as the user.
func (h *handler) renameShow(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Name string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	detail, err := h.service.RenameShow(r.Context(), user, r.PathValue("id"), input.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

// renameVideo renames the video's file, the 片源, as the user.
func (h *handler) renameVideo(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		FileName string `json:"fileName"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	detail, err := h.service.RenameVideo(r.Context(), user, r.PathValue("id"), input.FileName)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *handler) renameCollection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Name string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	if err := h.service.RenameCollection(r.Context(), user, r.PathValue("id"), input.Name); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteCollection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.DeleteCollection(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) addToCollection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var input struct {
		Items []string `json:"items"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &input) {
		return
	}
	if err := h.service.AddToCollection(r.Context(), user, r.PathValue("id"), input.Items); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeFromCollection(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.RemoveFromCollection(r.Context(), user, r.PathValue("id"), r.PathValue("item")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) folderRoots(w http.ResponseWriter, r *http.Request, user accounts.User) {
	roots, err := h.service.FolderRoots(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": roots})
}

func (h *handler) folder(w http.ResponseWriter, r *http.Request, user accounts.User) {
	listing, err := h.service.Folder(r.Context(), user, r.PathValue("entry"), r.URL.Query().Get("path"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, listing)
}

func (h *handler) artwork(w http.ResponseWriter, r *http.Request, user accounts.User) {
	image, err := h.service.Artwork(r.Context(), user, r.PathValue("id"), r.PathValue("kind"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = image.File.Close() }()
	// The URL carries the artwork version, so the browser may keep it.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "artwork.jpg", image.Modified.Truncate(time.Second), image.File)
}
