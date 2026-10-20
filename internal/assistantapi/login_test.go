package assistantapi

import (
	"net/http"
	"testing"
)

func TestARestartedInstanceIsLoggedInToAgainOnce(t *testing.T) {
	h := newHarness(t)
	if status, body := h.request(http.MethodGet, "/sessions", ""); status != http.StatusOK {
		t.Fatalf("sessions = %d %s", status, body)
	}
	// The instance restarted and refuses the token it signed before; the
	// request logs in again and goes through.
	h.serve.restart()
	if status, body := h.request(http.MethodGet, "/sessions", ""); status != http.StatusOK {
		t.Fatalf("after a restart sessions = %d %s", status, body)
	}
	// So does the window's gateway.
	h.serve.restart()
	h.open()
	h.serve.mu.Lock()
	logins := h.serve.logins
	h.serve.mu.Unlock()
	if logins != 3 {
		t.Fatalf("logins = %d, want the first and one per restart", logins)
	}
	// Further requests use the cached token.
	for range 3 {
		h.request(http.MethodGet, "/sessions", "")
	}
	h.serve.mu.Lock()
	defer h.serve.mu.Unlock()
	if h.serve.logins != 3 {
		t.Fatalf("logins = %d after more requests, want none new", h.serve.logins)
	}
}
