package workspace

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

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
	plain := filepath.Join(dir, "copy.db")
	defer func() { _ = os.Remove(plain) }()
	if err := sqlitedb.Copy(ctx, filepath.Join(s.directory, "workspace.db"), plain); err != nil {
		return err
	}
	if err := compress(plain, name); err != nil {
		return err
	}
	return sqlitedb.KeepNewest(dir, "workspace-", ".db.gz", BackupsKept)
}

// compress writes plain to name with gzip, readable by its owner alone; a
// failed write leaves nothing behind.
func compress(plain, name string) (err error) {
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
	if _, err = io.Copy(writer, in); err != nil {
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
