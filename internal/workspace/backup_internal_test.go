package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Stopping the service stops a compression under way, and it leaves no half
// archive behind.
func TestACancelledCompressionLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, plainCopy)
	if err := os.WriteFile(plain, bytes.Repeat([]byte("页面"), 1<<16), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := compress(ctx, plain, filepath.Join(dir, "workspace-2026-11-27.db.gz")); !errors.Is(err, context.Canceled) {
		t.Fatalf("compress() after cancelling = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != plainCopy {
		t.Fatalf("a cancelled compression left %v, %v", entries, err)
	}
}
