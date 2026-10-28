package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

// systemLogsTimeout leaves the Host Agent's own read deadline room to answer.
const systemLogsTimeout = 20 * time.Second

// ReadLogs reads allowlisted service logs through the Host Agent.
func (c *Client) ReadLogs(ctx context.Context, q systemlog.Query) (systemlog.Page, error) {
	ctx, cancel := context.WithTimeout(ctx, systemLogsTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host-agent"+wire.SystemLogsPath+"?"+q.Values().Encode(), nil)
	if err != nil {
		return systemlog.Page{}, err
	}
	response, err := c.operationClient.Do(request)
	if err != nil {
		return systemlog.Page{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return systemlog.Page{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	// A full page of long lines is several megabytes.
	var page systemlog.Page
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&page); err != nil {
		return systemlog.Page{}, fmt.Errorf("decode host agent response: %w", err)
	}
	if page.Items == nil {
		page.Items = []systemlog.Entry{}
	}
	return page, nil
}

var _ systemlog.Reader = (*Client)(nil)
