package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
)

// Hermes commits a write to a stored conversation only behind its fence: a
// request id of the write's own, and the revision and generation the
// conversation had when the write was decided. A write without them is refused
// (409 client_upgrade_required). Hermes hands out the counters for exactly this
// purpose (mutation-snapshot), and replays the receipt when the same request id
// arrives again.

// sessionFence is Hermes's mutation snapshot of one conversation.
type sessionFence struct {
	Exists     bool   `json:"exists"`
	Revision   int64  `json:"runtime_revision"`
	Generation *int64 `json:"runtime_generation"`
}

// writeSession renames (PATCH, with the fields to change) or deletes (DELETE) a
// stored conversation, and answers like rest. A conversation that is not there
// answers 404. A conflict is tried once more with fresh counters: a turn that
// ended between the snapshot and the write moved them.
func (h *Handler) writeSession(ctx context.Context, account, method, id string, fields map[string]any) (int, []byte, error) {
	status, data, err := h.fencedWrite(ctx, account, method, id, fields)
	if err == nil && status == http.StatusConflict {
		return h.fencedWrite(ctx, account, method, id, fields)
	}
	return status, data, err
}

func (h *Handler) fencedWrite(ctx context.Context, account, method, id string, fields map[string]any) (int, []byte, error) {
	path := "/api/sessions/" + id
	status, data, err := h.rest(ctx, account, http.MethodGet, path+"/mutation-snapshot", nil)
	if err != nil || status != http.StatusOK {
		return status, data, err
	}
	var fence sessionFence
	if err := json.Unmarshal(data, &fence); err != nil {
		return 0, nil, fmt.Errorf("the conversation's mutation snapshot is undecodable: %w", err)
	}
	if !fence.Exists {
		return http.StatusNotFound, []byte(`{"detail":"not_found"}`), nil
	}
	request, err := newCommandID()
	if err != nil {
		return 0, nil, err
	}
	if method == http.MethodDelete {
		query := url.Values{"request_id": {request}, "expected_revision": {fmt.Sprint(fence.Revision)}}
		if fence.Generation != nil {
			query.Set("expected_generation", fmt.Sprint(*fence.Generation))
		}
		return h.rest(ctx, account, method, path+"?"+query.Encode(), nil)
	}
	body := map[string]any{"request_id": request, "expected_revision": fence.Revision}
	if fence.Generation != nil {
		body["expected_generation"] = *fence.Generation
	}
	maps.Copy(body, fields)
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	return h.rest(ctx, account, method, path, payload)
}
