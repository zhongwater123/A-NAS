package photoservice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// failingListener fails to accept a few times, as when the process is out
// of descriptors, and is closed after that.
type failingListener struct {
	net.Listener
	failures int
}

func (l *failingListener) Accept() (net.Conn, error) {
	if l.failures > 0 {
		l.failures--
		return nil, errors.New("accept: too many open files")
	}
	return nil, net.ErrClosed
}

func (*failingListener) Close() error { return nil }

// A failed accept on the grant socket is waited out: only closing it ends
// serving, so it never takes the photo service down with it.
func TestTheGrantSocketOutlastsAFailedAccept(t *testing.T) {
	listener := &failingListener{failures: 3}
	grants := newGrantServer(listener, &atomic.Pointer[photos.Service]{}, nil, nil, nil, 0, slog.New(slog.DiscardHandler))
	served := make(chan error, 1)
	go func() { served <- grants.serve() }()
	select {
	case err := <-served:
		if err != nil || listener.failures != 0 {
			t.Fatalf("serve() = %v with %d failures left; want nil after all of them", err, listener.failures)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve() did not stop once the listener closed")
	}
}

// A search of windows is someone using the photo library, as a photo search
// is: background AI work waits until the quiet period after it. The other
// grant requests are the montage center's background work and hold nothing
// back.
func TestAWindowSearchHoldsBackgroundAIWorkBack(t *testing.T) {
	ctx := context.Background()
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	var opened atomic.Pointer[photos.Service]
	opened.Store(service)
	gate := NewGate(time.Hour, nil)
	gate.pressure = func(string) (float64, error) { return 0, nil }
	gate.available = nil
	gate.lastUse.Store(0)
	server := newGrantServer(nil, &opened, nil, nil, gate, 0, slog.New(slog.DiscardHandler))
	for _, request := range []photosapi.GrantRequest{
		{Op: photosapi.GrantLookup, GrantID: "grant:missing", AssetIDs: []string{"photo:1"}},
		{Op: photosapi.GrantAnalyze, GrantID: "grant:missing", AssetIDs: []string{"photo:1"}},
		{Op: photosapi.GrantList},
	} {
		server.handleWith(ctx, request, nil)
		if open, reason := gate.Open(ctx, 0); !open {
			t.Fatalf("after %s the gate is closed: %s", request.Op, reason)
		}
	}
	response, _ := server.handleWith(ctx, photosapi.GrantRequest{Op: photosapi.GrantSearchWindows, GrantID: "grant:missing",
		Texts: []string{"sea"}, K: 5}, nil)
	if response.Code != photosapi.GrantNotFound {
		t.Fatalf("search of no grant = %+v", response)
	}
	if open, reason := gate.Open(ctx, 0); open || reason != "foreground" {
		t.Fatalf("after a search the gate = %v, %q; want closed for the foreground", open, reason)
	}
	if using := gate.using.Load(); using != 0 {
		t.Fatalf("%d uses still open after the search was answered", using)
	}
}
