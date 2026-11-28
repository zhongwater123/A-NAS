// Package protocol exposes 剪辑中心 under /api/v1/videocut. It does not
// authenticate: the VideoCut service, or the Product Service in development,
// wraps it so that every request carries the signed-in user and the session
// token (accounts.WithUser, accounts.WithSessionToken), and writes have
// passed the Product Service's CSRF check.
package protocol

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

const PathPrefix = "/api/v1/videocut"

// Unavailable is the error code while the VideoCut store cannot be used, as
// when the data volume is offline or read-only.
const Unavailable = "videocut_unavailable"

type handler struct {
	store  *app.Store
	logger *slog.Logger
	mux    *apiroute.Mux
}

type route func(http.ResponseWriter, *http.Request, accounts.User)

// New returns the VideoCut API. A nil store answers 503 videocut_unavailable.
func New(store *app.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, mux: apiroute.NewMux()}
	// Venus's operations (#694): the contract came first, and VideoCut
	// serves an operation as it is built; until then it answers 501.
	routes := map[string]route{
		"GET /catalog":                                      notImplemented,
		"GET /songs/{songID}/audio":                         notImplemented,
		"GET /projects":                                     notImplemented,
		"POST /projects":                                    notImplemented,
		"GET /projects/{projectID}":                         notImplemented,
		"DELETE /projects/{projectID}":                      notImplemented,
		"POST /projects/{projectID}/files":                  notImplemented,
		"DELETE /projects/{projectID}/files/{fileID}":       notImplemented,
		"PATCH /projects/{projectID}/selection":             notImplemented,
		"PATCH /projects/{projectID}/settings":              notImplemented,
		"GET /projects/{projectID}/song":                    notImplemented,
		"POST /projects/{projectID}/song/import":            notImplemented,
		"GET /projects/{projectID}/song/audio":              notImplemented,
		"POST /projects/{projectID}/heartbeat":              notImplemented,
		"POST /projects/{projectID}/generate":               h.planningNotImplemented,
		"POST /projects/{projectID}/resume":                 notImplemented,
		"POST /projects/{projectID}/retry":                  h.planningNotImplemented,
		"GET /projects/{projectID}/assets":                  notImplemented,
		"POST /projects/{projectID}/assets":                 notImplemented,
		"GET /projects/{projectID}/assets/{assetID}/proxy":  notImplemented,
		"GET /projects/{projectID}/assets/{assetID}/frame":  notImplemented,
		"GET /projects/{projectID}/timeline":                notImplemented,
		"POST /projects/{projectID}/timeline/edits":         notImplemented,
		"POST /projects/{projectID}/timeline/undo":          notImplemented,
		"GET /projects/{projectID}/slots/{slot}/candidates": notImplemented,
		"GET /assistant":                                    notImplemented,
		"POST /projects/{projectID}/assistant":              notImplemented,
		"POST /projects/{projectID}/export":                 notImplemented,
		"GET /projects/{projectID}/film":                    notImplemented,
		"GET /notices":                                      h.notices,
	}
	for pattern, serve := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(serve))
	}
	// The Product Service copies a song here itself (CopySong); the browser
	// never reaches this route, so it is not one of the API's endpoints.
	h.mux.ServeMux.HandleFunc("PUT "+PathPrefix+"/projects/{projectID}/song", h.withUser(notImplemented))
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the VideoCut API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "VideoCut is not available")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.UserFrom(r.Context())
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, user)
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrTooManyAssets):
		httpjson.WriteError(w, http.StatusBadRequest, "too_many_assets", "at most 500 videos at once")
	case errors.Is(err, domain.ErrInvalid):
		httpjson.WriteError(w, http.StatusBadRequest, "validation_failed", "the request is not valid")
	case errors.Is(err, domain.ErrAssetsRefused):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "assets_unavailable", "a video is not one you can add")
	case errors.Is(err, domain.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "conflict", "the request key was used for another request")
	case errors.Is(err, domain.ErrGrantRevoked):
		httpjson.WriteError(w, http.StatusConflict, "grant_revoked", "the project can no longer reach its videos")
	case errors.Is(err, domain.ErrSessionEnded):
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
	case errors.Is(err, domain.ErrJobActive):
		httpjson.WriteError(w, http.StatusConflict, "job_in_progress", "the project already has a job in progress")
	case errors.Is(err, domain.ErrJobState):
		httpjson.WriteError(w, http.StatusConflict, "job_state", "the job cannot do this now")
	case errors.Is(err, domain.ErrJobsUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "VideoCut cannot make films here")
	case errors.Is(err, domain.ErrSongTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "song_too_large", "the song is larger than 100 MiB")
	case errors.Is(err, domain.ErrSongLength):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "song_length", "the song is shorter than 30 seconds or longer than 8 minutes")
	case errors.Is(err, domain.ErrNotAudio):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "not_audio", "the file is not audio VideoCut can read")
	case errors.Is(err, domain.ErrNoSong):
		httpjson.WriteError(w, http.StatusNotFound, "no_song", "the project has no song yet")
	case errors.Is(err, domain.ErrSongNotReady):
		httpjson.WriteError(w, http.StatusConflict, "song_not_ready", "the song is not analysed yet, or cannot be used")
	case errors.Is(err, domain.ErrSongStart):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "song_start_unusable", "no usable excerpt starts there")
	case errors.Is(err, domain.ErrMediaUnavailable):
		h.logger.WarnContext(r.Context(), "media worker unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_unavailable", "the media worker is not available")
	case errors.Is(err, videocutcompute.ErrWorkerUnavailable), errors.Is(err, videocutcompute.ErrWorkerFailed), errors.Is(err, videocutcompute.ErrWorkerRefused):
		h.logger.WarnContext(r.Context(), "compute worker unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "compute_unavailable", "the VideoCut compute worker is not available")
	case errors.Is(err, domain.ErrPhotosUnavailable):
		h.logger.WarnContext(r.Context(), "photo library unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available")
	default:
		httpjson.WriteInternal(w, r, h.logger, err)
	}
}

// notImplemented answers an operation of the contract VideoCut does not
// serve yet; the Web answers it from its MSW stand-ins meanwhile.
func notImplemented(w http.ResponseWriter, _ *http.Request, _ accounts.User) {
	httpjson.WriteError(w, http.StatusNotImplemented, "not_implemented", "VideoCut does not serve this yet")
}
