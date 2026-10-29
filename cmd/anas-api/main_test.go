package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type recordingVolumeGuard struct {
	root  string
	write bool
}

func (g *recordingVolumeGuard) Check(_ context.Context, root string, write bool) error {
	g.root = root
	g.write = write
	return nil
}

// File writes and app folders both go through the Host Agent; the Product
// Service's sandbox shows the volume read-only (issue #38 follow-up).
func TestVolumeChecksNeverRequireProductServiceWriteAccess(t *testing.T) {
	recorder := &recordingVolumeGuard{}
	guard := sandboxedVolumeGuard{recorder}
	if err := guard.Check(context.Background(), "/srv/a-nas/data", true); err != nil {
		t.Fatal(err)
	}
	if recorder.root != "/srv/a-nas/data" {
		t.Fatalf("guard root = %q", recorder.root)
	}
	if recorder.write {
		t.Fatal("a write check required direct Product Service write access")
	}
}

// Startup synchronization ran once: a Host Agent that was still starting, or
// one account it could not converge, left identities unsynchronized until the
// next restart.
func TestIdentitySynchronizationRetriesUntilItSucceeds(t *testing.T) {
	retryMin, retryMax := identitySyncRetryMin, identitySyncRetryMax
	t.Cleanup(func() { identitySyncRetryMin, identitySyncRetryMax = retryMin, retryMax })
	identitySyncRetryMin, identitySyncRetryMax = time.Millisecond, 4*time.Millisecond
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	attempts := 0
	syncIdentities(context.Background(), func(context.Context) error {
		if attempts++; attempts < 4 {
			return errors.New("host agent state is unavailable: status 503")
		}
		return nil
	}, logger)
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		syncIdentities(ctx, func(context.Context) error { return errors.New("permission denied") }, logger)
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("synchronization kept retrying after the service stopped")
	}
}
