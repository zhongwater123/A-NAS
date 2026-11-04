package localorigin_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/localorigin"
)

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:8080":      true,
		"localhost:18080":     true,
		"[::1]:8080":          true,
		"127.0.0.1":           true,
		"192.168.1.10:8080":   false,
		"attacker.example:80": false,
		"":                    false,
	} {
		if got := localorigin.LoopbackHost(host); got != want {
			t.Errorf("LoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDeviceHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:8080":      true,
		"localhost:8080":      true,
		"[::1]:8080":          true,
		"172.18.45.48":        true,
		"172.18.45.48:80":     true,
		"[fe80::1]:80":        true,
		"attacker.example:80": false,
		"a-nas.local":         false,
		"":                    false,
	} {
		if got := localorigin.DeviceHost(host); got != want {
			t.Errorf("DeviceHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestCheckWrite(t *testing.T) {
	tests := []struct {
		name, host, contentType, origin, fetchSite string
		want                                       error
	}{
		{name: "desktop request", host: "127.0.0.1:8080", contentType: "application/json; charset=utf-8", origin: "http://127.0.0.1:8080"},
		{name: "no origin from local tool", host: "localhost:8080", contentType: "application/json"},
		{name: "LAN desktop request", host: "172.18.45.48", contentType: "application/json", origin: "http://172.18.45.48"},
		{name: "LAN request from another device's page", host: "172.18.45.48", contentType: "application/json", origin: "http://172.18.45.9", want: localorigin.ErrOrigin},
		{name: "rebound host", host: "attacker.example:8080", contentType: "application/json", want: localorigin.ErrHost},
		{name: "form post", host: "127.0.0.1:8080", contentType: "text/plain", origin: "http://127.0.0.1:8080", want: localorigin.ErrContentType},
		{name: "cross origin", host: "127.0.0.1:8080", contentType: "application/json", origin: "http://attacker.example", want: localorigin.ErrOrigin},
		{name: "browser says same origin", host: "172.18.45.48", contentType: "application/json", origin: "http://172.18.45.48", fetchSite: "same-origin"},
		// An installed app's page on another port of the device is the same
		// site, not the same origin (#161).
		{name: "app on another port", host: "172.18.45.48", contentType: "application/json", origin: "http://172.18.45.48:8096", fetchSite: "same-site", want: localorigin.ErrOrigin},
		{name: "browser says cross site", host: "127.0.0.1:8080", contentType: "application/json", fetchSite: "cross-site", want: localorigin.ErrOrigin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/containers/x/actions", nil)
			request.Host = test.host
			request.Header.Set("Content-Type", test.contentType)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if err := localorigin.CheckWrite(request); !errors.Is(err, test.want) {
				t.Fatalf("CheckWrite() = %v, want %v", err, test.want)
			}
		})
	}
}
