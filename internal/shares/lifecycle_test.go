package shares

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fencedBackend struct {
	testBackend
	fenceErr error
	calls    int
}

func (b *fencedBackend) Fence(context.Context, string) error { b.calls++; return b.fenceErr }
func TestRevokeRequiresRemotePublicationFence(t *testing.T) {
	dir := t.TempDir()
	backend := &fencedBackend{fenceErr: errors.New("bridge unavailable")}
	s, err := Open(filepath.Join(dir, "db"), filepath.Join(dir, "content"), Options{Backend: backend, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v := publish(t, s)
	if err = s.Revoke(context.Background(), sender, v.ID); err == nil {
		t.Fatal("revoke acknowledged without remote publication fence")
	}
	backend.fenceErr = nil
	if err = s.Revoke(context.Background(), sender, v.ID); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 {
		t.Fatalf("fence calls %d", backend.calls)
	}
}
func TestSelectingNestedFileIncludesAncestorsOnly(t *testing.T) {
	r := &record{Share: Share{Entries: []Entry{{ID: "dir", Path: "folder", IsDir: true}, {ID: "sub", Path: "folder/sub", IsDir: true}, {ID: "a", Path: "folder/sub/a", Size: 1}, {ID: "b", Path: "folder/sub/b", Size: 1}}}}
	entries, err := selectedEntries(r, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "dir" || entries[1].ID != "sub" || entries[2].ID != "a" {
		t.Fatalf("selected ancestry %+v", entries)
	}
}
func TestRequestKeysAreActorOperationScopedAcrossShares(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	first := publish(t, s)
	second, err := s.Create(context.Background(), sender, CreateRequest{RequestID: "second", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID}})
	if err != nil {
		t.Fatal(err)
	}
	awaitShare(t, s, second.ID, "published")
	_, err = s.SendMessage(context.Background(), bob, first.ID, bob.ID, "reused", "reply")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendMessage(context.Background(), bob, second.ID, bob.ID, "reused", "reply"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-share key: %v", err)
	}
}
func TestPersistenceHistoryAndInterruptedJobRecovery(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	content := filepath.Join(dir, "content")
	options := Options{Backend: &testBackend{}, Active: func(context.Context, string) (bool, error) { return true, nil }}
	s, err := Open(db, content, options)
	if err != nil {
		t.Fatal(err)
	}
	v := publish(t, s)
	msg, err := s.SendMessage(context.Background(), bob, v.ID, bob.ID, "persist", "再见 🫶🏽")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkRead(context.Background(), sender, v.ID, bob.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.records[v.ID].Jobs["interrupted"] = &storedJob{Job: Job{ID: "interrupted", ShareID: v.ID, UserID: bob.ID, Status: "running"}}
	if err = s.persist(s.records[v.ID]); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	s.Close()
	s, err = Open(db, content, options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	messages, err := s.Messages(context.Background(), sender, v.ID, bob.ID, 0, 100)
	if err != nil || len(messages) != 2 || messages[1].Text != msg.Text {
		t.Fatalf("persisted messages %+v %v", messages, err)
	}
	job, err := s.Job(context.Background(), bob, "interrupted")
	if err != nil || job.Status != "interrupted" {
		t.Fatalf("restart job %+v %v", job, err)
	}
	view, err := s.Get(context.Background(), sender, v.ID, false)
	if err != nil || view.Recipients[0].SenderReadThrough != msg.ID {
		t.Fatalf("read cursor %+v %v", view, err)
	}
}
func TestExpiredCleanupWaitsForSaveAndRetriesOfflineVolume(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		close(started)
		<-release
		return commit(entries[0].ID, func() error { return nil })
	}}
	s, clock := setup(t, b)
	var offline atomic.Bool
	s.options.CheckVolume = func(context.Context) error {
		if offline.Load() {
			return errors.New("offline")
		}
		return nil
	}
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "save", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	clock.Advance(7 * 24 * time.Hour)
	if err = s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.dir, v.ID, v.Entries[0].ID)); err != nil {
		t.Fatalf("active job content removed %v", err)
	}
	close(release)
	awaitJob(t, s, job.ID, "completed")
	if _, err = os.Stat(filepath.Join(s.dir, v.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired content retained %v", err)
	}
	s.mu.Lock()
	s.records[v.ID].ContentRemoved = false
	s.mu.Unlock()
	offline.Store(true)
	if err = s.Maintain(context.Background()); err == nil {
		t.Fatal("offline cleanup acknowledged")
	}
	s.mu.Lock()
	removed := s.records[v.ID].ContentRemoved
	s.mu.Unlock()
	if removed {
		t.Fatal("offline cleanup lost retry state")
	}
	offline.Store(false)
	if err = s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestCaptureRejectsTraversalOversizeAndMissingStableDescriptor(t *testing.T) {
	for _, test := range []struct {
		name, path string
		size       int64
		clone      bool
	}{{"traversal", "../secret", 5, false}, {"mismatch", "file", 4, false}, {"clone required", "file", 5, true}} {
		t.Run(test.name, func(t *testing.T) {
			backend := &testBackend{capture: func(ctx context.Context, visit func(Entry, io.Reader) error) error {
				return visit(Entry{Path: test.path, Size: test.size}, strings.NewReader("hello"))
			}}
			s, _ := setup(t, backend)
			s.options.RequireClone = test.clone
			v, err := s.Create(context.Background(), sender, CreateRequest{RequestID: "bad", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID}})
			if err != nil {
				t.Fatal(err)
			}
			awaitShare(t, s, v.ID, "failed")
			events, _ := s.Notifications(context.Background(), bob, 0, 100)
			if len(events) != 0 {
				t.Fatal("invalid content published")
			}
		})
	}
}
func TestMessagesValidateUTF8SizeAndReadCursor(t *testing.T) {
	s, clock := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	for _, text := range []string{"", string([]byte{0xff}), strings.Repeat("x", MaxMessageBytes+1)} {
		if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "invalid", text); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid text: %v", err)
		}
	}
	if err := s.MarkRead(ctx, bob, v.ID, bob.ID, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future read %v", err)
	}
	if err := s.MarkRead(ctx, bob, v.ID, bob.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(ctx, bob, v.ID, bob.ID, 0); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Get(ctx, bob, v.ID, false)
	if view.Recipients[0].ReadThrough != 1 {
		t.Fatal("cursor moved backwards")
	}
	clock.Advance(7 * 24 * time.Hour)
	if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "expired", "late"); !errors.Is(err, ErrInactive) {
		t.Fatalf("expired reply %v", err)
	}
	messages, err := s.Messages(ctx, bob, v.ID, bob.ID, 0, 1)
	if err != nil || len(messages) != 1 {
		t.Fatalf("history lost %v", err)
	}
}

