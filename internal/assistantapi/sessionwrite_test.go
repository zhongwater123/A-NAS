package assistantapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestConversationWritesCarryHermessFence(t *testing.T) {
	h := newHarness(t)
	asked := func() []string {
		h.serve.mu.Lock()
		defer h.serve.mu.Unlock()
		calls := append([]string(nil), h.serve.rest...)
		h.serve.rest = nil
		return calls
	}

	// The fake refuses a write that names no request, or other counters than its snapshot's.
	status, body := h.request(http.MethodPatch, "/sessions/"+fakeStored, `{"title":"周末"}`)
	if status != http.StatusOK || body != `{"id":"`+fakeStored+`","title":"周末"}` {
		t.Fatalf("rename = %d %s", status, body)
	}
	if calls := asked(); len(calls) != 2 || calls[0] != "GET /api/sessions/"+fakeStored+"/mutation-snapshot" ||
		calls[1] != "PATCH /api/sessions/"+fakeStored {
		t.Fatalf("rename asked Hermes %v", calls)
	}

	if status, body := h.request(http.MethodDelete, "/sessions/"+fakeStored, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", status, body)
	}
	calls := asked()
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "DELETE /api/sessions/"+fakeStored+"?") {
		t.Fatalf("delete asked Hermes %v", calls)
	}
	query, err := url.ParseQuery(strings.SplitN(calls[1], "?", 2)[1])
	if err != nil || len(query.Get("request_id")) != 32 || query.Get("expected_revision") != "3" || query.Get("expected_generation") != "1" {
		t.Fatalf("delete named %v", query)
	}

	// A conflict is tried once more with fresh counters, and each try is a request of its own.
	if status, body := h.request(http.MethodDelete, "/sessions/20261015_130000_aaaaaa", ""); status != http.StatusServiceUnavailable ||
		!strings.Contains(body, "assistant_busy") {
		t.Fatalf("delete while a turn writes = %d %s", status, body)
	}
	calls = asked()
	if len(calls) != 4 || !strings.HasSuffix(calls[2], "/mutation-snapshot") || calls[1] == calls[3] {
		t.Fatalf("a busy conversation asked Hermes %v", calls)
	}

	// What Hermes no longer has is not written to.
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		if status, body := h.request(method, "/sessions/"+fakeGone, `{"title":"周末"}`); status != http.StatusNotFound ||
			!strings.Contains(body, "session_not_found") {
			t.Fatalf("%s of a conversation that is gone = %d %s", method, status, body)
		}
		if calls := asked(); len(calls) != 1 || !strings.HasSuffix(calls[0], "/mutation-snapshot") {
			t.Fatalf("%s of a conversation that is gone asked Hermes %v", method, calls)
		}
	}
}
