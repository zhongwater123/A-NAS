package shares

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A recipient cannot revoke what was sent to them: to them the share is not
// something they can revoke, and it stays published for everyone.
func TestOnlyTheSenderRevokesAShare(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	if err := s.Revoke(ctx, bob, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recipient Revoke() error = %v, want ErrNotFound", err)
	}
	if got, err := s.Get(ctx, carol, v.ID, false); err != nil || got.Status != "published" {
		t.Fatalf("after a recipient's revoke the share is %q, %v; want published", got.Status, err)
	}
	if err := s.Revoke(ctx, sender, v.ID); err != nil {
		t.Fatalf("sender Revoke() error = %v", err)
	}
	if got, err := s.Get(ctx, sender, v.ID, false); err != nil || got.Status != "revoked" {
		t.Fatalf("after the sender's revoke the share is %q, %v; want revoked", got.Status, err)
	}
}

// Recipients see a share only once it is published: while it is still being
// prepared, and when preparing it failed, it is the sender's alone.
func TestRecipientsDoNotSeeASharePreparingOrFailed(t *testing.T) {
	release := make(chan struct{})
	s, _ := setup(t, &testBackend{capture: func(context.Context, func(Entry, io.Reader) error) error {
		<-release
		return errors.New("source changed")
	}})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish) // before the store closes, which waits for preparing
	ctx := context.Background()
	v, err := s.Create(ctx, sender, CreateRequest{RequestID: "hidden", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID}})
	if err != nil {
		t.Fatal(err)
	}
	hidden := func(status string) {
		t.Helper()
		if got, err := s.Get(ctx, sender, v.ID, false); err != nil || got.Status != status {
			t.Fatalf("sender sees the share %q, %v; want %s", got.Status, err, status)
		}
		if got, err := s.Get(ctx, bob, v.ID, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("recipient Get() of a %s share = %q, %v; want ErrNotFound", status, got.Status, err)
		}
		if received, err := s.List(ctx, bob, "received"); err != nil || len(received) != 0 {
			t.Fatalf("recipient's received list with a %s share = %+v, %v; want empty", status, received, err)
		}
	}
	hidden("preparing")
	finish()
	awaitShare(t, s, v.ID, "failed")
	hidden("failed")
}

// One share takes at most 100 sources and 100 recipients.
func TestAShareTakesAtMostOneHundredSourcesAndRecipients(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	ctx := context.Background()
	ids := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s-%d", prefix, i)
		}
		return out
	}
	if _, err := s.Create(ctx, sender, CreateRequest{RequestID: "sources", SourceIDs: ids("source", 101), RecipientIDs: []string{bob.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create() with 101 sources error = %v, want ErrInvalid", err)
	}
	if _, err := s.Create(ctx, sender, CreateRequest{RequestID: "recipients", SourceIDs: []string{"source"}, RecipientIDs: ids("user", 101)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create() with 101 recipients error = %v, want ErrInvalid", err)
	}
	if sent, err := s.List(ctx, sender, "sent"); err != nil || len(sent) != 0 {
		t.Fatalf("refused shares were kept: %+v, %v", sent, err)
	}
	v, err := s.Create(ctx, sender, CreateRequest{RequestID: "limit", SourceIDs: ids("source", 100), RecipientIDs: ids("user", 100)})
	if err != nil {
		t.Fatalf("Create() with 100 sources and recipients error = %v", err)
	}
	awaitShare(t, s, v.ID, "published")
}

// A captured path must mean the same on every client and file system, so a
// backslash, NUL or colon in it fails the share instead of being stored.
func TestASharedPathRefusesBackslashNULAndColon(t *testing.T) {
	for name, want := range map[string]string{`back\slash.txt`: "failed", "nul\x00.txt": "failed", "drive:c.txt": "failed", "plain name.txt": "published"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			var visitErr error
			s, _ := setup(t, &testBackend{capture: func(_ context.Context, visit func(Entry, io.Reader) error) error {
				visitErr = visit(Entry{Path: name, Size: 1}, strings.NewReader("x"))
				return visitErr
			}})
			v, err := s.Create(context.Background(), sender, CreateRequest{RequestID: "path", SourceIDs: []string{"source"}, RecipientIDs: []string{bob.ID}})
			if err != nil {
				t.Fatal(err)
			}
			got := awaitShare(t, s, v.ID, want)
			if want == "published" {
				if visitErr != nil || len(got.Entries) != 1 || got.Entries[0].Path != name {
					t.Fatalf("entries = %+v, capture error = %v; want %q", got.Entries, visitErr, name)
				}
				return
			}
			if !errors.Is(visitErr, ErrInvalid) || len(got.Entries) != 0 {
				t.Fatalf("capturing %q: error = %v, entries = %+v; want ErrInvalid and none", name, visitErr, got.Entries)
			}
		})
	}
}

// The sent list holds what the user sent, the received list what others sent
// to them, and neither holds the other's shares.
func TestSentAndReceivedListsDoNotMix(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	for _, c := range []struct {
		user           User
		sent, received int
	}{{sender, 1, 0}, {bob, 0, 1}, {User{ID: "dave"}, 0, 0}} {
		for direction, want := range map[string]int{"sent": c.sent, "received": c.received} {
			list, err := s.List(context.Background(), c.user, direction)
			if err != nil || len(list) != want || (want == 1 && list[0].ID != v.ID) {
				t.Errorf("%s's %s list = %+v, %v; want %d share(s)", c.user.ID, direction, list, err, want)
			}
		}
	}
}

// One save takes at most MaxEntries items. A share holds no more than that,
// so only a request that repeats an item can ask for more.
func TestASaveTakesAtMostMaxEntriesItems(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	ctx := context.Background()
	limit := slices.Repeat([]string{v.Entries[0].ID}, MaxEntries)
	if _, err := s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "over", EntryIDs: append(slices.Clone(limit), v.Entries[0].ID), Target: Target{RootID: "home"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Save() of MaxEntries+1 items error = %v, want ErrInvalid", err)
	}
	job, err := s.Save(ctx, bob, v.ID, SaveRequest{RequestID: "limit", EntryIDs: limit, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatalf("Save() of MaxEntries items error = %v", err)
	}
	if done := awaitJob(t, s, job.ID, "completed"); len(done.EntryIDs) != 1 {
		t.Fatalf("saved entries = %v, want the one item once", done.EntryIDs)
	}
}
