package montage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

func TestTheStoreOpensDurablyAndListsNothingYet(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := store.Projects(ctx, "user:1")
	if err != nil || projects == nil || len(projects) != 0 {
		t.Fatalf("Projects() = %v, %v; want an empty list", projects, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.OpenReadOnly(filepath.Join(root, DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	// Reopening an existing store keeps it.
	store, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
}
