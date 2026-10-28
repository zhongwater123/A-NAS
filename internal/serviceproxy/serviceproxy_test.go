package serviceproxy_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// A browser's request reaches the service with the session token of the
// request's context, whatever it sent itself, and without its cookies or
// CSRF token; in this process as through the socket.
func TestOnlyTheVerifiedSessionTokenReachesTheService(t *testing.T) {
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "service.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan http.Header, 2)
	service := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: service}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	for name, proxy := range map[string]http.Handler{
		"socket":     serviceproxy.New(socket, serviceproxy.Unavailable{Code: "service_unavailable"}, nil),
		"in process": serviceproxy.Local(service),
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
		request.Header.Set("Cookie", "anas_session=untrusted")
		request.Header.Set("Authorization", "Bearer untrusted")
		request.Header.Set(serviceproxy.SessionHeader, "forged")
		request.Header.Set("X-CSRF-Token", "csrf")
		request = request.WithContext(accounts.WithSessionToken(context.Background(), "verified"))
		recorder := httptest.NewRecorder()
		proxy.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s: status %d", name, recorder.Code)
		}
		headers := <-received
		if headers.Get(serviceproxy.SessionHeader) != "verified" || headers.Get("Cookie") != "" || headers.Get("Authorization") != "" || headers.Get("X-CSRF-Token") != "" {
			t.Fatalf("%s: forwarded headers %+v", name, headers)
		}
	}

	unreachable := httptest.NewRecorder()
	serviceproxy.New(filepath.Join(dir, "absent.sock"), serviceproxy.Unavailable{Code: "service_unavailable", Message: "gone"}, nil).
		ServeHTTP(unreachable, httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil))
	if unreachable.Code != http.StatusServiceUnavailable || unreachable.Body.String() != `{"error":{"code":"service_unavailable","message":"gone"}}`+"\n" {
		t.Fatalf("unreachable service = %d %s", unreachable.Code, unreachable.Body.String())
	}
}
