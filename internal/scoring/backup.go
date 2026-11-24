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

// BackupIfDue writes today's backup unless it exists.
func (s *Store) BackupIfDue(ctx context.Context) error {
	dir := filepath.Join(s.dir, "backups")
	name := filepath.Join(dir, "scoring-"+s.now().UTC().Format("2006-01-02")+".db")
	if _, err := os.Stat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A copy is about the size of the database; below the reserve the day
	// goes without one, and RunBackups tries again within the hour.
	database, err := os.Stat(filepath.Join(s.dir, "scoring.db"))
	if err != nil {
		return err
	}
	if err := s.room(database.Size()); err != nil {
		return err
	}
	partial := name + ".partial"
	_ = os.Remove(partial)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		return err
	}
	if err := os.Rename(partial, name); err != nil {
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
