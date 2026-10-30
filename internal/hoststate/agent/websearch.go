package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/websearch"
)

// webSearchTimeout covers systemctl answering; starting the service does
// not wait for it.
const webSearchTimeout = 20 * time.Second

// WebSearch reads the administrator's switch of web search (ADR 0030).
func (c *Client) WebSearch(ctx context.Context) (websearch.ServiceState, error) {
	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	var state websearch.ServiceState
	err := c.doJSON(ctx, c.operationClient, http.MethodGet, wire.WebSearchPath, nil, &state)
	return state, err
}

// SetWebSearch turns web search on or off.
func (c *Client) SetWebSearch(ctx context.Context, enabled bool) (websearch.ServiceState, error) {
	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	var state websearch.ServiceState
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, wire.WebSearchPath, wire.WebSearchRequest{Enabled: enabled}, &state)
	return state, err
}

var _ websearch.Switch = (*Client)(nil)
