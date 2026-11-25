package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
)

// systemUpdateTimeout covers reading the service's files and systemctl
// answering; starting the service does not wait for it.
const systemUpdateTimeout = 20 * time.Second

// SystemUpdate reads the system update service's state.
func (c *Client) SystemUpdate(ctx context.Context) (systemupdate.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, systemUpdateTimeout)
	defer cancel()
	var status systemupdate.Status
	err := c.doJSON(ctx, c.operationClient, http.MethodGet, wire.SystemUpdatePath, nil, &status)
	return status, err
}

// RequestSystemUpdate asks the service for an operation; it is
// systemupdate.ErrBusy while the service runs.
func (c *Client) RequestSystemUpdate(ctx context.Context, request systemupdate.Request) (systemupdate.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, systemUpdateTimeout)
	defer cancel()
	var status systemupdate.Status
	err := c.doJSON(ctx, c.operationClient, http.MethodPost, wire.SystemUpdatePath, request, &status)
	return status, err
}

// SetSystemUpdateSettings replaces the administrator's settings.
func (c *Client) SetSystemUpdateSettings(ctx context.Context, settings systemupdate.Settings) (systemupdate.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, systemUpdateTimeout)
	defer cancel()
	var status systemupdate.Status
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, wire.SystemUpdateSettingsPath, settings, &status)
	return status, err
}

var _ systemupdate.Updater = (*Client)(nil)
