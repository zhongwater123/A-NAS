package rootbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

func TestShareEpochDrainsDisconnectedClientAndRejectsDelayedPublication(t *testing.T) {
	h := newHarness(t)
	ctx := accountid.WithSessionToken(context.Background(), "alice-token")
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
	pub, cancel := context.WithCancel(filebroker.WithSharePublication(ctx, epoch, "share"))
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
	if err := h.client.CheckShareEpoch(context.Background(), epoch); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("stale health: %v", err)
	}
	if next == "" || next == epoch {
		t.Fatal("epoch not rotated")
	}
	if data, e := os.ReadFile(target); e != nil || string(data) != "complete" {
		t.Fatalf("settled publication %q %v", data, e)
	}
	if e := h.client.Mkdir(filebroker.WithSharePublication(ctx, epoch, "share"), filepath.Join(h.volume, "late")); !errors.Is(e, filesystem.ErrForbidden) {
		t.Fatalf("old process published: %v", e)
	}
	if e := h.client.FenceShare(context.Background(), next, "share"); e != nil {
		t.Fatal(e)
	}
	if e := h.client.Mkdir(filebroker.WithSharePublication(ctx, next, "share"), filepath.Join(h.volume, "revoked")); !errors.Is(e, filesystem.ErrForbidden) {
		t.Fatalf("revoked share published: %v", e)
	}
	if e := h.client.Mkdir(filebroker.WithSharePublication(ctx, next, "unrelated"), filepath.Join(h.volume, "other")); e != nil {
		t.Fatal(e)
	}
}
