package filebroker

import (
	"context"
	"errors"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShareEpochDrainsDisconnectedClientAndRejectsDelayedPublication(t *testing.T) {
	h := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	// The test worker holds the rename of a stuck-* file until it is
	// released, whether or not the call is cancelled, as a rename does not
	// look at its context.
	source := filepath.Join(h.volume, "stuck-source")
	target := filepath.Join(h.volume, "published")
	if err := os.WriteFile(source, []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	epoch, err := h.client.ResetShares(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.client.Lstat(ctx, source); err != nil {
		t.Fatal(err)
	}
	release := func() {
		if err := os.WriteFile(source+".release", nil, 0o600); err != nil {
			t.Error(err)
		}
	}
	pub, cancel := context.WithCancel(WithSharePublication(ctx, epoch, "share"))
	renamed := make(chan error, 1)
	go func() { renamed <- h.client.Rename(pub, source, target) }()
	// The publication was accepted and its rename is under way.
	waitForFile(t, source+".started")
	cancel()
	if err := <-renamed; !errors.Is(err, context.Canceled) {
		release()
		t.Fatal(err)
	}
	reset := make(chan string, 1)
	go func() {
		v, e := h.client.ResetShares(context.Background())
		if e != nil {
			reset <- ""
		} else {
			reset <- v
		}
	}()
	select {
	case <-reset:
		release()
		t.Fatal("reset returned while orphan worker could rename")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	next := <-reset
	if err := h.client.CheckShareEpoch(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err := h.client.CheckShareEpoch(context.Background(), epoch); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("stale health: %v", err)
	}
	if next == "" || next == epoch {
		t.Fatal("epoch not rotated")
	}
	if data, e := os.ReadFile(target); e != nil || string(data) != "complete" {
		t.Fatalf("settled publication %q %v", data, e)
	}
	if e := h.client.Mkdir(WithSharePublication(ctx, epoch, "share"), filepath.Join(h.volume, "late")); !errors.Is(e, files.ErrForbidden) {
		t.Fatalf("old process published: %v", e)
	}
	if e := h.client.FenceShare(context.Background(), next, "share"); e != nil {
		t.Fatal(e)
	}
	if e := h.client.Mkdir(WithSharePublication(ctx, next, "share"), filepath.Join(h.volume, "revoked")); !errors.Is(e, files.ErrForbidden) {
		t.Fatalf("revoked share published: %v", e)
	}
	if e := h.client.Mkdir(WithSharePublication(ctx, next, "unrelated"), filepath.Join(h.volume, "other")); e != nil {
		t.Fatal(e)
	}
}
