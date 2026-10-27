package sqlitedb

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDatabasesOpenWithTheSameSettings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path, Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	for pragma, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "2"} {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE notes(text TEXT)"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.ExecContext(ctx, "INSERT INTO notes(text) VALUES('x')"); err == nil {
		t.Fatal("a read-only database accepted a write")
	}
	if _, err := Open(" ", Options{}); err == nil {
		t.Fatal("an empty path opened")
	}
}
