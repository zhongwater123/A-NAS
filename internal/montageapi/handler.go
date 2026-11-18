// Package montageapi exposes 剪辑中心 under /api/v1/montage. It does not
// authenticate: the montage service, or the Product Service in development,
// wraps it so that every request carries the signed-in user and the session
// token (accounts.WithUser, accounts.WithSessionToken), and writes have
// passed the Product Service's CSRF check.
package montageapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/montage"
)

const PathPrefix = "/api/v1/montage"

// AssistantHeader tells the montage service whether the request registered
// a planning grant for the member's VNAS (docs/architecture/montage-center.md,
// 规划授权). Only the Product Service sets it, on the requests that register
// one; it removes the header from every other request first.
const AssistantHeader = "X-A-NAS-Assistant"

// Unavailable is the error code while the montage store cannot be used, as
// when the data volume is offline or read-only.
const Unavailable = "montage_unavailable"

type handler struct {
	store  *montage.Store
	logger *slog.Logger
	mux    *apiroute.Mux
}

type route func(http.ResponseWriter, *http.Request, accounts.User)

// New returns the montage API. A nil store answers 503 montage_unavailable.
func New(store *montage.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, mux: apiroute.NewMux()}
	routes := map[string]route{
		"GET /projects":                     h.projects,
		"POST /projects":                    h.createProject,
		"GET /projects/{projectID}":         h.project,
		"PATCH /projects/{projectID}":       h.updateProject,
		"DELETE /projects/{projectID}":      h.deleteProject,
		"POST /projects/{projectID}/assets": h.addAssets,
	}
	for pattern, serve := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(serve))
	}
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

// maxJSONBytes bounds a JSON request: at most montage.MaxAssets IDs.
const maxJSONBytes = 64 << 10

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, montage.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, montage.ErrTooManyAssets):
		httpjson.WriteError(w, http.StatusBadRequest, "too_many_assets", "at most 500 videos at once")
	case errors.Is(err, montage.ErrInvalid):
		httpjson.WriteError(w, http.StatusBadRequest, "validation_failed", "the request is not valid")
	case errors.Is(err, montage.ErrAssetsRefused):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "assets_unavailable", "a video is not one you can add")
	case errors.Is(err, montage.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "conflict", "the request key belongs to another project")
	case errors.Is(err, montage.ErrSessionEnded):
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
	case errors.Is(err, montage.ErrPhotosUnavailable):
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
	var change montage.ProjectChange
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
