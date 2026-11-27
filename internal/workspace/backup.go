package workspace

import (
	"context"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// BackupIfDue writes today's gzipped copy of workspace.db into the store's
// backups folder unless it is there, and keeps the newest BackupsKept
// (sqlitedb.BackUpDaily). A copy guards against a damaged database or a
// mistake that neither versions nor the trash undo, not against losing the
// disk (docs/runbooks/restore-workspace-database.md). The copy reads through
// a connection of its own, so pages stay readable and savable while it runs.
func (s *Store) BackupIfDue(ctx context.Context) error {
	return sqlitedb.BackUpDaily(ctx, sqlitedb.DailyBackup{
		DB:       s.db,
		Path:     filepath.Join(s.directory, "workspace.db"),
		Dir:      filepath.Join(s.directory, "backups"),
		Prefix:   "workspace-",
		Compress: true,
		Keep:     BackupsKept,
		Day:      s.now(),
		Admit:    s.admit,
	})
}
