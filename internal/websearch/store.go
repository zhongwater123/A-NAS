package websearch

import (
	"context"
	"database/sql"
	"errors"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Store keeps the torrent tab's switch in web-search.db, beside the Product
// Service's other state; the switch of web search itself is the Host
// Agent's.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	const schema = `
CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    torrents INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Torrents is off until an administrator turns it on.
func (s *Store) Torrents(ctx context.Context) (bool, error) {
	var torrents bool
	err := s.db.QueryRowContext(ctx, "SELECT torrents FROM settings WHERE id = 1").Scan(&torrents)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return torrents, err
}

func (s *Store) SetTorrents(ctx context.Context, torrents bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(id, torrents) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET torrents = excluded.torrents`, torrents)
	return err
}