func TestAuthorsReadTheirOwnMessagesButNeverSkipReplies(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	cursors := func(u User) Recipient {
		t.Helper()
		view, err := s.Get(ctx, u, v.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range view.Recipients {
			if item.UserID == bob.ID {
				return item
			}
		}
		t.Fatal("bob's thread missing")
		return Recipient{}
	}
	if got := cursors(sender); got.LastMessage != 1 || got.SenderReadThrough != 1 || got.ReadThrough != 0 {
		t.Fatalf("initial note %+v", got)
	}
	// Bob has not read the note yet, so his own reply must not mark it read.
	if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "reply-1", "收到"); err != nil {
		t.Fatal(err)
	}
	if got := cursors(bob); got.ReadThrough != 0 {
		t.Fatalf("reply skipped the unread note %+v", got)
	}
	if err := s.MarkRead(ctx, bob, v.ID, bob.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "reply-2", "谢谢"); err != nil {
		t.Fatal(err)
	}
	if got := cursors(bob); got.ReadThrough != 3 {
		t.Fatalf("own reply left unread %+v", got)
	}
	// The sender has not read Bob's replies; answering must leave them unread.
	if _, err := s.SendMessage(ctx, sender, v.ID, bob.ID, "answer", "好的"); err != nil {
		t.Fatal(err)
	}
	if got := cursors(sender); got.SenderReadThrough != 1 || got.LastMessage != 4 {
		t.Fatalf("sender cursor %+v", got)
	}
}
