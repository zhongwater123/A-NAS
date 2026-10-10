package media

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is media.db on the system disk: libraries, the catalog read from the
// files, and each user's progress, favorites and collections (ADR 0017).
type Store struct {
	db *sql.DB
}

func OpenSQLite(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS libraries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('movies', 'shows', 'mixed', 'other')),
    space_id TEXT NOT NULL,
    space_kind TEXT NOT NULL CHECK (space_kind IN ('private', 'shared')),
    owner_user_id TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    scanned_at TEXT NOT NULL DEFAULT '',
    scan_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS library_folders (
    library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    entry_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    missing INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (library_id, entry_id)
);
CREATE TABLE IF NOT EXISTS shows (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    title TEXT NOT NULL,
    original_title TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    plot TEXT NOT NULL DEFAULT '',
    genres TEXT NOT NULL DEFAULT '[]',
    rating REAL NOT NULL DEFAULT 0,
    poster_entry TEXT NOT NULL DEFAULT '',
    backdrop_entry TEXT NOT NULL DEFAULT '',
    nfo_entry TEXT NOT NULL DEFAULT '',
    nfo_modified TEXT NOT NULL DEFAULT '',
    nfo TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL,
    UNIQUE (library_id, key)
);
CREATE TABLE IF NOT EXISTS videos (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    entry_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('movie', 'episode', 'other')),
    show_id TEXT REFERENCES shows(id) ON DELETE CASCADE,
    season INTEGER NOT NULL DEFAULT 0,
    episode INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL,
    episode_title TEXT NOT NULL DEFAULT '',
    original_title TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    plot TEXT NOT NULL DEFAULT '',
    genres TEXT NOT NULL DEFAULT '[]',
    rating REAL NOT NULL DEFAULT 0,
    collection TEXT NOT NULL DEFAULT '',
    root_entry TEXT NOT NULL,
    folder TEXT NOT NULL DEFAULT '',
    file_name TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    modified_at TEXT NOT NULL,
    added_at TEXT NOT NULL,
    poster_entry TEXT NOT NULL DEFAULT '',
    backdrop_entry TEXT NOT NULL DEFAULT '',
    thumb_entry TEXT NOT NULL DEFAULT '',
    nfo_entry TEXT NOT NULL DEFAULT '',
    nfo_modified TEXT NOT NULL DEFAULT '',
    nfo TEXT NOT NULL DEFAULT '',
    subtitles TEXT NOT NULL DEFAULT '[]',
    probe_state TEXT NOT NULL DEFAULT 'pending' CHECK (probe_state IN ('pending', 'ready', 'failed')),
    probe_error TEXT NOT NULL DEFAULT '',
    probe TEXT NOT NULL DEFAULT '{}',
    duration REAL NOT NULL DEFAULT 0,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    hdr TEXT NOT NULL DEFAULT '',
    frame_state TEXT NOT NULL DEFAULT 'pending' CHECK (frame_state IN ('pending', 'ready', 'failed', 'none')),
    UNIQUE (library_id, entry_id)
);
CREATE INDEX IF NOT EXISTS videos_show ON videos(show_id, season, episode);
CREATE INDEX IF NOT EXISTS videos_pending ON videos(library_id, probe_state, frame_state);
CREATE TABLE IF NOT EXISTS progress (
    user_id TEXT NOT NULL,
    video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    position REAL NOT NULL DEFAULT 0,
    duration REAL NOT NULL DEFAULT 0,
    watched INTEGER NOT NULL DEFAULT 0,
    played_at TEXT NOT NULL DEFAULT '',
    in_history INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, video_id)
);
CREATE INDEX IF NOT EXISTS progress_history ON progress(user_id, in_history, played_at);
CREATE TABLE IF NOT EXISTS favorites (
    user_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, target_id)
);
CREATE TABLE IF NOT EXISTS collections (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS collection_members (
    collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (collection_id, target_id)
);
-- Favorites and collections point at videos or shows, so they are cleaned
-- up by trigger rather than by foreign key.
CREATE TRIGGER IF NOT EXISTS videos_forget AFTER DELETE ON videos BEGIN
    DELETE FROM favorites WHERE target_id = OLD.id;
    DELETE FROM collection_members WHERE target_id = OLD.id;
END;
CREATE TRIGGER IF NOT EXISTS shows_forget AFTER DELETE ON shows BEGIN
    DELETE FROM favorites WHERE target_id = OLD.id;
    DELETE FROM collection_members WHERE target_id = OLD.id;
END;
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
