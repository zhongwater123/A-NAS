// Package containersapi exposes container management to the Web desktop under
// /api/v1/containers. Reads pass through to a containers.Manager; the only
// writes are typed lifecycle actions guarded by localorigin.CheckWrite.
package containersapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/containers"
	"github.com/zhongwater123/A-NAS/internal/containers/agent"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
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
	mux        *apiroute.Mux
}

// New returns the container API. A nil manager means container management is
// disabled; every request then answers 503 containers_disabled.
func New(manager containers.Manager, dataSource DataSource, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{manager: manager, dataSource: dataSource, logger: logger, mux: apiroute.NewMux()}
	h.mux.Only(http.MethodGet, PathPrefix, h.snapshot)
	h.mux.Only(http.MethodGet, PathPrefix+"/{containerID}/logs", h.logs)
	h.mux.Only(http.MethodPost, PathPrefix+"/{containerID}/actions", h.act)
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the container API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, "", nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *handler) snapshot(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	snapshot, err := h.manager.Snapshot(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, snapshotResponse{DataSource: h.dataSource, SnapshotDocument: agent.SnapshotFromDomain(snapshot)})
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	tail := containers.MaxLogLines
	if value := r.URL.Query().Get("tail"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > containers.MaxLogLines {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "tail must be between 1 and 500")
			return
		}
		tail = parsed
	}
	lines, err := h.manager.Logs(r.Context(), r.PathValue("containerID"), tail)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, agent.LogsFromDomain(lines))
}

func (h *handler) act(w http.ResponseWriter, r *http.Request) {
	if err := localorigin.CheckWrite(r); err != nil {
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "container actions must come from the local desktop")
		return
	}
	if !h.enabled(w) {
		return
	}
	var body struct {
		Action containers.Action `json:"action"`
	}
	if err := httpjson.Decode(w, r, 1024, &body); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_request", "action must be start, stop or restart")
		return
	}
	if !body.Action.Valid() {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "action must be start, stop or restart")
		return
	}
	// Stop and restart wait up to the engine's grace period, which can exceed
	// the server-wide write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(actionWriteBudget))
	id := r.PathValue("containerID")
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
	httpjson.WriteError(w, http.StatusServiceUnavailable, "containers_disabled", "container management is disabled")
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
	httpjson.WriteError(w, status, code, messages[code])
}

type snapshotResponse struct {
	DataSource DataSource `json:"dataSource"`
	agent.SnapshotDocument
}
