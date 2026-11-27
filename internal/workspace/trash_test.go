package workspace_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/workspace"
)

func TestADeletedPageWaitsInTheTrashWithAllItsVersions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	page, err := f.store.CreatePage(ctx, f.alice, "a.html", []byte("<title>甲</title><p>v1"), workspace.Web, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveVersion(ctx, f.bob, page.ID, 1, []byte("<title>甲</title><p>v2"), workspace.Web, ""); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Hour)
	if err := f.store.DeletePage(ctx, f.alice, page.ID); err != nil {
		t.Fatal(err)
	}

	// Gone from everything that works on pages.
	if pages, err := f.store.ListPages(ctx); err != nil || len(pages) != 0 {
		t.Fatalf("the list after deleting = %+v, %v", pages, err)
	}
	if _, err := f.store.Page(ctx, page.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("Page() of a trashed page: %v", err)
	}
	if _, _, err := f.store.Content(ctx, page.ID, 1); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("Content() of a trashed page: %v", err)
	}
	if _, err := f.store.RenamePage(ctx, f.alice, page.ID, "乙"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("renaming a trashed page: %v", err)
	}
	if _, err := f.store.SaveVersion(ctx, f.alice, page.ID, 2, []byte("<p>v3"), workspace.Web, ""); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("saving over a trashed page: %v", err)
	}
	if err := f.store.DeletePage(ctx, f.alice, page.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}

	trash, err := f.store.Trash(ctx)
	deletedAt := *f.now
	want := workspace.TrashedPage{
		ID: page.ID, Title: "甲", CreatedBy: workspace.Person{ID: "user:alice", Name: "Alice"},
		DeletedBy: workspace.Person{ID: "user:alice", Name: "Alice"}, DeletedAt: deletedAt,
		PurgeAt: deletedAt.AddDate(0, 0, workspace.TrashDays), Versions: 2,
		SizeBytes: int64(len("<title>甲</title><p>v1") + len("<title>甲</title><p>v2")),
	}
	if err != nil || len(trash) != 1 || trash[0] != want {
		t.Fatalf("Trash() = %+v, %v; want %+v", trash, err, want)
	}

	// Only the uploader and administrators bring it back, as they deleted it.
	if _, err := f.store.RestorePage(ctx, f.bob, page.ID); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("another member restored the page: %v", err)
	}
	restored, err := f.store.RestorePage(ctx, f.admin, page.ID)
	if err != nil || restored.ID != page.ID || restored.Latest.Version != 2 {
		t.Fatalf("RestorePage() = %+v, %v", restored, err)
	}
	if _, err := f.store.RestorePage(ctx, f.admin, page.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("restoring a page in the list: %v", err)
	}
	back, err := f.store.Page(ctx, page.ID)
	if err != nil || len(back.Versions) != 2 {
		t.Fatalf("the restored page = %+v, %v", back, err)
	}
	if source, _, err := f.store.Content(ctx, page.ID, 1); err != nil || string(source) != "<title>甲</title><p>v1" {
		t.Fatalf("v1 after restoring = %q, %v", source, err)
	}
	if trash, _ := f.store.Trash(ctx); len(trash) != 0 {
		t.Fatalf("the trash after restoring = %+v", trash)
	}
	if err := f.store.PurgePage(ctx, f.admin, page.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("purging a page in the list: %v", err)
	}
	actions := func(actor workspace.Actor) []string {
		events, err := f.store.Activity(ctx, actor, activity.Query{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, event := range events {
			names = append(names, event.Action)
		}
		return names
	}
	if got := actions(f.alice); !slices.Contains(got, "workspace.page_deleted") {
		t.Fatalf("alice's activity = %v", got)
	}
	if got := actions(f.admin); !slices.Equal(got, []string{"workspace.page_restored"}) {
		t.Fatalf("the administrator's activity = %v", got)
	}
}

func TestATrashedPageGoesForGoodWhenPurgedOrAfterThirtyDays(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	create := func(title string) string {
		page, err := f.store.CreatePage(ctx, f.alice, title+".html", []byte("<title>"+title+"</title>"), workspace.Web, "")
		if err != nil {
			t.Fatal(err)
		}
		return page.ID
	}
	purged, expiring, recent := create("甲"), create("乙"), create("丙")
	for _, id := range []string{purged, expiring} {
		if err := f.store.DeletePage(ctx, f.alice, id); err != nil {
			t.Fatal(err)
		}
	}
	*f.now = f.now.AddDate(0, 0, 10)
	if err := f.store.DeletePage(ctx, f.alice, recent); err != nil {
		t.Fatal(err)
	}

	if err := f.store.PurgePage(ctx, f.bob, purged); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("another member purged the page: %v", err)
	}
	if err := f.store.PurgePage(ctx, f.alice, purged); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RestorePage(ctx, f.alice, purged); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("restoring a purged page: %v", err)
	}

	// Thirty days after it was deleted, upkeep removes it; one deleted ten
	// days later stays.
	*f.now = f.now.AddDate(0, 0, workspace.TrashDays-10)
	if err := f.store.Upkeep(ctx); err != nil {
		t.Fatal(err)
	}
	trash, err := f.store.Trash(ctx)
	if err != nil || len(trash) != 1 || trash[0].ID != recent {
		t.Fatalf("the trash after thirty days = %+v, %v", trash, err)
	}
	if _, err := f.store.RestorePage(ctx, f.alice, expiring); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("restoring an expired page: %v", err)
	}
	// Whoever deleted it reads that it went.
	events, err := f.store.Activity(ctx, f.alice, activity.Query{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var expired, purgedEvents []string
	for _, event := range events {
		switch event.Action {
		case "workspace.page_expired":
			expired = append(expired, event.Detail)
		case "workspace.page_purged":
			purgedEvents = append(purgedEvents, event.Detail)
		}
	}
	if !slices.Equal(expired, []string{"乙"}) || !slices.Equal(purgedEvents, []string{"甲"}) {
		t.Fatalf("alice's activity: expired %v, purged %v", expired, purgedEvents)
	}
}

func TestEachDayKeepsACompressedCopyOfThePages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	full := false
	store, err := workspace.Open(dir, workspace.Options{
		Now:    func() time.Time { return now },
		Admits: func(string, int64) (bool, error) { return !full, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice := workspace.Actor{AccountID: "user:alice", Name: "Alice"}
	source := readSample(t)
	page, err := store.CreatePage(ctx, alice, "prd.html", source, workspace.Web, "")
	if err != nil {
		t.Fatal(err)
	}
	// A page deleted before the copy is in it too.
	gone, err := store.CreatePage(ctx, alice, "gone.html", []byte("<title>旧</title>"), workspace.Web, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePage(ctx, alice, gone.ID); err != nil {
		t.Fatal(err)
	}

	// What a backup cut short by a power cut left goes first, even on a day
	// with no room for a copy.
	backups := filepath.Join(dir, "backups")
	plant := func(names ...string) {
		t.Helper()
		if err := os.MkdirAll(backups, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(backups, name), []byte("left over"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	plant("copy.db", "copy.db.partial", "workspace-2026-11-26.db.gz.partial")
	full = true
	if err := store.BackupIfDue(ctx); !errors.Is(err, workspace.ErrVolumeFull) {
		t.Fatalf("a backup at the reserve: %v", err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Fatalf("a refused backup left %v", entries)
	}
	full = false
	if err := store.BackupIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	today := filepath.Join(backups, "workspace-2026-11-27.db.gz")
	info, err := os.Stat(today)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("today's backup: %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 1 {
		t.Fatalf("the backups folder holds %v; want only the copy", entries)
	}

	// The copy opens as a database with both pages and the page's source.
	copied := unpack(t, today)
	var title, deletedAt string
	var content []byte
	if err := copied.QueryRowContext(ctx, `SELECT p.title, p.deleted_at, v.content FROM pages p JOIN versions v ON v.page_id = p.id
WHERE p.id = ?`, page.ID).Scan(&title, &deletedAt, &content); err != nil || title != page.Title || deletedAt != "" || !bytes.Equal(content, source) {
		t.Fatalf("the page in the copy: %q %q %d bytes, %v", title, deletedAt, len(content), err)
	}
	if err := copied.QueryRowContext(ctx, "SELECT deleted_at FROM pages WHERE id = ?", gone.ID).Scan(&deletedAt); err != nil || deletedAt == "" {
		t.Fatalf("the trashed page in the copy: %q, %v", deletedAt, err)
	}

	// Once a day; a plain copy left by a crash after the archive was written
	// goes all the same. The newest BackupsKept stay.
	plant("copy.db")
	if err := store.BackupIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 1 {
		t.Fatalf("the backups folder after a second run today holds %v", entries)
	}
	for range workspace.BackupsKept + 2 {
		now = now.AddDate(0, 0, 1)
		if err := store.Upkeep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != workspace.BackupsKept || names[0] != "workspace-2026-11-30.db.gz" || names[len(names)-1] != "workspace-2026-12-13.db.gz" {
		t.Fatalf("the backups after %d more days = %v", workspace.BackupsKept+2, names)
	}
}

// unpack opens a compressed backup the way the runbook restores it.
func unpack(t *testing.T, name string) *sql.DB {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "workspace.db")
	out, err := os.Create(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, reader); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+plain+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}
