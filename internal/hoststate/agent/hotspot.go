package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
)

func (c *Client) PhoneHotspot(ctx context.Context) (phonehotspot.State, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var state phonehotspot.State
	err := c.doJSON(ctx, c.operationClient, http.MethodGet, wire.PhoneHotspotPath, nil, &state)
	return state, err
}

func (c *Client) SetPhoneHotspot(ctx context.Context, enabled bool) (phonehotspot.State, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var state phonehotspot.State
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, wire.PhoneHotspotPath, phonehotspot.Request{Enabled: &enabled}, &state)
	return state, err
}

var _ phonehotspot.Switch = (*Client)(nil)

func (c *Client) SavePhoneHotspotSettings(ctx context.Context, change phonehotspot.SettingsChange) (phonehotspot.Settings, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var settings phonehotspot.Settings
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, wire.PhoneHotspotSettingsPath, change, &settings)
	return settings, err
}
