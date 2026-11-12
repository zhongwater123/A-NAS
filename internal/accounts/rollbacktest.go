//go:build rollbacktest

package accounts

import "github.com/zhongwater123/A-NAS/internal/sqlitedb"

// A step only the system test's "next" release has (scripts/system-test.sh,
// ADR 0034): apt installs that release and then rolls back from it, and the
// release before takes control.db back with the Down this step stored.
func init() {
	databaseSchema.Steps = append(databaseSchema.Steps, sqlitedb.Step{
		Up:   "CREATE TABLE rollback_probe (id INTEGER PRIMARY KEY); INSERT INTO rollback_probe VALUES (1);",
		Down: "DROP TABLE rollback_probe;",
	})
}
