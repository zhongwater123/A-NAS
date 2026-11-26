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

// DB is the open database. It still lends app its *sql.DB while the reads
// and writes move here (#665).
type DB struct{ *sql.DB }

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
	return &DB{database}, nil
}
