package shares

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnpersistedMessageCannotAppearOrNotify(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	before, _ := s.Notifications(ctx, sender, 0, 100)
	s.db.Close()
	if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "unpersisted", "must remain invisible"); err == nil {
		t.Fatal("message write succeeded after storage failure")
	}
	after, _ := s.Notifications(ctx, sender, 0, 100)
	if len(after) != len(before) {
		t.Fatal("unpersisted message produced notification")
	}
	messages, _ := s.Messages(ctx, bob, v.ID, bob.ID, 0, 100)
	if len(messages) != 1 {
		t.Fatal("unpersisted message visible")
	}
}
func TestContentSeekerReauthorizesAfterExpiry(t *testing.T) {
	s, clock := setup(t, &testBackend{})
	v := publish(t, s)
	reader, _, err := s.OpenContent(context.Background(), bob, v.ID, v.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	seeker, ok := reader.(io.Seeker)
	if !ok {
		t.Fatal("content must support guarded range reads")
	}
	if n, err := seeker.Seek(1, io.SeekStart); err != nil || n != 1 {
		t.Fatalf("seek %d %v", n, err)
	}
	clock.Advance(7 * 24 * time.Hour)
	if _, err = seeker.Seek(0, io.SeekStart); !errors.Is(err, ErrInactive) {
		t.Fatalf("expired seek %v", err)
	}
}
func TestSessionExpiryStopsSaveButBrowserCancellationDoesNot(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser close", true: "session expired"}[expire], func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var invalid atomic.Bool
			b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
				close(started)
				<-release
				return commit(entries[0].ID, func() error { return nil })
			}}
			s, _ := setup(t, b)
			s.options.Authorize = func(ctx context.Context, u User) error {
				if invalid.Load() && u.ID == bob.ID {
					return ErrUnauthorized
				}
				return nil
			}
			v := publish(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			job, err := s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "save", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			cancel()
			invalid.Store(expire)
			close(release)
			want := "completed"
			if expire {
				want = "failed"
			}
			for range 500 {
				s.mu.Lock()
				status := s.records[v.ID].Jobs[job.ID].Job.Status
				s.mu.Unlock()
				if status == want {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
			t.Fatalf("save did not become %s", want)
		})
	}
}
func TestDisabledSenderRevokesAccessButKeepsRecipientHistory(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	s.mu.Lock()
	s.options.Active = func(ctx context.Context, id string) (bool, error) { return id != sender.ID, nil }
	s.mu.Unlock()
	ctx := context.Background()
	view, err := s.Get(ctx, bob, v.ID, false)
	if err != nil || view.Status != "revoked" {
		t.Fatalf("disabled sender %+v %v", view, err)
	}
	if _, _, err = s.OpenContent(ctx, bob, v.ID, v.Entries[0].ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("disabled content %v", err)
	}
	messages, err := s.Messages(ctx, bob, v.ID, bob.ID, 0, 100)
	if err != nil || len(messages) != 1 {
		t.Fatalf("disabled history %+v %v", messages, err)
	}
}
func TestRevokeWaitsForPreviouslyAcceptedCommit(t *testing.T) {
	committing := make(chan struct{})
	release := make(chan struct{})
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		return commit(entries[0].ID, func() error { close(committing); <-release; return nil })
	}}
	s, _ := setup(t, b)
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "save", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-committing
	revoked := make(chan error, 1)
	go func() { revoked <- s.Revoke(context.Background(), sender, v.ID) }()
	select {
	case err := <-revoked:
		t.Fatalf("revoke passed in-flight commit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	j, err := s.Job(context.Background(), bob, job.ID)
	if err != nil || len(j.CompletedIDs) != 1 {
		t.Fatalf("prior committed file lost %+v %v", j, err)
	}
}
func TestRestartRemovesOnlyOrphanOwnedDirectories(t *testing.T) {
	dir := t.TempDir()
	content := filepath.Join(dir, "content")
	s, err := Open(filepath.Join(dir, "db"), content, Options{Backend: &testBackend{}, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	v := publish(t, s)
	s.Close()
	orphan := newID()
	if err = os.Mkdir(filepath.Join(content, orphan), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(content, "unmanaged"), 0700); err != nil {
		t.Fatal(err)
	}
	options := Options{Backend: &testBackend{}, Active: func(context.Context, string) (bool, error) { return true, nil }}
	s, err = Open(filepath.Join(dir, "db"), content, options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = os.Stat(filepath.Join(content, orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan retained: %v", err)
	}
	if _, err = os.Stat(filepath.Join(content, "unmanaged")); err != nil {
		t.Fatalf("unmanaged path removed: %v", err)
	}
	r, _, err := s.OpenContent(context.Background(), bob, v.ID, v.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}
