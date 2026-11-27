package workspace

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// BackupIfDue writes today's copy of workspace.db, compressed, into the
// store's backups folder unless it is there, and keeps the newest
// BackupsKept. A copy guards against a damaged database or a mistake that
// neither versions nor the trash undo, not against losing the disk
// (docs/runbooks/restore-workspace-database.md).
//
// The copy reads through a connection of its own, so pages stay readable
// and savable while it runs. gzip's fastest level halves pages of Chinese
// text at about 7 seconds a gigabyte (130 MB of versions: 0.9 s, 71 MB);
// its default level saves 4 % more in two and a half times as long.
func (s *Store) BackupIfDue(ctx context.Context) error {
	dir := filepath.Join(s.directory, "backups")
	// A backup cut short by a crash or a power cut leaves its plain copy or
	// half an archive; they go first, so they neither stay for good nor
	// count against the room the next copy asks for.
	if err := removeLeftovers(dir); err != nil {
		return err
	}
	name := filepath.Join(dir, "workspace-"+s.now().UTC().Format("2006-01-02")+".db.gz")
	if _, err := os.Stat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	bytes, err := sqlitedb.Size(ctx, s.db)
	if err != nil {
		return err
	}
	// The plain copy and the compressed one are on the volume together for
	// a moment.
	if admitted, err := s.admits(s.directory, 2*bytes); err != nil {
		return err
	} else if !admitted {
		return ErrVolumeFull
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	plain := filepath.Join(dir, plainCopy)
	defer func() { _ = os.Remove(plain) }()
	if err := sqlitedb.Copy(ctx, filepath.Join(s.directory, "workspace.db"), plain); err != nil {
		return err
	}
	if err := compress(ctx, plain, name); err != nil {
		return err
	}
	return sqlitedb.KeepNewest(dir, "workspace-", ".db.gz", BackupsKept)
}

// plainCopy is the uncompressed copy a backup compresses. Its name is fixed,
// so leftovers are found.
const plainCopy = "copy.db"

// removeLeftovers removes the plain copy and the half-written files
// (….partial) of a backup that did not finish.
func removeLeftovers(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, entry := range entries {
		if name := entry.Name(); name == plainCopy || strings.HasSuffix(name, ".partial") {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// compress writes plain to name with gzip, readable by its owner alone; a
// failed or cancelled write leaves nothing behind.
func compress(ctx context.Context, plain, name string) (err error) {
	in, err := os.Open(plain)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	partial := name + ".partial"
	out, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(partial)
		}
	}()
	writer, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		return err
	}
	// A large database takes a while; stopping the service does not wait for it.
	if _, err = io.Copy(writer, cancellable{ctx, in}); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(partial, name)
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
