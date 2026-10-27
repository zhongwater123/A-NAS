package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func (c *Client) ReadDeviceStatus(ctx context.Context) (taskmanager.DeviceSnapshot, error) {
	var snapshot taskmanager.DeviceSnapshot
	if err := c.getTaskManager(ctx, wire.DeviceStatusPath, &snapshot); err != nil {
		return snapshot, err
	}
	if !snapshot.Valid() {
		return taskmanager.DeviceSnapshot{}, errors.New("invalid host agent device observation")
	}
	return snapshot, nil
}

func (c *Client) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	var snapshot taskmanager.TaskSnapshot
	if err := c.getTaskManager(ctx, wire.TasksPath, &snapshot); err != nil {
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
