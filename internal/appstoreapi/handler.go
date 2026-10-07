// Package appstoreapi exposes the App Center under /api/v1/apps. Installs and
// uninstalls are state-changing and pass localorigin.CheckWrite; an install
// must carry the digest of the plan the owner confirmed.
package appstoreapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/agent"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
)

const PathPrefix = "/api/v1/apps"

type DataSource string

const (
	DataSourceSimulated DataSource = "simulated"
	DataSourceLive      DataSource = "live"
)

type handler struct {
	store      appstore.Store
	dataSource DataSource
	logger     *slog.Logger
}

// New returns the App Center API; a nil store disables it.
func New(store appstore.Store, dataSource DataSource, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{store: store, dataSource: dataSource, logger: logger}
}

func Matches(path string) bool {
	return path == PathPrefix || strings.HasPrefix(path, PathPrefix+"/")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathPrefix {
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		apps, err := h.store.Apps(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, appsResponse{DataSource: h.dataSource, AppsDocument: agent.AppsFromDomain(apps)})
		return
	}

	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix+"/"), "/")
	if !ok || !appstore.ValidID(id) || strings.Contains(operation, "/") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch operation {
	case "icon":
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		icon, contentType, err := h.store.Icon(r.Context(), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		// Icons are fixed for a given catalog; SVGs must not run scripts.
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(icon)
	case "plan":
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		plan, err := h.store.Plan(r.Context(), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, agent.PlanFromDomain(plan))
	case "install", "uninstall":
		if !allow(w, r, http.MethodPost) {
			return
		}
		if err := localorigin.CheckWrite(r); err != nil {
			writeError(w, http.StatusForbidden, "forbidden", "app changes must come from the local desktop")
			return
		}
		if !h.enabled(w) {
			return
		}
		var request agent.InstallRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || (operation == "install" && len(request.Digest) != 64) {
			writeError(w, http.StatusBadRequest, "invalid_request", "install requires the confirmed plan digest")
			return
		}
		var job appstore.Job
		var err error
		if operation == "install" {
			job, err = h.store.Install(r.Context(), id, request.Digest)
		} else {
			job, err = h.store.Uninstall(r.Context(), id)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.logger.InfoContext(r.Context(), "app job requested", "app", id, "action", job.Action)
		writeJSON(w, http.StatusAccepted, agent.JobFromDomain(job))
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (h *handler) enabled(w http.ResponseWriter) bool {
	if h.store != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "apps_disabled", "the App Center is disabled")
	return false
}

// fail keeps conflict details (which port, which container) but hides engine internals.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := agent.StatusForError(err)
	message := err.Error()
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "app request failed", "path", r.URL.Path, "error", err)
		message = "the container engine could not complete the request"
	}
	var policy *appstore.PolicyError
	if errors.As(err, &policy) {
		message = "this app's manifest does not meet the install policy"
	}
	writeError(w, status, code, message)
}

type appsResponse struct {
	DataSource DataSource `json:"dataSource"`
	agent.AppsDocument
}

func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}
