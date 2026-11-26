// Package protocol exposes 剪辑中心 under /api/v1/montage. It does not
// authenticate: the montage service, or the Product Service in development,
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

const PathPrefix = "/api/v1/montage"

// Unavailable is the error code while the montage store cannot be used, as
// when the data volume is offline or read-only.
const Unavailable = "montage_unavailable"

type handler struct {
	store  *app.Store
	logger *slog.Logger
	mux    *apiroute.Mux
}

type route func(http.ResponseWriter, *http.Request, accounts.User)

// New returns the montage API. A nil store answers 503 montage_unavailable.
func New(store *app.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, mux: apiroute.NewMux()}
	routes := map[string]route{
		"GET /projects":                        h.projects,
		"POST /projects":                       h.createProject,
		"GET /projects/{projectID}":            h.project,
		"PATCH /projects/{projectID}":          h.updateProject,
		"DELETE /projects/{projectID}":         h.deleteProject,
		"POST /projects/{projectID}/assets":    h.addAssets,
		"GET /projects/{projectID}/song":       h.song,
		"GET /projects/{projectID}/song/audio": h.songAudio,
		"POST /projects/{projectID}/generate":  h.generate,
		"GET /jobs/{jobID}":                    h.job,
		"POST /jobs/{jobID}/cancel":            h.cancelJob,
		"POST /jobs/{jobID}/drop-assets":       h.dropAssets,
		"POST /jobs/{jobID}/retry-analysis":    h.retryAnalysis,
		"POST /jobs/{jobID}/retry":             h.retryJob,
		"POST /jobs/{jobID}/replan":            h.replan,
		"GET /notices":                         h.notices,
		"GET /hero":                            h.hero("film"),
		"GET /hero/poster":                     h.hero("poster"),
		// Editing a film after it was made (#596).
		"POST /versions/{versionID}/confirm":         h.confirmVersion,
		"POST /versions/{versionID}/recut":           h.recut,
		"GET /versions/{versionID}/outline":          h.versionOutline,
		"GET /versions/{versionID}/draft":            h.versionDraft,
		"POST /drafts/{draftID}/ops":                 h.editDraft,
		"POST /drafts/{draftID}/save":                h.saveDraft,
		"GET /drafts/{draftID}/song":                 h.draftSong,
		"GET /drafts/{draftID}/candidates":           h.draftCandidates,
		"GET /versions/{versionID}/shots":            h.versionShots,
		"GET /drafts/{draftID}/shots":                h.draftShots,
		"GET /projects/{projectID}/frames/{assetID}": h.frame,
	}
	for pattern, serve := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(serve))
	}
	// The Product Service copies a song here itself (CopySong); the browser
	// never reaches this route, so it is not one of the API's endpoints.
	h.mux.ServeMux.HandleFunc("PUT "+PathPrefix+"/projects/{projectID}/song", h.withUser(h.putSong))
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the montage API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "the montage center is not available")
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

// maxJSONBytes bounds a JSON request. The largest the contract allows is
// about 34 KB: app.MaxAssets IDs of app.MaxIDLength printable ASCII
// characters with their quotes and commas, a request key and a name of
// app.MaxNameRunes characters, each escaped to six bytes at most
// (TestTheLargestRequestFits).
const maxJSONBytes = 64 << 10

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if failEdit(w, err) {
		return
	}
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
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "the montage center cannot make films here")
	case errors.Is(err, domain.ErrSongTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "song_too_large", "the song is larger than 100 MiB")
	case errors.Is(err, domain.ErrSongLength):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "song_length", "the song is shorter than 30 seconds or longer than 8 minutes")
	case errors.Is(err, domain.ErrNotAudio):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "not_audio", "the file is not audio the montage center can read")
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
		httpjson.WriteError(w, http.StatusServiceUnavailable, "compute_unavailable", "the montage compute worker is not available")
	case errors.Is(err, domain.ErrPhotosUnavailable):
		h.logger.WarnContext(r.Context(), "photo library unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available")
	default:
		httpjson.WriteInternal(w, r, h.logger, err)
	}
}

func (h *handler) projects(w http.ResponseWriter, r *http.Request, user accounts.User) {
	page, err := h.store.Projects(r.Context(), user.ID, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}

type createRequest struct {
	RequestKey string   `json:"requestKey"`
	Name       string   `json:"name"`
	AssetIDs   []string `json:"assetIds"`
}

func (h *handler) createProject(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request createRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	detail, created, err := h.store.CreateProject(r.Context(), user.ID, accounts.SessionToken(r.Context()), request.RequestKey, request.Name, request.AssetIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpjson.Write(w, status, detail)
}

func (h *handler) project(w http.ResponseWriter, r *http.Request, user accounts.User) {
	detail, err := h.store.Project(r.Context(), user.ID, r.PathValue("projectID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *handler) updateProject(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var change domain.ProjectChange
	if !httpjson.Read(w, r, maxJSONBytes, &change) {
		return
	}
	detail, err := h.store.UpdateProject(r.Context(), user.ID, r.PathValue("projectID"), change)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *handler) deleteProject(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.store.DeleteProject(r.Context(), user.ID, r.PathValue("projectID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type addRequest struct {
	RequestKey string   `json:"requestKey"`
	AssetIDs   []string `json:"assetIds"`
}

func (h *handler) addAssets(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request addRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	detail, err := h.store.AddAssets(r.Context(), user.ID, accounts.SessionToken(r.Context()), r.PathValue("projectID"), request.RequestKey, request.AssetIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}
