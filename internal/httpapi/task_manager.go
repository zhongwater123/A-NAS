package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func (h *handler) handleTaskManager(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	reader, ok := h.reader.(taskmanager.Reader)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var result any
	var err error
	if r.URL.Path == "/api/v1/task-manager/device" {
		var snapshot taskmanager.DeviceSnapshot
		snapshot, err = reader.ReadDeviceStatus(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid device observation")
		}
		result = struct {
			taskmanager.DeviceSnapshot
			DataSource DataSource `json:"dataSource"`
		}{snapshot, h.dataSource}
	} else {
		var snapshot taskmanager.TaskSnapshot
		snapshot, err = reader.ReadTasks(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid task observation")
		}
		result = struct {
			taskmanager.TaskSnapshot
			DataSource DataSource `json:"dataSource"`
		}{snapshot, h.dataSource}
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "task manager read failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
