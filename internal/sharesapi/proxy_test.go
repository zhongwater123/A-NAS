//go:build linux

package sharesapi

import (
	"context"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestProxyForwardsOriginalAuthenticatedTokenOnly(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "shares.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan http.Header, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received <- r.Header.Clone(); w.WriteHeader(204) })}
	go server.Serve(listener)
	defer server.Close()
	proxy := NewProxy(socket, nil)
	req := httptest.NewRequest("GET", PathPrefix, nil)
	req.Header.Set("Cookie", "session=untrusted")
	req.Header.Set("Authorization", "Bearer untrusted")
	req.Header.Set(SessionHeader, "forged")
	req.Header.Set("X-CSRF-Token", "csrf")
	req = req.WithContext(accounts.WithSessionToken(context.Background(), "verified"))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("proxy status %d", w.Code)
	}
	headers := <-received
	if headers.Get(SessionHeader) != "verified" || headers.Get("Cookie") != "" || headers.Get("Authorization") != "" || headers.Get("X-CSRF-Token") != "" {
		t.Fatalf("forwarded headers %+v", headers)
	}
}
