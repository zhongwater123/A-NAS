package sqlitedb

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/renameio/v2"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
)

// Size is how much room a copy of db needs: its pages, committed ones still
// in the WAL among them.
func Size(ctx context.Context, db *sql.DB) (int64, error) {
	var bytes int64
	err := db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&bytes)
	return bytes, err
}

// Copy writes a consistent copy of the database at path to name, readable by
// its owner alone, replacing one that is there; a failed copy leaves at most
// name.partial, which the next BackUpDaily in that folder removes. It reads
// through a read-only connection of its own: a store's own connection (Open
// allows one) stays free for requests while a large copy runs, and in WAL
// mode the copy neither waits for writers nor stops them.
func Copy(ctx context.Context, path, name string) error {
	partial := name + ".partial"
	if err := vacuumInto(ctx, path, partial); err != nil {
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, name); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(name))
}

// vacuumInto writes a consistent copy of the database at path to target.
func vacuumInto(ctx context.Context, path, target string) error {
	db, err := OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_ = os.Remove(target)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

// DailyBackup is a store's daily copy of its database (BackUpDaily).
type DailyBackup struct {
	// DB is the store's connection, which sizes the copy; Path is its file.
	DB   *sql.DB
	Path string
	// Dir is the backups folder, made readable by its owner alone.
	Dir string
	// A day's copy is Prefix+YYYY-MM-DD+".db", or ".db.gz" when Compress
	// has it gzipped; the newest Keep files named so stay.
	Prefix   string
	Compress bool
	Keep     int
	// Day is when the backup runs; its UTC date names the copy.
	Day time.Time
	// Admit is asked for the room the copy takes on the volume, and returns
	// the caller's error when there is not that much above its reserve.
	Admit func(bytes int64) error
}

// BackUpDaily writes the day's copy of the database unless it is there, and
// keeps the newest. It first removes what a copy cut short by a crash or a
// power cut left in the folder, so that nothing stays there for good or
// counts against the room the next copy asks for. A copy guards against a
// damaged database or a mistake, not against losing the disk it is on.
func BackUpDaily(ctx context.Context, backup DailyBackup) error {
	if err := removeLeftovers(backup.Dir, backup.Prefix); err != nil {
		return err
	}
	suffix := ".db"
	if backup.Compress {
		suffix = ".db.gz"
	}
	name := filepath.Join(backup.Dir, backup.Prefix+backup.Day.UTC().Format("2006-01-02")+suffix)
	if _, err := os.Stat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	bytes, err := Size(ctx, backup.DB)
	if err != nil {
		return err
	}
	// The plain copy and the archive are on the volume together for a moment.
	if backup.Compress {
		bytes *= 2
	}
	if err := backup.Admit(bytes); err != nil {
		return err
	}
	if err := os.MkdirAll(backup.Dir, 0o700); err != nil {
		return err
	}
	if backup.Compress {
		err = compressedCopy(ctx, backup.Path, name)
	} else {
		err = Copy(ctx, backup.Path, name)
	}
	if err != nil {
		return err
	}
	return KeepNewest(backup.Dir, backup.Prefix, suffix, backup.Keep)
}

// removeLeftovers removes the files of copies that did not finish: the
// copies' ….partial and renameio's temporary files, named after the archive
// with a dot in front.
func removeLeftovers(dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".partial") || strings.HasPrefix(name, "."+prefix) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// compressedCopy writes a gzipped copy of the database at path to name,
// readable by its owner alone. The plain copy loses its name as soon as it
// is open, so a crash or a cancelled copy leaves no plain copy behind.
// gzip's fastest level halves pages of Chinese text at about 7 seconds a
// gigabyte (130 MB of versions: 0.9 s, 71 MB); its default level saves 4 %
// more in two and a half times as long.
func compressedCopy(ctx context.Context, path, name string) error {
	plain := name + ".partial"
	if err := vacuumInto(ctx, path, plain); err != nil {
		return err
	}
	in, err := os.Open(plain)
	removed := os.Remove(plain)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if removed != nil {
		return removed
	}
	dir := filepath.Dir(name)
	pending, err := renameio.NewPendingFile(name, renameio.WithTempDir(dir), renameio.WithStaticPermissions(0o600))
	if err != nil {
		return err
	}
	defer func() { _ = pending.Cleanup() }()
	writer, err := gzip.NewWriterLevel(pending, gzip.BestSpeed)
	if err != nil {
		return err
	}
	// A large database takes a while; stopping the service does not wait for it.
	if _, err := io.Copy(writer, cancellable{ctx, in}); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := pending.CloseAtomicallyReplace(); err != nil {
		return err
	}
	return atomicfile.SyncDir(dir)
}

// cancellable reads from r until ctx is done.
type cancellable struct {
	ctx context.Context
	r   io.Reader
}

func (c cancellable) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
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
