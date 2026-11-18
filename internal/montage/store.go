// Package montage keeps the montage center's records (剪辑中心, ADR 0038):
// projects, plans, timelines and jobs, in montage.db on the montage store.
// The videos, their analysis and the films stay in the photo library.
package montage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// DatabaseName is the store's database file.
const DatabaseName = "montage.db"

// schema grows a step per change (sqlitedb.Schema); every step has a Down,
// so that an older release takes the database back.
var schema = sqlitedb.Schema{
	Name:     DatabaseName,
	Baseline: func(context.Context, *sql.DB) error { return nil },
	Steps: []sqlitedb.Step{
		// 1: projects, the batches that brought their videos, the videos
		// fixed to their content, and grants waiting to be revoked.
		{Up: `
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    grant_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('registering', 'ready')),
    grant_lost INTEGER NOT NULL DEFAULT 0,
    ask TEXT NOT NULL DEFAULT '',
    chronological INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX projects_by_owner ON projects(owner_user_id, created_at, id);

CREATE TABLE batches (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (owner_user_id, request_key)
);

CREATE TABLE project_assets (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    asset_id TEXT NOT NULL,
    object_id TEXT NOT NULL,
    batch_id TEXT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL,
    PRIMARY KEY (project_id, asset_id)
);

CREATE TABLE revocations (
    grant_id TEXT PRIMARY KEY,
    next_try TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0
);
`, Down: `
DROP TABLE revocations;
DROP TABLE project_assets;
DROP TABLE batches;
DROP TABLE projects;
`},
	},
}

// Store is the montage center's database. Every record names the member it
// belongs to, and every read is for one member.
type Store struct {
	db     *sql.DB
	grants Grants
	now    func() time.Time
	logger *slog.Logger
}

// Options are what a store works with.
type Options struct {
	// Grants reaches the photo library's project grants.
	Grants Grants
	Now    func() time.Time
	Logger *slog.Logger
}

// Open opens, creating when missing, the store's database in root. Commits
// wait until they are on disk: a job that was told it advanced must still
// have advanced after a power cut.
func Open(ctx context.Context, root string, options Options) (*Store, error) {
	if options.Grants == nil {
		return nil, errors.New("the montage store needs the photo library's grants")
	}
	db, err := sqlitedb.Open(filepath.Join(root, DatabaseName), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	store := &Store{db: db, grants: options.Grants, now: options.Now, logger: options.Logger}
	if store.now == nil {
		store.now = time.Now
	}
	if store.logger == nil {
		store.logger = slog.Default()
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
