package sqlitedb_test

import (
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

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

func TestACopyHoldsWhatTheOpenStoreCommitted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := sqlitedb.Open(path, sqlitedb.Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	// Committed rows still in the WAL, which nothing has checkpointed.
	for range 100 {
		if _, err := db.ExecContext(ctx, "INSERT INTO notes(body) VALUES ('一条')"); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the rows are not in the WAL: %v, %v", info, err)
	}
	size, err := sqlitedb.Size(ctx, db)
	if err != nil || size <= 0 {
		t.Fatalf("Size() = %d, %v", size, err)
	}

	name := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(name, []byte("an older copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The store's one connection stays busy in a transaction meanwhile: the
	// copy reads through its own.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO notes(body) VALUES ('没提交')"); err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Copy(ctx, path, name); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the copy: %v, %v", info, err)
	}
	if _, err := os.Stat(name + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("the partial copy stayed: %v", err)
	}
	copied, err := sqlitedb.OpenReadOnly(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copied.Close() })
	var count int
	if err := copied.QueryRowContext(ctx, "SELECT count(*) FROM notes").Scan(&count); err != nil || count != 100 {
		t.Fatalf("the copy holds %d notes, %v; want the 100 committed", count, err)
	}
}

func TestADailyBackupIsGzippedOnceADayAndTheNewestStay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := sqlitedb.Open(path, sqlitedb.Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO notes(body) VALUES ('页面的正文')"); err != nil {
		t.Fatal(err)
	}
	size, err := sqlitedb.Size(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	// What copies cut short by a power cut left: a plain copy, renameio's
	// temporary archive, and another store's file that stays.
	for _, name := range []string{"a-2026-11-26.db.gz.partial", ".a-2026-11-26.db.gz4711", "b-2026-11-26.db"} {
		if err := os.WriteFile(filepath.Join(backups, name), []byte("left over"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 11, 27, 23, 30, 0, 0, time.UTC)
	var asked int64
	room := false
	backup := sqlitedb.DailyBackup{
		DB: db, Path: path, Dir: backups, Prefix: "a-", Compress: true, Keep: 3, Day: day,
		Admit: func(bytes int64) error {
			asked = bytes
			if !room {
				return errFull
			}
			return nil
		},
	}
	names := func() []string {
		t.Helper()
		entries, err := os.ReadDir(backups)
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, entry := range entries {
			found = append(found, entry.Name())
		}
		return found
	}

	// Without room, nothing is written, and the leftovers go all the same.
	if err := sqlitedb.BackUpDaily(ctx, backup); !errors.Is(err, errFull) {
		t.Fatalf("BackUpDaily() without room = %v", err)
	}
	if asked != 2*size {
		t.Fatalf("asked for %d bytes; want twice the database's %d", asked, size)
	}
	if got := names(); !slices.Equal(got, []string{"b-2026-11-26.db"}) {
		t.Fatalf("after a refused backup the folder holds %v", got)
	}

	room = true
	if err := sqlitedb.BackUpDaily(ctx, backup); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(backups, "a-2026-11-27.db.gz")
	if info, err := os.Stat(archive); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the day's archive: %v, %v", info, err)
	}
	copied := unpack(t, archive)
	var body string
	if err := copied.QueryRowContext(ctx, "SELECT body FROM notes").Scan(&body); err != nil || body != "页面的正文" {
		t.Fatalf("the archive holds %q, %v", body, err)
	}

	// Once a day: a second run leaves the archive as it is.
	before, _ := os.Stat(archive)
	if err := sqlitedb.BackUpDaily(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(archive); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the second run of the day wrote the archive again")
	}
	for range 3 {
		backup.Day = backup.Day.AddDate(0, 0, 1)
		if err := sqlitedb.BackUpDaily(ctx, backup); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"a-2026-11-28.db.gz", "a-2026-11-29.db.gz", "a-2026-11-30.db.gz", "b-2026-11-26.db"}
	if got := names(); !slices.Equal(got, want) {
		t.Fatalf("after three more days the folder holds %v; want %v", got, want)
	}
}

var errFull = errors.New("the volume is at its reserve")

// unpack opens a gzipped copy the way the restore runbooks do.
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
	plain := filepath.Join(t.TempDir(), "restored.db")
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
	db, err := sqlitedb.OpenReadOnly(plain)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestKeepNewestRemovesOnlyTheOldestOfItsKind(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a-2026-11-01.db", "a-2026-11-03.db", "a-2026-11-02.db", "b-2026-10-01.db", "a-notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqlitedb.KeepNewest(dir, "a-", ".db", 2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if want := []string{"a-2026-11-02.db", "a-2026-11-03.db", "a-notes.txt", "b-2026-10-01.db"}; !slices.Equal(names, want) {
		t.Fatalf("left %v; want %v", names, want)
	}
}
