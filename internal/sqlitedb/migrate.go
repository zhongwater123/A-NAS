package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Step is one change to a database's structure (ADR 0034). Up makes it;
// Down undoes it, so that a database goes back with a release. Both are SQL
// statements: an older build runs the Down a newer build left behind, so it
// cannot be code.
type Step struct {
	Up   string
	Down string
}

// Schema is how a store's database gets its structure.
//
// PRAGMA user_version counts the steps applied. Applying a step also stores
// its Down in the database, so a build with fewer steps that opens a newer
// database, as after apt installs an older release or a device falls back to
// one, takes it back to its own version with the newer build's own Downs.
// The release's packages back the databases up before an upgrade; a database
// that lacks a Down is left alone and the store does not open.
type Schema struct {
	// Name names the database in errors, such as "control.db".
	Name string
	// Baseline makes the structure a store had before it had steps, from
	// nothing or from any earlier form. It runs while user_version is 0, on
	// a new database or one that no step has reached, so it repeats safely.
	Baseline func(ctx context.Context, db *sql.DB) error
	// Steps are applied in order. An applied step is never edited: databases
	// on devices and in backups have it; add a new one instead.
	Steps []Step
	// WithoutDown counts the first steps, which were released before steps
	// had Downs. Every later step has one; a database never goes back past
	// them, because every release that runs it has them.
	WithoutDown int
}

// Statements is a baseline that runs SQL statements, such as CREATE TABLE IF
// NOT EXISTS.
func Statements(statements string) func(context.Context, *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, statements)
		return err
	}
}

// downgrades holds the Down of every applied step that has one, keyed by the
// user_version the step brought the database to.
const downgrades = "schema_downgrades"

// ErrNoDown reports a database that a newer build changed without leaving the
// reverse of the change.
var ErrNoDown = errors.New("the newer build left no reverse step")

// Migrate brings db to the schema's version: back with stored Downs when a
// newer build left it ahead, then the baseline, then forward step by step.
func Migrate(ctx context.Context, db *sql.DB, schema Schema) error {
	for index, step := range schema.Steps {
		if index >= schema.WithoutDown && step.Down == "" {
			return fmt.Errorf("%s step %d has no Down", schema.Name, index+1)
		}
	}
	version, err := userVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read the version of %s: %w", schema.Name, err)
	}
	for ; version > len(schema.Steps); version-- {
		if err := stepBack(ctx, db, version); err != nil {
			return fmt.Errorf("take %s back from version %d to %d, the newest this build has: %w; restore the copy taken before the upgrade",
				schema.Name, version, len(schema.Steps), err)
		}
	}
	if version == 0 && schema.Baseline != nil {
		if err := schema.Baseline(ctx, db); err != nil {
			return fmt.Errorf("migrate %s: %w", schema.Name, err)
		}
	}
	for ; version < len(schema.Steps); version++ {
		if err := stepUp(ctx, db, version+1, schema.Steps[version]); err != nil {
			return fmt.Errorf("migrate %s to version %d: %w", schema.Name, version+1, err)
		}
	}
	return nil
}

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

func stepUp(ctx context.Context, db *sql.DB, version int, step Step) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, step.Up); err != nil {
			return err
		}
		if step.Down != "" {
			if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+downgrades+
				" (version INTEGER PRIMARY KEY, statements TEXT NOT NULL)"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO "+downgrades+
				" (version, statements) VALUES (?, ?)", version, step.Down); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
		return err
	})
}

func stepBack(ctx context.Context, db *sql.DB, version int) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		var statements string
		err := tx.QueryRowContext(ctx, "SELECT statements FROM "+downgrades+" WHERE version = ?", version).Scan(&statements)
		if errors.Is(err, sql.ErrNoRows) || err != nil && !hasDowngrades(ctx, tx) {
			return fmt.Errorf("version %d: %w", version, ErrNoDown)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, statements); err != nil {
			return fmt.Errorf("version %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+downgrades+" WHERE version = ?", version); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version-1))
		return err
	})
}

// hasDowngrades reports whether the database stores any Downs at all.
func hasDowngrades(ctx context.Context, tx *sql.Tx) bool {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", downgrades).Scan(&count)
	return err != nil || count > 0
}

func inTx(ctx context.Context, db *sql.DB, apply func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := apply(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
