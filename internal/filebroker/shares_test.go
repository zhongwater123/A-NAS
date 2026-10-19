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
	source := filepath.Join(h.volume, "source")
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
	h.server.mu.Lock()
	var w *worker
	for _, v := range h.server.workers {
		w = v
	}
	h.server.mu.Unlock()
	w.mu.Lock()
	pub, cancel := context.WithCancel(WithSharePublication(ctx, epoch, "share"))
	renamed := make(chan error, 1)
	go func() { renamed <- h.client.Rename(pub, source, target) }()
	deadline := time.Now().Add(time.Second)
	for {
		if !h.server.shareMu.TryLock() {
			break
		}
		h.server.shareMu.Unlock()
		if time.Now().After(deadline) {
			w.mu.Unlock()
			t.Fatal("publication not accepted")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-renamed; !errors.Is(err, context.Canceled) {
		w.mu.Unlock()
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
		w.mu.Unlock()
		t.Fatal("reset returned while orphan worker could rename")
	case <-time.After(20 * time.Millisecond):
	}
	w.mu.Unlock()
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
