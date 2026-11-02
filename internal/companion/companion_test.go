package companion

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

func testStore(t *testing.T) (*Store, *time.Time, string) {
	t.Helper()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "companion.db")
	s, err := Open(path, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, &now, path
}
func mustClaim(t *testing.T, s *Store, owner, client string, revision int64) State {
	t.Helper()
	v, err := s.Claim(context.Background(), owner, Claim{client, "web", revision})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestHandoffWaitsForOldClientToStopAndFencesStaleMessages(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	a, b := uuid.NewString(), uuid.NewString()
	one := mustClaim(t, s, "alice", a, 0)
	old := Holder{a, one.Active.Epoch}
	waiting := mustClaim(t, s, "alice", b, one.Revision)
	if waiting.Active.ClientID != a || waiting.Pending.ClientID != b {
		t.Fatalf("handoff did not wait: %#v", waiting)
	}
	heartbeat, err := s.Heartbeat(ctx, "alice", old)
	if err != nil || heartbeat.Active.RemainingMS != waiting.Active.RemainingMS {
		t.Fatalf("pending holder renewed its lease: %#v %v", heartbeat, err)
	}
	if _, err = s.Act(ctx, "alice", Action{ClientID: a, Epoch: old.Epoch, EventID: uuid.NewString(), Type: "ball"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old holder played during handoff: %v", err)
	}
	two, err := s.Release(ctx, "alice", old)
	if err != nil || two.Active.ClientID != b || two.Pending != nil {
		t.Fatalf("handoff: %#v %v", two, err)
	}
	for _, action := range []func() error{
		func() error { _, e := s.Heartbeat(ctx, "alice", old); return e },
		func() error { _, e := s.Release(ctx, "alice", old); return e },
		func() error {
			_, e := s.Act(ctx, "alice", Action{ClientID: a, Epoch: old.Epoch, EventID: uuid.NewString(), Type: "quiet"})
			return e
		},
	} {
		if err := action(); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale epoch was accepted: %v", err)
		}
	}
}
func TestDisconnectedHolderExpiresWithoutBeingSilentlyReclaimed(t *testing.T) {
	s, now, _ := testStore(t)
	ctx := context.Background()
	a, b := uuid.NewString(), uuid.NewString()
	first := mustClaim(t, s, "alice", a, 0)
	waiting := mustClaim(t, s, "alice", b, first.Revision)
	*now = now.Add(LeaseDuration + time.Millisecond)
	read, err := s.Read(ctx, "alice")
	if err != nil || read.Active != nil || read.Pending.ClientID != b {
		t.Fatalf("expired state: %#v %v", read, err)
	}
	claimed := mustClaim(t, s, "alice", b, waiting.Revision)
	if claimed.Active.ClientID != b {
		t.Fatal("pending client could not take over after expiry")
	}
	if _, err = s.Heartbeat(ctx, "alice", Holder{a, first.Active.Epoch}); !errors.Is(err, ErrConflict) {
		t.Fatal("late heartbeat reclaimed a lease")
	}
}
func TestMembersAndRetriesAreIsolatedAndStateSurvivesRestart(t *testing.T) {
	s, now, path := testStore(t)
	ctx := context.Background()
	client := uuid.NewString()
	first := mustClaim(t, s, "alice", client, 0)
	event := Action{ClientID: client, Epoch: first.Active.Epoch, EventID: uuid.NewString(), Type: "ball"}
	played, err := s.Act(ctx, "alice", event)
	if err != nil || played.PlayCount != 1 {
		t.Fatalf("play: %#v %v", played, err)
	}
	again, err := s.Act(ctx, "alice", event)
	if err != nil || again.PlayCount != 1 || again.Revision != played.Revision {
		t.Fatal("retry duplicated the activity")
	}
	event.Type = "quiet"
	if _, err = s.Act(ctx, "alice", event); !errors.Is(err, ErrConflict) {
		t.Fatal("same event id accepted different contents")
	}
	bob, err := s.Read(ctx, "bob")
	if err != nil || bob.PlayCount != 0 || bob.Active != nil || bob.Pending != nil {
		t.Fatalf("member state leaked: %#v %v", bob, err)
	}
	if _, err = s.Act(ctx, "bob", Action{ClientID: client, Epoch: first.Active.Epoch, EventID: uuid.NewString(), Type: "ball"}); !errors.Is(err, ErrConflict) {
		t.Fatal("another owner reused a lease")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Read(ctx, "alice")
	if err != nil || got.PlayCount != 1 || got.Active.ClientID != client {
		t.Fatalf("restart: %#v %v", got, err)
	}
}
func TestConcurrentClaimsAndActivityDoNotLoseUpdates(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range 12 {
		wg.Go(func() {
			_, err := s.Claim(ctx, "alice", Claim{uuid.NewString(), "web", 0})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("same revision claimed %d times", successes)
	}
	state, err := s.Read(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		wg.Go(func() {
			_, err := s.Act(ctx, "alice", Action{ClientID: state.Active.ClientID, Epoch: state.Active.Epoch, EventID: uuid.NewString(), Type: "ball"})
			if err != nil {
				t.Errorf("play: %v", err)
			}
		})
	}
	wg.Wait()
	state, err = s.Read(ctx, "alice")
	if err != nil || state.PlayCount != 20 {
		t.Fatalf("lost activity: %#v %v", state, err)
	}
}
func TestInvalidCommandsCannotChangeState(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Claim(ctx, "alice", Claim{"not-a-uuid", "web", 0}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "alice", Claim{uuid.NewString(), "touch", 0}); !errors.Is(err, ErrInvalid) {
		t.Fatal("deferred touch client accepted")
	}
	if _, err := s.Read(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty owner accepted")
	}
	state, _ := s.Read(ctx, "alice")
	if state.Revision != 0 {
		t.Fatal("invalid request changed state")
	}
}

func TestConversationReferenceIsFencedAndPersistsWithoutCopyingHistory(t *testing.T) {
	s, now, path := testStore(t)
	ctx := context.Background()
	a, b := uuid.NewString(), uuid.NewString()
	first := mustClaim(t, s, "alice", a, 0)
	event := Action{ClientID: a, Epoch: first.Active.Epoch, EventID: uuid.NewString(), Type: "conversation", ConversationID: "20261102_090000_abcdef"}
	bound, err := s.Act(ctx, "alice", event)
	if err != nil || bound.ConversationID != event.ConversationID {
		t.Fatalf("bind: %#v %v", bound, err)
	}
	again, err := s.Act(ctx, "alice", event)
	if err != nil || again.Revision != bound.Revision {
		t.Fatal("retried binding changed state")
	}
	bob, _ := s.Read(ctx, "bob")
	if bob.ConversationID != "" {
		t.Fatal("conversation leaked between members")
	}
	waiting := mustClaim(t, s, "alice", b, bound.Revision)
	event.EventID = uuid.NewString()
	event.ConversationID = ""
	if _, err := s.Act(ctx, "alice", event); !errors.Is(err, ErrConflict) {
		t.Fatalf("old holder cleared during transfer: %v", err)
	}
	transferred, err := s.Release(ctx, "alice", Holder{a, first.Active.Epoch})
	if err != nil || transferred.ConversationID != bound.ConversationID || waiting.Pending == nil {
		t.Fatal("handoff lost reference")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, Options{Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Read(ctx, "alice")
	if err != nil || got.ConversationID != bound.ConversationID {
		t.Fatal("reference lost after restart")
	}
	for _, id := range []string{"../alice", "20261102_090000_ABCDEF", "cron_123", "https://evil.example"} {
		_, err = reopened.Act(ctx, "alice", Action{ClientID: b, Epoch: got.Active.Epoch, EventID: uuid.NewString(), Type: "conversation", ConversationID: id})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid reference accepted %q: %v", id, err)
		}
	}
	cleared, err := reopened.Act(ctx, "alice", Action{ClientID: b, Epoch: got.Active.Epoch, EventID: uuid.NewString(), Type: "conversation"})
	if err != nil || cleared.ConversationID != "" {
		t.Fatal("could not start fresh")
	}
}

func TestOriginalDatabaseMigratesWithoutLosingActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE companions (owner TEXT PRIMARY KEY, revision INTEGER NOT NULL, character TEXT NOT NULL, quiet INTEGER NOT NULL,
play_count INTEGER NOT NULL, client TEXT NOT NULL, kind TEXT NOT NULL, epoch INTEGER NOT NULL,
lease_until INTEGER NOT NULL, pending_client TEXT NOT NULL, pending_kind TEXT NOT NULL);
INSERT INTO companions VALUES ('alice',5,'chestnut',1,42,'','',3,0,'','');`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	state, err := s.Read(context.Background(), "alice")
	if err != nil || state.Revision != 5 || state.PlayCount != 42 || !state.Quiet || state.Character != "chestnut" || state.ConversationID != "" {
		t.Fatalf("migration lost activity: %#v %v", state, err)
	}
}
