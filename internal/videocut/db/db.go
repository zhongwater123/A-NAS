// Package db is VideoCut's own database: videocut.db in the
// store's root, its tables and their steps (schema.go), and the reads and
// writes of each kind of record. It imports no other layer of
// internal/videocut but domain (docs/architecture/videocut-center.md,
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
type DB struct {
	conn
	db *sql.DB
}

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
	return &DB{q: database, db: database}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// SQLForTests is the database's own handle, for tests that read what no
// method reads or set up states only a crash or an earlier release leaves
// behind. Code outside tests reads and writes through the methods.
func (d *DB) SQLForTests() *sql.DB { return d.db }

// Tx is one transaction: reads and writes that stand or fall together.
type Tx struct {
	conn
	tx *sql.Tx
}

// conn holds the reads and writes that app makes both on the database and
// inside a transaction: DB and Tx each have its methods.
type conn struct{ q queryer }

// queryer is the database or one transaction in it (DB.db, Tx.tx).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ErrNotFound is what a read of one record answers when there is no such
// record: app tells "not found" by it and not by the database's own error.
var ErrNotFound = errors.New("videocut: no such record")

// queryRow runs query for one row on q. Its Scan answers ErrNotFound when
// the query gives none.
func queryRow(ctx context.Context, q queryer, query string, args ...any) row {
	return row{q.QueryRowContext(ctx, query, args...)}
}

// row is one row queryRow read.
type row struct{ row *sql.Row }

// Scan copies the row's columns into dest, as sql.Row.Scan does.
func (r row) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// column is the values of the one column query selects, in the order it
// gives them.
func column[T any](ctx context.Context, q queryer, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []T
	for rows.Next() {
		var value T
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// set is the values of column as a set.
func set[T comparable](values []T, err error) (map[T]bool, error) {
	if err != nil {
		return nil, err
	}
	present := make(map[T]bool, len(values))
	for _, value := range values {
		present[value] = true
	}
	return present, nil
}

// Update runs apply in one transaction and commits it when apply returns
// nil; otherwise nothing apply wrote stays.
func (d *DB) Update(ctx context.Context, apply func(*Tx) error) error {
	return sqlitedb.WithTx(ctx, d.db, func(tx *sql.Tx) error { return apply(&Tx{q: tx, tx: tx}) })
}
