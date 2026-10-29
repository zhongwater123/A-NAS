package filebroker

import (
	"context"
	"encoding/json"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

// PrepareChange asks the executor to prepare a call of 提交更改 for the
// member signed in on the context's session; it returns the card.
func (c *Client) PrepareChange(ctx context.Context, sessionID, requestID string, arguments json.RawMessage) (json.RawMessage, error) {
	return c.change(ctx, wire.OpChangePrepare, &wire.ChangeRequest{SessionID: sessionID, RequestID: requestID, Arguments: arguments})
}

// DecideChange approves, rejects or expires one of the member's changes.
func (c *Client) DecideChange(ctx context.Context, id, decision string) (json.RawMessage, error) {
	return c.change(ctx, wire.OpChangeDecide, &wire.ChangeRequest{ID: id, Decision: decision})
}

// GetChange returns one of the member's changes as its card shows it.
func (c *Client) GetChange(ctx context.Context, id string) (json.RawMessage, error) {
	return c.change(ctx, wire.OpChangeGet, &wire.ChangeRequest{ID: id})
}

func (c *Client) change(ctx context.Context, op string, change *wire.ChangeRequest) (json.RawMessage, error) {
	result, file, err := c.call(ctx, wire.Request{Op: op, Change: change})
	wire.CloseFile(file)
	if err != nil {
		return nil, err
	}
	return result.Change, nil
}
