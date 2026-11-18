// Package montageapi exposes 剪辑中心 under /api/v1/montage. It does not
// authenticate: the montage service, or the Product Service in development,
// wraps it so that every request carries the signed-in user and the session
// token (accounts.WithUser, accounts.WithSessionToken), and writes have
// passed the Product Service's CSRF check.
package montageapi

import (
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
		"GET /projects": h.projects,
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

type projectList struct {
	Items []montage.Project `json:"items"`
}

func (h *handler) projects(w http.ResponseWriter, r *http.Request, user accounts.User) {
	projects, err := h.store.Projects(r.Context(), user.ID)
	if err != nil {
		httpjson.WriteInternal(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, projectList{Items: projects})
}
