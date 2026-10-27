package music

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Store is the music library's SQLite database in the Product Service state
// directory. Track rows are derived from files and can be rebuilt by a scan;
// favorites, play history, playlists and music folders are user data.
type Store struct {
	db *sql.DB
}

func OpenSQLite(path string) (*Store, error) {
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS tracks (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL,
    dir TEXT NOT NULL,
    name TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    modified_at TEXT NOT NULL,
    format TEXT NOT NULL,
    codec TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    artist TEXT NOT NULL,
    album TEXT NOT NULL,
    album_artist TEXT NOT NULL,
    genre TEXT NOT NULL,
    year INTEGER NOT NULL,
    track_no INTEGER NOT NULL,
    disc_no INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    duration_state TEXT NOT NULL DEFAULT 'pending' CHECK (duration_state IN ('pending', 'done', 'failed')),
    embedded_cover TEXT NOT NULL DEFAULT '',
    embedded_lyrics INTEGER NOT NULL DEFAULT 0,
    folder_cover TEXT NOT NULL DEFAULT '',
    lyrics_file TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL,
    sample_rate INTEGER NOT NULL DEFAULT 0,
    bit_depth INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tracks_scope ON tracks(space_id, dir);
CREATE TABLE IF NOT EXISTS folder_images (
    entry_id TEXT PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    modified_at TEXT NOT NULL,
    cover TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS favorites (
    user_id TEXT NOT NULL,
    track_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (user_id, track_id)
);
CREATE TABLE IF NOT EXISTS plays (
    user_id TEXT NOT NULL,
    track_id TEXT NOT NULL,
    played_at TEXT NOT NULL,
    play_count INTEGER NOT NULL,
    PRIMARY KEY (user_id, track_id)
);
CREATE INDEX IF NOT EXISTS plays_recent ON plays(user_id, played_at);
CREATE TABLE IF NOT EXISTS playlists (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS playlist_tracks (
    playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    track_id TEXT NOT NULL,
    PRIMARY KEY (playlist_id, position)
);
CREATE TABLE IF NOT EXISTS sources (
    user_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    space_id TEXT NOT NULL,
    folder_id TEXT NOT NULL,
    path TEXT NOT NULL,
    PRIMARY KEY (user_id, position)
);
CREATE TABLE IF NOT EXISTS settings (
    user_id TEXT PRIMARY KEY,
    custom_sources INTEGER NOT NULL DEFAULT 0
);
`

const trackColumns = `id, space_id, dir, name, size_bytes, modified_at, format, codec, title, artist, album,
album_artist, genre, year, track_no, disc_no, duration_ms, duration_state, embedded_cover, embedded_lyrics,
folder_cover, lyrics_file, added_at, sample_rate, bit_depth`

// row is a track as stored.
type row struct {
	ID, SpaceID, Dir, Name  string
	SizeBytes               int64
	ModifiedAt              time.Time
	Format, Codec           string
	Title, Artist, Album    string
	AlbumArtist, Genre      string
	Year, TrackNo, DiscNo   int
	DurationMs              int64
	DurationState           string
	EmbeddedCover           string
	EmbeddedLyrics          bool
	FolderCover, LyricsFile string
	AddedAt                 time.Time
	SampleRate, BitDepth    int
}

type rowScanner interface{ Scan(...any) error }

func scanRow(scanner rowScanner) (row, error) {
	var value row
	var modifiedAt, addedAt string
	err := scanner.Scan(&value.ID, &value.SpaceID, &value.Dir, &value.Name, &value.SizeBytes, &modifiedAt,
		&value.Format, &value.Codec, &value.Title, &value.Artist, &value.Album, &value.AlbumArtist, &value.Genre,
		&value.Year, &value.TrackNo, &value.DiscNo, &value.DurationMs, &value.DurationState, &value.EmbeddedCover,
		&value.EmbeddedLyrics, &value.FolderCover, &value.LyricsFile, &addedAt, &value.SampleRate, &value.BitDepth)
	value.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modifiedAt)
	value.AddedAt, _ = time.Parse(time.RFC3339Nano, addedAt)
	return value, err
}

func (s *Store) queryRows(ctx context.Context, where string, args ...any) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+trackColumns+" FROM tracks "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []row
	for rows.Next() {
		value, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// scopeClause matches the tracks of one music folder.
func scopeClause(scope scope) (string, []any) {
	if scope.Path == "" {
		return "space_id = ?", []any{scope.SpaceID}
	}
	return "space_id = ? AND (dir = ? OR (dir >= ? AND dir < ?))", []any{scope.SpaceID, scope.Path, scope.Path + "/", scope.Path + "0"}
}

func (s *Store) rowsIn(ctx context.Context, scopes []scope) ([]row, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	clause, args := scopesClause(scopes)
	return s.queryRows(ctx, "WHERE "+clause, args...)
}

func scopesClause(scopes []scope) (string, []any) {
	var clauses []string
	var args []any
	for _, scope := range scopes {
		clause, values := scopeClause(scope)
		clauses = append(clauses, "("+clause+")")
		args = append(args, values...)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

func (s *Store) countIn(ctx context.Context, scopes []scope) (int, error) {
	if len(scopes) == 0 {
		return 0, nil
	}
	clause, args := scopesClause(scopes)
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tracks WHERE "+clause, args...).Scan(&count)
	return count, err
}

// pendingIn returns the tracks whose duration is still to be measured.
func (s *Store) pendingIn(ctx context.Context, scopes []scope) ([]row, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	clause, args := scopesClause(scopes)
	return s.queryRows(ctx, "WHERE duration_state = 'pending' AND "+clause+" ORDER BY space_id, dir, name", args...)
}

// write stores tracks in one transaction.
func (s *Store) write(ctx context.Context, values []row) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, value := range values {
		if err := s.upsert(ctx, tx, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM tracks WHERE id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) rowByID(ctx context.Context, id string) (row, error) {
	value, err := scanRow(s.db.QueryRowContext(ctx, "SELECT "+trackColumns+" FROM tracks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, ErrNotFound
	}
	return value, err
}

func (s *Store) upsert(ctx context.Context, tx *sql.Tx, value row) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO tracks(`+trackColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
space_id=excluded.space_id, dir=excluded.dir, name=excluded.name, size_bytes=excluded.size_bytes,
modified_at=excluded.modified_at, format=excluded.format, codec=excluded.codec, title=excluded.title,
artist=excluded.artist, album=excluded.album, album_artist=excluded.album_artist, genre=excluded.genre,
year=excluded.year, track_no=excluded.track_no, disc_no=excluded.disc_no, duration_ms=excluded.duration_ms,
duration_state=excluded.duration_state, embedded_cover=excluded.embedded_cover,
embedded_lyrics=excluded.embedded_lyrics, folder_cover=excluded.folder_cover, lyrics_file=excluded.lyrics_file,
sample_rate=excluded.sample_rate, bit_depth=excluded.bit_depth`,
		value.ID, value.SpaceID, value.Dir, value.Name, value.SizeBytes, formatTime(value.ModifiedAt), value.Format,
		value.Codec, value.Title, value.Artist, value.Album, value.AlbumArtist, value.Genre, value.Year, value.TrackNo,
		value.DiscNo, value.DurationMs, value.DurationState, value.EmbeddedCover, value.EmbeddedLyrics,
		value.FolderCover, value.LyricsFile, formatTime(value.AddedAt), value.SampleRate, value.BitDepth)
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

var errNoRows = sql.ErrNoRows

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
