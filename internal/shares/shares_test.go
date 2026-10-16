package shares

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBackend struct {
	capture    func(context.Context, func(Entry, io.Reader) error) error
	save       func(context.Context, []Entry, func(string) (io.ReadCloser, error), func(string, func() error) error) error
	captureErr error
}

func (b *testBackend) Capture(ctx context.Context, u User, ids []string, visit func(Entry, io.Reader) error) error {
	if b.capture != nil {
		return b.capture(ctx, visit)
	}
	if b.captureErr != nil {
		return b.captureErr
	}
	return visit(Entry{Path: "hello.txt", Size: 5}, strings.NewReader("hello"))
}
func (b *testBackend) Save(ctx context.Context, u User, target Target, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error, progress func(int64)) error {
	if b.save != nil {
		return b.save(ctx, entries, open, commit)
	}
	for _, e := range entries {
		r, err := open(e.ID)
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, r)
		r.Close()
		if err != nil {
			return err
		}
		if err = commit(e.ID, func() error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

type testClock struct {
	sync.Mutex
	t time.Time
}

func (c *testClock) Now() time.Time          { c.Lock(); defer c.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) { c.Lock(); defer c.Unlock(); c.t = c.t.Add(d) }
func setup(t *testing.T, b *testBackend) (*Store, *testClock) {
	t.Helper()
	dir := t.TempDir()
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s, err := Open(filepath.Join(dir, "shares.db"), filepath.Join(dir, "content"), Options{Backend: b, Now: clock.Now, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, clock
}

var sender = User{ID: "alice", Username: "Alice"}
var bob = User{ID: "bob", Username: "Bob"}
var carol = User{ID: "carol", Username: "Carol"}

func publish(t *testing.T, s *Store) Share {
	t.Helper()
	v, err := s.Create(context.Background(), sender, CreateRequest{RequestID: "share-1", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID, carol.ID}, Message: "你好 👩🏽‍💻\nhello"})
	if err != nil {
		t.Fatal(err)
	}
	return awaitShare(t, s, v.ID, "published")
}
func awaitShare(t *testing.T, s *Store, id, status string) Share {
	t.Helper()
	for range 500 {
		v, err := s.Get(context.Background(), sender, id, false)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == status {
			return v
		}
		time.Sleep(time.Millisecond * 2)
	}
	t.Fatalf("share never became %s", status)
	return Share{}
}
func awaitJob(t *testing.T, s *Store, id, status string) Job {
	t.Helper()
	for range 500 {
		v, err := s.Job(context.Background(), bob, id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == status {
			return v
		}
		time.Sleep(time.Millisecond * 2)
	}
	t.Fatalf("job never became %s", status)
	return Job{}
}
func TestIsolationMessagesIdempotencyAndImmutableCopy(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	if _, err := s.Get(ctx, User{ID: "admin"}, v.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin access: %v", err)
	}
	own, err := s.Get(ctx, bob, v.ID, true)
	if err != nil || len(own.Recipients) != 1 || own.Recipients[0].UserID != bob.ID {
		t.Fatalf("recipient isolation: %+v %v", own, err)
	}
	if _, err = s.Messages(ctx, bob, v.ID, carol.ID, 0, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other thread: %v", err)
	}
	m, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "reply-1", "👍🏽 你好\n👨‍👩‍👧‍👦")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "reply-1", m.Text)
	if err != nil || m.ID != same.ID {
		t.Fatalf("dedupe %v", err)
	}
	if _, err = s.SendMessage(ctx, bob, v.ID, bob.ID, "reply-1", "changed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	msgs, _ := s.Messages(ctx, carol, v.ID, carol.ID, 0, 100)
	if len(msgs) != 1 {
		t.Fatalf("leaked reply %+v", msgs)
	}
	r, _, err := s.OpenContent(ctx, bob, v.ID, v.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "hello" {
		t.Fatalf("copy %q", data)
	}
	events, _ := s.Notifications(ctx, bob, 0, 100)
	if len(events) != 1 {
		t.Fatalf("events: %+v", events)
	}
}
func TestExpiryDrainsExistingSaveAndRevokeBlocksCommit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		close(started)
		<-release
		r, err := open(entries[0].ID)
		if err != nil {
			return err
		}
		r.Close()
		return commit(entries[0].ID, func() error { return nil })
	}}
	s, clock := setup(t, b)
	v := publish(t, s)
	ctx := context.Background()
	job, err := s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "save1", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home", ParentID: "root"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	clock.Advance(7 * 24 * time.Hour)
	if _, err = s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "save2", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}}); !errors.Is(err, ErrInactive) {
		t.Fatalf("new expired save: %v", err)
	}
	if _, _, err = s.OpenContent(ctx, bob, v.ID, v.Entries[0].ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("expired preview %v", err)
	}
	if err = s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitJob(t, s, job.ID, "completed")
	if err = s.Revoke(ctx, sender, v.ID); err != nil {
		t.Fatalf("revoke expired: %v", err)
	}
}
func TestRevokeCancelsSaveAndStream(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	committed := false
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		close(started)
		<-release
		return commit(entries[0].ID, func() error { committed = true; return nil })
	}}
	s, _ := setup(t, b)
	v := publish(t, s)
	ctx := context.Background()
	r, _, err := s.OpenContent(ctx, bob, v.ID, v.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	job, err := s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "save1", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.Revoke(ctx, sender, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(make([]byte, 5)); err == nil {
		t.Fatal("revoked stream readable")
	}
	close(release)
	awaitJob(t, s, job.ID, "cancelled")
	if committed {
		t.Fatal("committed after revoke")
	}
}
func TestPreparationFailureNeverNotifies(t *testing.T) {
	s, _ := setup(t, &testBackend{captureErr: errors.New("source changed")})
	v, err := s.Create(context.Background(), sender, CreateRequest{RequestID: "bad", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID}})
	if err != nil {
		t.Fatal(err)
	}
	awaitShare(t, s, v.ID, "failed")
	events, _ := s.Notifications(context.Background(), bob, 0, 100)
	if len(events) != 0 {
		t.Fatal("failed share notified")
	}
}
