package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackHotspotProxyMarksSessionsSecure(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://hotspot.example.test:8443/api/v1/session", nil)
	r.RemoteAddr = "127.0.0.1:20000"
	r.Header.Set("X-Forwarded-For", "192.168.173.20")
	r.Header.Set("X-Forwarded-Proto", "https")
	if !overHTTPS(r) {
		t.Fatal("the TLS-terminating hotspot proxy must receive Secure cookies")
	}
	r.Header.Set("X-Forwarded-Proto", "http")
	if overHTTPS(r) {
		t.Fatal("the LAN Caddy entry still serves HTTP")
	}
	r.RemoteAddr = "192.0.2.10:20000"
	r.Header.Set("X-Forwarded-Proto", "https")
	if overHTTPS(r) {
		t.Fatal("a non-loopback sender's forwarded scheme was trusted")
	}
}
