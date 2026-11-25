package accounts

import (
	"database/sql"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb/sqlitedbtest"
)

func TestDatabaseStepsHaveWorkingDowns(t *testing.T) {
	sqlitedbtest.CheckReversible(t, databaseSchema, func(path string) (*sql.DB, error) {
		return sqlitedb.Open(path, sqlitedb.Options{})
	})
}
