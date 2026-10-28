package shares

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type pollingProbeKey struct{}

type pollingBackend struct {
	testBackend
	fences      atomic.Int64
	totalFences atomic.Int64
}

func TestUnavailableAccountSnapshotStopsAcceptedWork(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	backend := &testBackend{save: func(ctx context.Context, _ []Entry, _ func(string) (io.ReadCloser, error), _ func(string, func() error) error) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	store, _ := setup(t, backend)
	share := publish(t, store)
	reader, _, err := store.OpenContent(context.Background(), bob, share.ID, share.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, err = store.Save(context.Background(), bob, share.ID, SaveRequest{RequestID: "cancel", EntryIDs: []string{share.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	store.mu.Lock()
	store.options.ActiveAccounts = func(context.Context) (map[string]bool, error) { return nil, errors.New("host unavailable") }
	store.mu.Unlock()
	if err := store.Maintain(context.Background()); err == nil {
		t.Fatal("unavailable snapshot acknowledged")
	}
	select {
	case <-reader.(*contentReader).Done():
	default:
		t.Error("preview remains open without account authorization")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("save remains running without account authorization")
	}
}

func (b *pollingBackend) Fence(ctx context.Context, _ string) error {
	b.totalFences.Add(1)
	if ctx.Value(pollingProbeKey{}) != nil {
		b.fences.Add(1)
	}
	return nil
}

func TestRevokedHistoryDoesNotRepeatMaintenanceIPC(t *testing.T) {
	directory := t.TempDir()
	backend := &pollingBackend{}
	var activeCalls atomic.Int64
	store, err := Open(filepath.Join(directory, "catalog.db"), filepath.Join(directory, "content"), Options{
		Backend: backend,
		Active: func(ctx context.Context, _ string) (bool, error) {
			if ctx.Value(pollingProbeKey{}) != nil {
				activeCalls.Add(1)
			}
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for index := range 24 {
		share, err := store.Create(context.Background(), sender, CreateRequest{
			RequestID: fmt.Sprintf("history-%d", index), SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		awaitShare(t, store, share.ID, "published")
		if err := store.Revoke(context.Background(), sender, share.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Only this invocation contributes to the counters, making the probe
	// independent of the background ticker's timing.
	probe := context.WithValue(context.Background(), pollingProbeKey{}, true)
	if err := store.Maintain(probe); err != nil {
		t.Fatal(err)
	}
	if got := activeCalls.Load(); got > 1 {
		t.Errorf("one maintenance cycle made %d active-account calls for cleaned history, want at most one", got)
	}
	if got := backend.fences.Load(); got != 0 {
		t.Errorf("one maintenance cycle repeated %d acknowledged fences, want zero", got)
	}
	activeCalls.Store(0)
	listed, err := store.List(probe, sender, "sent")
	if err != nil || len(listed) != 24 {
		t.Fatalf("history = %d records, %v; all history must remain readable", len(listed), err)
	}
	if got := activeCalls.Load(); got > 1 {
		t.Errorf("one history listing made %d active-account calls, want at most one", got)
	}
}

func TestMaintenanceUsesOneAccountSnapshotAndRefencesAfterReopen(t *testing.T) {
	directory := t.TempDir()
	backend := &pollingBackend{}
	var batchCalls atomic.Int64
	options := Options{
		Backend: backend,
		Active:  func(context.Context, string) (bool, error) { return true, nil },
		ActiveAccounts: func(ctx context.Context) (map[string]bool, error) {
			if ctx.Value(pollingProbeKey{}) != nil {
				batchCalls.Add(1)
			}
			return map[string]bool{sender.ID: true, bob.ID: true}, nil
		},
	}
	db, content := filepath.Join(directory, "catalog.db"), filepath.Join(directory, "content")
	store, err := Open(db, content, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for index := range 12 {
		share, err := store.Create(context.Background(), sender, CreateRequest{
			RequestID: fmt.Sprintf("batch-%d", index), SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		awaitShare(t, store, share.ID, "published")
		if index < 6 {
			if err := store.Revoke(context.Background(), sender, share.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	probe := context.WithValue(context.Background(), pollingProbeKey{}, true)
	if err := store.Maintain(probe); err != nil {
		t.Fatal(err)
	}
	if got := batchCalls.Load(); got != 1 {
		t.Fatalf("account snapshots = %d, want 1", got)
	}
	if got := backend.fences.Load(); got != 0 {
		t.Fatalf("acknowledged fences repeated %d times", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(db, content, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Maintain(probe); err != nil {
		t.Fatal(err)
	}
	if got := backend.totalFences.Load(); got != 12 {
		t.Fatalf("two generations fenced %d records, want 12", got)
	}
	if err := store.Maintain(probe); err != nil {
		t.Fatal(err)
	}
	if got := backend.totalFences.Load(); got != 12 {
		t.Fatalf("new generation repeated fences: %d", got)
	}
}
