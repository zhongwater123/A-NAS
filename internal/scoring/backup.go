package scoring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// StagingDir is a directory in the store for temporary files such as
// exports before they are sent.
func (s *Store) StagingDir() string { return filepath.Join(s.dir, "staging") }

// RunBackups copies the database once a day into the store's backups
// folder and keeps the latest backupsKept copies. A copy guards against a
// mistake or a damaged file, not against losing the disk: exports are what
// administrators keep elsewhere.
func (s *Store) RunBackups(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	// Below the reserve the backup waits, said once, as imports do.
	waiting := false
	for {
		err := s.BackupIfDue(ctx)
		switch {
		case ctx.Err() != nil:
		case errors.Is(err, ErrInsufficientSpace):
			if !waiting {
				s.logger.WarnContext(ctx, "photo scoring backup waits: the data volume is at its reserve")
			}
			waiting = true
		case err != nil:
			s.logger.ErrorContext(ctx, "photo scoring backup failed", "error", err)
		case waiting:
			s.logger.InfoContext(ctx, "photo scoring backup no longer waits for space")
			waiting = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// BackupIfDue writes today's backup unless it exists. The copies taken
// before an upgrade (migrate), named after the version they hold as in
// scoring-2026-11-24-v4.db, count among the kept ones.
func (s *Store) BackupIfDue(ctx context.Context) error {
	dir := filepath.Join(s.dir, "backups")
	name := filepath.Join(dir, "scoring-"+s.now().UTC().Format("2006-01-02")+".db")
	if _, err := os.Stat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.copyTo(ctx, name); err != nil {
		return err
	}
	return sqlitedb.KeepNewest(dir, "scoring-", ".db", backupsKept)
}

// copyTo writes a consistent copy of the database to name, readable by the
// module's user alone, replacing one that is there.
func (s *Store) copyTo(ctx context.Context, name string) error {
	bytes, err := sqlitedb.Size(ctx, s.db)
	if err != nil {
		return err
	}
	if err := s.room(bytes); err != nil {
		return err
	}
	return sqlitedb.Copy(ctx, filepath.Join(s.dir, "scoring.db"), name)
}
