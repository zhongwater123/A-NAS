package websearch

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Store keeps the torrent tab's switch and the engines' pauses in
// web-search.db, beside the Product Service's other state; the switch of web
// search itself is the Host Agent's.
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
);
CREATE TABLE IF NOT EXISTS pauses (
    engine TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    strikes INTEGER NOT NULL,
    until INTEGER NOT NULL
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

// Pauses are website pauses, including expired ones. The SQL column keeps
// its legacy name; Service migrates older per-engine keys on first read.
func (s *Store) Pauses(ctx context.Context) (map[string]Pause, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT engine, reason, strikes, until FROM pauses")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pauses := map[string]Pause{}
	for rows.Next() {
		var (
			engine string
			pause  Pause
			until  int64
		)
		if err := rows.Scan(&engine, &pause.Reason, &pause.Strikes, &until); err != nil {
			return nil, err
		}
		pause.Until = time.Unix(until, 0)
		pauses[engine] = pause
	}
	return pauses, rows.Err()
}

func (s *Store) SetPause(ctx context.Context, engine string, pause Pause) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pauses(engine, reason, strikes, until) VALUES(?, ?, ?, ?)
ON CONFLICT(engine) DO UPDATE SET reason = excluded.reason, strikes = excluded.strikes, until = excluded.until`,
		engine, pause.Reason, pause.Strikes, pause.Until.Unix())
	return err
}

// ClearPause forgets an engine's blocks once it answers again.
func (s *Store) ClearPause(ctx context.Context, engine string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM pauses WHERE engine = ?", engine)
	return err
}

// replacePauses migrates legacy engine keys to website keys in one commit.
func (s *Store) replacePauses(ctx context.Context, pauses map[string]Pause) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM pauses"); err != nil {
		return err
	}
	for site, pause := range pauses {
		if _, err := tx.ExecContext(ctx, "INSERT INTO pauses(engine, reason, strikes, until) VALUES(?, ?, ?, ?)", site, pause.Reason, pause.Strikes, pause.Until.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
