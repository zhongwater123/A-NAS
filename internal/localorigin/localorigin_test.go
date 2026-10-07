package localorigin_test

import (
	"errors"
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

func TestCheckWrite(t *testing.T) {
	tests := []struct {
		name, host, contentType, origin string
		want                            error
	}{
		{name: "desktop request", host: "127.0.0.1:8080", contentType: "application/json; charset=utf-8", origin: "http://127.0.0.1:8080"},
		{name: "no origin from local tool", host: "localhost:8080", contentType: "application/json"},
		{name: "rebound host", host: "attacker.example:8080", contentType: "application/json", want: localorigin.ErrHost},
		{name: "form post", host: "127.0.0.1:8080", contentType: "text/plain", origin: "http://127.0.0.1:8080", want: localorigin.ErrContentType},
		{name: "cross origin", host: "127.0.0.1:8080", contentType: "application/json", origin: "http://attacker.example", want: localorigin.ErrOrigin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/v1/containers/x/actions", nil)
			request.Host = test.host
			request.Header.Set("Content-Type", test.contentType)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if err := localorigin.CheckWrite(request); !errors.Is(err, test.want) {
				t.Fatalf("CheckWrite() = %v, want %v", err, test.want)
			}
		})
	}
}
