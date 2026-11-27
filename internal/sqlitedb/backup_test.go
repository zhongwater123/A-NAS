package sqlitedb_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
