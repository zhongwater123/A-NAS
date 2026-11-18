package photoservice

import (
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
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
	grants := newGrantServer(listener, &atomic.Pointer[photos.Service]{}, nil, nil, 0, slog.New(slog.DiscardHandler))
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
