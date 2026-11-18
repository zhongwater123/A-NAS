// Package montage keeps the montage center's records (剪辑中心, ADR 0038):
// projects, plans, timelines and jobs, in montage.db on the montage store.
// The videos, their analysis and the films stay in the photo library.
package montage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// DatabaseName is the store's database file.
const DatabaseName = "montage.db"

// schema grows a step per change (sqlitedb.Schema). The tables come with
// the projects (M3); until then the database only carries its version.
var schema = sqlitedb.Schema{
	Name:     DatabaseName,
	Baseline: func(context.Context, *sql.DB) error { return nil },
}

// Store is the montage center's database. Every record names the member it
// belongs to, and every read is for one member.
type Store struct {
	db *sql.DB
}

// Open opens, creating when missing, the store's database in root. Commits
// wait until they are on disk: a job that was told it advanced must still
// have advanced after a power cut.
func Open(ctx context.Context, root string) (*Store, error) {
	db, err := sqlitedb.Open(filepath.Join(root, DatabaseName), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Project is a member's montage project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Projects lists owner's projects, newest first.
func (s *Store) Projects(ctx context.Context, owner string) ([]Project, error) {
	if err := s.db.PingContext(ctx); err != nil {
		return nil, err
	}
	return []Project{}, nil
}

// RemoveAccount forgets a deleted account's projects. Its videos and films
// are the photo library's to remove.
func (s *Store) RemoveAccount(ctx context.Context, userID string) error {
	return s.db.PingContext(ctx)
}
