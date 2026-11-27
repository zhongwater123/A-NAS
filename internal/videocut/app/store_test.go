package app_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
)

func TestTheStoreOpensDurablyAndListsNothingYet(t *testing.T) {
	ctx := context.Background()
	store, _, root := newStore(t)
	page, err := store.Projects(ctx, "user:1", "")
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("Projects() = %+v, %v; want an empty list", page, err)
	}
	db, err := sqlitedb.OpenReadOnly(filepath.Join(root, db.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}
