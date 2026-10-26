// Package taskmanagerapi serves the task manager's read-only observations
// of the device and its tasks (docs/specs/task-manager.md) to members as well
// as administrators.
package taskmanagerapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const (
	devicePath = "/api/v1/task-manager/device"
	tasksPath  = "/api/v1/task-manager/tasks"
)

// API observes through reader; without one it answers
// task_manager_unavailable. dataSource tells the page whether the
// observations are simulated or live.
type API struct {
	reader     taskmanager.Reader
	dataSource string
	logger     *slog.Logger
}

func New(reader taskmanager.Reader, dataSource string, logger *slog.Logger) *API {
	return &API{reader: reader, dataSource: dataSource, logger: logger}
}

// Routes are mounted for every method, so that a write answers JSON 405
// after authentication instead of falling through to the catch-all 404.
func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Pattern: devicePath, Posture: apiroute.Session, Handler: a.serve},
		{Pattern: tasksPath, Posture: apiroute.Session, Handler: a.serve},
	}
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reader := a.reader
	if reader == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var result any
	var err error
	if r.URL.Path == devicePath {
		var snapshot taskmanager.DeviceSnapshot
		snapshot, err = reader.ReadDeviceStatus(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid device observation")
		}
		result = struct {
			taskmanager.DeviceSnapshot
			DataSource string `json:"dataSource"`
		}{snapshot, a.dataSource}
	} else {
		var snapshot taskmanager.TaskSnapshot
		snapshot, err = reader.ReadTasks(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid task observation")
		}
		result = struct {
			taskmanager.TaskSnapshot
			DataSource string `json:"dataSource"`
		}{snapshot, a.dataSource}
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "task manager read failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
