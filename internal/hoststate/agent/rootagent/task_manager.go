package rootagent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func (h *handler) handleTaskManager(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	reader, ok := h.observer.(taskmanager.Reader)
	if !ok {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var result any
	var err error
	if r.URL.Path == wire.DeviceStatusPath {
		var snapshot taskmanager.DeviceSnapshot
		snapshot, err = reader.ReadDeviceStatus(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid device observation")
		}
		result = snapshot
	} else {
		var snapshot taskmanager.TaskSnapshot
		snapshot, err = reader.ReadTasks(ctx)
		if err == nil && !snapshot.Valid() {
			err = errors.New("invalid task observation")
		}
		result = snapshot
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "task manager read failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
