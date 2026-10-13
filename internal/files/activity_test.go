package files_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestFileEventsCarryTheirSpaceAndMembersSeeOnlyTheirOwn(t *testing.T) {
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	private := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	// Events a millisecond apart would be ordered by their random IDs.
	now := time.Date(2026, time.October, 12, 8, 0, 0, 0, time.UTC)
	service := files.NewService(catalog, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true,
		Now: func() time.Time { now = now.Add(time.Second); return now },
	})
	uploaded, err := service.Upload(ctx, alice, private.ID, "", "体检报告.pdf", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, alice, private.ID, uploaded.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateDirectory(ctx, admin, findPrivateSpace(t, mustSpaces(t, accountService, admin)).ID, "", "admin notes"); err != nil {
		t.Fatal(err)
	}

	mine, err := service.Activity(ctx, alice, activity.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].Action != "file.trashed" || mine[1].Action != "file.uploaded" {
		t.Fatalf("alice's file activity = %+v", mine)
	}
	for _, event := range mine {
		if event.SpaceID != private.ID || event.ActorUserID != alice.ID {
			t.Fatalf("event %s space/actor = %q/%q, want %q/%q", event.Action, event.SpaceID, event.ActorUserID, private.ID, alice.ID)
		}
	}
	all, err := service.Activity(ctx, admin, activity.Query{Limit: 10})
	if err != nil || len(all) != 3 {
		t.Fatalf("administrator's file activity = %+v, %v", all, err)
	}
}

func TestOlderFileEventsAreGivenTheSpaceTheyHappenedIn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "files.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// The catalog as it was before events recorded their space.
	if _, err := db.Exec(`
CREATE TABLE entries (id TEXT PRIMARY KEY, space_id TEXT NOT NULL, parent_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
    kind TEXT NOT NULL, relative_path TEXT NOT NULL, inode INTEGER NOT NULL, size_bytes INTEGER NOT NULL, modified_at TEXT NOT NULL,
    trashed INTEGER NOT NULL DEFAULT 0, trash_id TEXT, UNIQUE(space_id, relative_path), UNIQUE(space_id, inode));
CREATE TABLE trash (id TEXT PRIMARY KEY, entry_id TEXT NOT NULL, space_id TEXT NOT NULL, original_parent_id TEXT NOT NULL DEFAULT '',
    original_name TEXT NOT NULL, trash_relative_path TEXT NOT NULL, deleted_by TEXT NOT NULL, deleted_at TEXT NOT NULL);
CREATE TABLE snapshots (id TEXT PRIMARY KEY, space_id TEXT NOT NULL, name TEXT NOT NULL, created_by TEXT NOT NULL, created_at TEXT NOT NULL,
    UNIQUE(space_id, name));
CREATE TABLE audit_events (id TEXT PRIMARY KEY, actor_user_id TEXT NOT NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL, occurred_at TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '');
INSERT INTO entries VALUES('entry:1', 'space:shared', '', 'a.txt', 'file', 'a.txt', 1, 1, '2026-10-01T00:00:00Z', 0, NULL);
INSERT INTO trash VALUES('trash:1', 'entry:2', 'space:alice', '', 'b.txt', '.trash/b.txt', 'alice', '2026-10-01T00:00:00Z');
INSERT INTO snapshots VALUES('snapshot:1', 'space:alice', 'nightly', 'alice', '2026-10-01T00:00:00Z');
INSERT INTO audit_events VALUES('audit:1', 'alice', 'file.uploaded', 'file', 'entry:1', '2026-10-01T00:00:01Z', 'a.txt');
INSERT INTO audit_events VALUES('audit:2', 'alice', 'file.trashed', 'file', 'entry:2', '2026-10-01T00:00:02Z', 'b.txt');
INSERT INTO audit_events VALUES('audit:3', 'alice', 'snapshot.created', 'file', 'snapshot:1', '2026-10-01T00:00:03Z', 'nightly');
INSERT INTO audit_events VALUES('audit:4', 'alice', 'file.purged', 'file', 'entry:gone', '2026-10-01T00:00:04Z', 'c.txt');`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	catalog, err := files.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	events, err := activity.Table{DB: catalogDB(t, path), SpaceColumn: true}.List(ctx, activity.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"audit:1": "space:shared", "audit:2": "space:alice", "audit:3": "space:alice", "audit:4": ""}
	for _, event := range events {
		if got := event.SpaceID; got != want[event.ID] {
			t.Fatalf("space of %s = %q, want %q", event.ID, got, want[event.ID])
		}
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
}

func catalogDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustSpaces(t *testing.T, service *accounts.Service, user accounts.User) []accounts.Space {
	t.Helper()
	spaces, err := service.ListSpaces(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return spaces
}
