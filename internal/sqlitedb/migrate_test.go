package sqlitedb_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb/sqlitedbtest"
)

// notesSchema is a store as it grows: a baseline from before steps, two steps
// released without Downs, then steps with them.
func notesSchema(baselineRuns *int) sqlitedb.Schema {
	return sqlitedb.Schema{
		Name: "notes.db",
		Baseline: func(ctx context.Context, db *sql.DB) error {
			if baselineRuns != nil {
				*baselineRuns++
			}
			_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)`)
			return err
		},
		Steps: []sqlitedb.Step{
			{Up: `ALTER TABLE notes ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`},
			{Up: `CREATE INDEX notes_created_at ON notes(created_at)`},
			{
				Up:   `ALTER TABLE notes ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`,
				Down: `ALTER TABLE notes DROP COLUMN pinned`,
			},
			{
				Up: `CREATE TABLE tags (note_id INTEGER NOT NULL REFERENCES notes(id), tag TEXT NOT NULL);
INSERT INTO tags SELECT id, 'pinned' FROM notes WHERE pinned = 1;`,
				Down: `UPDATE notes SET pinned = 1 WHERE id IN (SELECT note_id FROM tags WHERE tag = 'pinned'); DROP TABLE tags;`,
			},
		},
		WithoutDown: 2,
	}
}

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func withSteps(schema sqlitedb.Schema, steps int) sqlitedb.Schema {
	schema.Steps = schema.Steps[:steps]
	return schema
}

func TestMigrateAppliesTheBaselineOnlyBeforeTheFirstStep(t *testing.T) {
	ctx := context.Background()
	db := open(t, filepath.Join(t.TempDir(), "notes.db"))
	runs := 0
	schema := notesSchema(&runs)
	if err := sqlitedb.Migrate(ctx, db, withSteps(schema, 0)); err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Migrate(ctx, db, withSteps(schema, 0)); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || version(t, db) != 0 {
		t.Fatalf("baseline ran %d times, version %d; want 2 and 0", runs, version(t, db))
	}
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if runs != 3 || version(t, db) != 4 {
		t.Fatalf("baseline ran %d times, version %d; want 3 and 4", runs, version(t, db))
	}
}

func TestAnOlderBuildTakesANewerDatabaseBackWithItsDowns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notes.db")
	newer := open(t, path)
	if err := sqlitedb.Migrate(ctx, newer, notesSchema(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := newer.Exec(`INSERT INTO notes (id, body, created_at) VALUES (1, 'kept', '2026-11-11');
INSERT INTO tags VALUES (1, 'pinned');`); err != nil {
		t.Fatal(err)
	}
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}

	older := open(t, path)
	if err := sqlitedb.Migrate(ctx, older, withSteps(notesSchema(nil), 3)); err != nil {
		t.Fatal(err)
	}
	var body string
	var pinned int
	if err := older.QueryRow("SELECT body, pinned FROM notes WHERE id = 1").Scan(&body, &pinned); err != nil {
		t.Fatal(err)
	}
	if body != "kept" || pinned != 1 || version(t, older) != 3 {
		t.Fatalf("got %q pinned=%d at version %d; want the note kept, pinned again, at version 3", body, pinned, version(t, older))
	}
	// Going back past the steps released without Downs is not possible: every
	// build that opens the database has them.
	err := sqlitedb.Migrate(ctx, older, withSteps(notesSchema(nil), 1))
	if !errors.Is(err, sqlitedb.ErrNoDown) || !strings.Contains(err.Error(), "restore the copy taken before the upgrade") {
		t.Fatalf("got %v; want ErrNoDown with the way out", err)
	}
	if version(t, older) != 2 {
		t.Fatalf("version %d after the refused step back; want 2, the steps with Downs undone", version(t, older))
	}
}

func TestADatabaseWithoutStoredDownsIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	db := open(t, filepath.Join(t.TempDir(), "notes.db"))
	if _, err := db.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY); PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	err := sqlitedb.Migrate(ctx, db, notesSchema(nil))
	if !errors.Is(err, sqlitedb.ErrNoDown) {
		t.Fatalf("got %v; want ErrNoDown", err)
	}
	if version(t, db) != 7 {
		t.Fatalf("version %d; want 7, unchanged", version(t, db))
	}
}

func TestEveryStepAfterTheReleasedOnesNeedsADown(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "notes.db"))
	schema := notesSchema(nil)
	schema.Steps = append(schema.Steps, sqlitedb.Step{Up: "CREATE TABLE later (id INTEGER)"})
	err := sqlitedb.Migrate(context.Background(), db, schema)
	if err == nil || !strings.Contains(err.Error(), "notes.db step 5 has no Down") {
		t.Fatalf("got %v; want the step without a Down named", err)
	}
	if version(t, db) != 0 {
		t.Fatal("a schema with a step lacking its Down changed the database")
	}
}

func TestAFailedStepLeavesTheVersionBefore(t *testing.T) {
	ctx := context.Background()
	db := open(t, filepath.Join(t.TempDir(), "notes.db"))
	schema := notesSchema(nil)
	schema.Steps = append(schema.Steps, sqlitedb.Step{Up: "CREATE TABLE later (id INTEGER); SELECT * FROM missing", Down: "DROP TABLE later"})
	if err := sqlitedb.Migrate(ctx, db, schema); err == nil {
		t.Fatal("a failing step succeeded")
	}
	var later int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'later'").Scan(&later); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != 4 || later != 0 {
		t.Fatalf("version %d, table later %d; want 4 and none", version(t, db), later)
	}
}

func TestCheckReversible(t *testing.T) {
	sqlitedbtest.CheckReversible(t, notesSchema(nil), func(path string) (*sql.DB, error) {
		return sqlitedb.Open(path, sqlitedb.Options{})
	})
}
