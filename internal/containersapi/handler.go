// Package containersapi exposes container management to the Web desktop under
// /api/v1/containers. Reads pass through to a containers.Manager; the only
// writes are typed lifecycle actions guarded by localorigin.CheckWrite.
package containersapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/containers"
	"github.com/zhongwater123/A-NAS/internal/containers/agent"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
)

const (
	PathPrefix        = "/api/v1/containers"
	actionWriteBudget = 30 * time.Second
)

type DataSource string

const (
	DataSourceSimulated DataSource = "simulated"
	DataSourceLive      DataSource = "live"
)

type handler struct {
	manager    containers.Manager
	dataSource DataSource
	logger     *slog.Logger
}

// New returns the container API. A nil manager means container management is
// disabled; every request then answers 503 containers_disabled.
func New(manager containers.Manager, dataSource DataSource, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{manager: manager, dataSource: dataSource, logger: logger}
}

// Matches reports whether a request path belongs to this API.
func Matches(path string) bool {
	return path == PathPrefix || strings.HasPrefix(path, PathPrefix+"/")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == PathPrefix {
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		snapshot, err := h.manager.Snapshot(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshotResponse{DataSource: h.dataSource, SnapshotDocument: agent.SnapshotFromDomain(snapshot)})
		return
	}

	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix+"/"), "/")
	if !ok || strings.Contains(operation, "/") || (operation != "actions" && operation != "logs") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if operation == "logs" {
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		tail := containers.MaxLogLines
		if value := r.URL.Query().Get("tail"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > containers.MaxLogLines {
				writeError(w, http.StatusBadRequest, "invalid_request", "tail must be between 1 and 500")
				return
			}
			tail = parsed
		}
		lines, err := h.manager.Logs(r.Context(), id, tail)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, agent.LogsFromDomain(lines))
		return
	}

	if !allow(w, r, http.MethodPost) {
		return
	}
	if err := localorigin.CheckWrite(r); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "container actions must come from the local desktop")
		return
	}
	if !h.enabled(w) {
		return
	}
	var body struct {
		Action containers.Action `json:"action"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || !body.Action.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_request", "action must be start, stop or restart")
		return
	}
	// Stop and restart wait up to the engine's grace period, which can exceed
	// the server-wide write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(actionWriteBudget))
	if err := h.manager.Act(r.Context(), id, body.Action); err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "container action requested", "container", id, "action", body.Action)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) enabled(w http.ResponseWriter) bool {
	if h.manager != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "containers_disabled", "container management is disabled")
	return false
}

// fail maps domain errors to the shared codes without exposing engine details.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := agent.StatusForError(err)
	messages := map[string]string{
		"invalid_request":        "container ID or action is invalid",
		"container_not_found":    "container not found",
		"containers_unavailable": "container engine is unavailable",
		"container_engine_error": "container engine rejected the request",
	}
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "container request failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, status, code, messages[code])
}

type snapshotResponse struct {
	DataSource DataSource `json:"dataSource"`
	agent.SnapshotDocument
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
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}
