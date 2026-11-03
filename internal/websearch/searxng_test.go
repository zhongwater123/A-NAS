package websearch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestTheImageProxysRefusalsAreMissingImages(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "search.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status.Load() == http.StatusOK {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
			return
		}
		w.WriteHeader(int(status.Load()))
	})}
	go func() { _ = server.Serve(listener) }()
	client := NewSearXNG(socket)
	ctx := context.Background()

	// The remote host's own refusals, which SearXNG passes on, and its 400
	// for an address it could not fetch.
	for _, code := range []int32{http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest, http.StatusBadGateway} {
		status.Store(code)
		if _, err := client.Thumbnail(ctx, "https://example.org/a.png", "signature"); !errors.Is(err, ErrNotFound) {
			t.Errorf("status %d = %v, want ErrNotFound", code, err)
		}
	}
	status.Store(http.StatusOK)
	image, err := client.Thumbnail(ctx, "https://example.org/a.png", "signature")
	if err != nil || image.ContentType != "image/png" || string(image.Body) != "png" {
		t.Fatalf("image = %+v, %v", image, err)
	}
	// A service that is not there is unavailable, not a missing image.
	_ = server.Close()
	if _, err := client.Thumbnail(ctx, "https://example.org/a.png", "signature"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("closed service = %v, want a connection error", err)
	}
}
