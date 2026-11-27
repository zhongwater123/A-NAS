package sqlitedb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Size is how much room a copy of db needs: its pages, committed ones still
// in the WAL among them.
func Size(ctx context.Context, db *sql.DB) (int64, error) {
	var bytes int64
	err := db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&bytes)
	return bytes, err
}

// Copy writes a consistent copy of the database at path to name, readable by
// its owner alone, replacing one that is there; a failed copy leaves nothing
// behind. It reads through a read-only connection of its own: a store's own
// connection (Open allows one) stays free for requests while a large copy
// runs, and in WAL mode the copy neither waits for writers nor stops them.
func Copy(ctx context.Context, path, name string) error {
	db, err := OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	partial := name + ".partial"
	_ = os.Remove(partial)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return os.Rename(partial, name)
}

// KeepNewest removes the oldest of the files in dir named prefix…suffix until
// keep remain. The names hold dates that sort as text, so the oldest sort
// first.
func KeepNewest(dir, prefix, suffix string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var kept []string
	for _, entry := range entries {
		if name := entry.Name(); strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			kept = append(kept, name)
		}
	}
	slices.Sort(kept)
	for len(kept) > keep {
		if err := os.Remove(filepath.Join(dir, kept[0])); err != nil {
			return err
		}
		kept = kept[1:]
	}
	return nil
}
