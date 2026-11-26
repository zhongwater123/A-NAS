// Package db is the montage center's own database: montage.db in the
// store's root, its tables and their steps (schema.go), and the reads and
// writes of each kind of record. It imports no other layer of
// internal/videocut but domain (docs/architecture/montage-center.md,
// 代码结构).
package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// DB is the open database. Its reads and writes are its methods, one file
// for each kind of record; app writes no SQL of its own (#665).
type DB struct{ db *sql.DB }

// Open opens, creating when missing, the database in root and takes it to
// the newest step. Commits wait until they are on disk: a job that was
// told it advanced must still have advanced after a power cut.
func Open(ctx context.Context, root string) (*DB, error) {
	database, err := sqlitedb.Open(filepath.Join(root, DatabaseName), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, database, schema); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return &DB{db: database}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// SQLForTests is the database's own handle, for tests that read what no
// method reads or set up states only a crash or an earlier release leaves
// behind. Code outside tests reads and writes through the methods.
func (d *DB) SQLForTests() *sql.DB { return d.db }

// Tx is one transaction: reads and writes that stand or fall together.
type Tx struct{ tx *sql.Tx }

// queryer is the database or one transaction in it, for the reads and
// writes that app makes in both (DB.db, Tx.tx).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Update runs apply in one transaction and commits it when apply returns
// nil; otherwise nothing apply wrote stays.
func (d *DB) Update(ctx context.Context, apply func(*Tx) error) error {
	return sqlitedb.WithTx(ctx, d.db, func(tx *sql.Tx) error { return apply(&Tx{tx: tx}) })
}
