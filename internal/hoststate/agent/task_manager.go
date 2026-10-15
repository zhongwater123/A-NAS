package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const deviceStatusPath = "/v1/task-manager/device"
const tasksPath = "/v1/task-manager/tasks"

func (c *Client) ReadDeviceStatus(ctx context.Context) (taskmanager.DeviceSnapshot, error) {
	var snapshot taskmanager.DeviceSnapshot
	if err := c.getTaskManager(ctx, deviceStatusPath, &snapshot); err != nil {
		return snapshot, err
	}
	if !snapshot.Valid() {
		return taskmanager.DeviceSnapshot{}, errors.New("invalid host agent device observation")
	}
	return snapshot, nil
}

func (c *Client) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	var snapshot taskmanager.TaskSnapshot
	if err := c.getTaskManager(ctx, tasksPath, &snapshot); err != nil {
		return snapshot, err
	}
	if !snapshot.Valid() {
		return taskmanager.TaskSnapshot{}, errors.New("invalid host agent task observation")
	}
	return snapshot, nil
}

func (c *Client) getTaskManager(ctx context.Context, path string, into any) error {
	client := &http.Client{Transport: c.httpClient.Transport, Timeout: 8 * time.Second}
	return c.getWithClient(ctx, path, into, client, 4<<20)
}

func (h *handler) handleTaskManager(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	reader, ok := h.observer.(taskmanager.Reader)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var result any
	var err error
	if r.URL.Path == deviceStatusPath {
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
		writeError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "task manager is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
