package sqlitedb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stopping is a context whose service is stopping: it reports cancelled,
// but never closes Done, so the copy itself runs and only the compression
// sees it.
type stopping struct{ context.Context }

func (stopping) Err() error { return context.Canceled }

// Stopping the service stops a compression under way, and it leaves neither
// the plain copy nor half an archive behind.
func TestACancelledCompressionLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	db, err := Open(path, Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE notes (body TEXT)"); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	err = compressedCopy(stopping{context.Background()}, path, filepath.Join(backups, "a-2026-11-27.db.gz"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("compressedCopy() while stopping = %v", err)
	}
	if entries, err := os.ReadDir(backups); err != nil || len(entries) != 0 {
		t.Fatalf("a cancelled compression left %v, %v", entries, err)
	}
}
