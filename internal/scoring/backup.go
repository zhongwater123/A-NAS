package scoring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
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
	for {
		if err := s.BackupIfDue(ctx); err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "photo scoring backup failed", "error", err)
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var backups []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "scoring-") && strings.HasSuffix(entry.Name(), ".db") {
			backups = append(backups, entry.Name())
		}
	}
	slices.Sort(backups)
	for len(backups) > backupsKept {
		if err := os.Remove(filepath.Join(dir, backups[0])); err != nil {
			return err
		}
		backups = backups[1:]
	}
	return nil
}

// copyTo writes a consistent copy of the database to name, readable by the
// module's user alone, replacing one that is there.
func (s *Store) copyTo(ctx context.Context, name string) error {
	partial := name + ".partial"
	_ = os.Remove(partial)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		return err
	}
	return os.Rename(partial, name)
}
