package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of songs: each member's row for a song's content,
// and how its analysis stands (docs/architecture/montage-center.md).

// unusedSongs holds for a song row nothing needs. A copy is needed by the
// current project, by the settings captured by an unfinished job, even
// after the project has changed its song, and by any version's job:
// editing a version renders and previews it with the song it was made
// with (#596).
const unusedSongs = `NOT EXISTS (SELECT 1 FROM projects WHERE song_id = songs.id)
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE owner_user_id = songs.owner_user_id AND ` + activeStateQuery + `
		AND json_extract(CASE WHEN json_valid(settings_json) THEN settings_json END, '$.songId') = songs.id)
	AND NOT EXISTS (SELECT 1 FROM versions v JOIN jobs j ON j.id = v.job_id WHERE j.owner_user_id = songs.owner_user_id
		AND json_extract(CASE WHEN json_valid(j.settings_json) THEN j.settings_json END, '$.songId') = songs.id)`

// RequeueStaleSongs queues again, at at, the songs found unusable by an
// analysis version other than version.
func (d *DB) RequeueStaleSongs(ctx context.Context, at time.Time, version string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE songs SET state = 'analyzing', reason = '', waiting = '', attempts = 0, next_try = NULL,
		analysis_json = NULL, loudness = NULL, updated_at = ? WHERE state = 'unusable' AND analysis_version != ?`,
		sqlitedb.FormatTime(at), version)
	return err
}

// NextSong is the content of the earliest song waiting for its analysis
// and due at now; sql.ErrNoRows when there is none.
func (d *DB) NextSong(ctx context.Context, now time.Time) (string, error) {
	var sha string
	err := d.db.QueryRowContext(ctx, `SELECT sha256 FROM songs WHERE state = 'analyzing' AND (next_try IS NULL OR next_try <= ?)
		ORDER BY created_at, id LIMIT 1`, sqlitedb.FormatTime(now)).Scan(&sha)
	return sha, err
}

// KnownStructure is the latest analysis kept for the content sha, and its
// loudness, as kept; sql.ErrNoRows when there is none.
func (d *DB) KnownStructure(ctx context.Context, sha string) (sql.NullString, []byte, error) {
	var text sql.NullString
	var blob []byte
	err := d.db.QueryRowContext(ctx, `SELECT analysis_json, loudness FROM songs WHERE sha256 = ? AND analysis_json IS NOT NULL
		ORDER BY updated_at DESC LIMIT 1`, sha).Scan(&text, &blob)
	return text, blob, err
}

// KeepSongStructure keeps, at at, what the analysis of version found so
// far in every row of the content sha still waiting for it.
func (d *DB) KeepSongStructure(ctx context.Context, sha, analysis string, loudness []byte, version string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE songs SET analysis_json = ?, loudness = ?, analysis_version = ?, updated_at = ?
		WHERE sha256 = ? AND state = 'analyzing'`, analysis, loudness, version, sqlitedb.FormatTime(at), sha)
	return err
}

// FinishSong records, at at, the analysis of version in every row of the
// content sha still waiting for it: the state it leaves them in and why.
func (d *DB) FinishSong(ctx context.Context, sha, state, reason, analysis string, loudness []byte, version string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE songs SET state = ?, reason = ?, waiting = '', next_try = NULL, analysis_json = ?,
			loudness = ?, analysis_version = ?, updated_at = ? WHERE sha256 = ? AND state = 'analyzing'`,
		state, reason, analysis, loudness, version, sqlitedb.FormatTime(at), sha)
	return err
}

// WaitSong leaves the rows of the content sha still waiting for their
// analysis waiting for why, due again at next.
func (d *DB) WaitSong(ctx context.Context, sha, why string, next time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE songs SET waiting = ?, next_try = ?
		WHERE sha256 = ? AND state = 'analyzing'`, why, sqlitedb.FormatTime(next), sha)
	return err
}

// FailSongAttempt counts a failed attempt at analysing the content sha in
// its rows still waiting, and leaves them waiting for why, due again at
// next.
func (tx *Tx) FailSongAttempt(ctx context.Context, sha, why string, next time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE songs SET waiting = ?, attempts = attempts + 1, next_try = ?
			WHERE sha256 = ? AND state = 'analyzing'`, why, sqlitedb.FormatTime(next), sha)
	return err
}

// SongAttempts is the most attempts any row of the content sha still
// waiting counts, 0 when none waits.
func (tx *Tx) SongAttempts(ctx context.Context, sha string) (int, error) {
	var attempts int
	err := tx.tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(attempts), 0) FROM songs WHERE sha256 = ? AND state = 'analyzing'", sha).Scan(&attempts)
	return attempts, err
}

// SongID is owner's row for the content sha; sql.ErrNoRows when there is
// none.
func (tx *Tx) SongID(ctx context.Context, owner, sha string) (string, error) {
	var id string
	err := tx.tx.QueryRowContext(ctx, "SELECT id FROM songs WHERE owner_user_id = ? AND sha256 = ?", owner, sha).Scan(&id)
	return id, err
}

// RetitleSong gives song id title, at at.
func (tx *Tx) RetitleSong(ctx context.Context, id, title string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE songs SET title = ?, updated_at = ? WHERE id = ?", title, sqlitedb.FormatTime(at), id)
	return err
}

// AddSong adds owner's row id for the content sha, made at at and waiting
// for its analysis.
func (tx *Tx) AddSong(ctx context.Context, id, owner, sha, title string, duration float64, contentType string, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO songs(id, owner_user_id, sha256, title, duration, content_type, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 'analyzing', ?, ?)`, id, owner, sha, title, duration, contentType, now, now)
	return err
}

// CopySongAnalysis gives song id the analysis another row of its content
// has finished, the latest; nothing when none has.
func (tx *Tx) CopySongAnalysis(ctx context.Context, id string) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE songs SET (state, reason, analysis_version, analysis_json, loudness) =
		(SELECT state, reason, analysis_version, analysis_json, loudness FROM songs o
		 WHERE o.sha256 = songs.sha256 AND o.id != songs.id AND o.state != 'analyzing' ORDER BY o.updated_at DESC LIMIT 1)
		WHERE id = ? AND EXISTS (SELECT 1 FROM songs o WHERE o.sha256 = songs.sha256 AND o.id != songs.id AND o.state != 'analyzing')`, id)
	return err
}

// ForgetUnusedSongs forgets owner's song rows nothing needs (unusedSongs).
func (d *DB) ForgetUnusedSongs(ctx context.Context, owner string) error {
	return forgetUnusedSongs(ctx, d.db, owner)
}

// ForgetUnusedSongs is DB.ForgetUnusedSongs inside the transaction.
func (tx *Tx) ForgetUnusedSongs(ctx context.Context, owner string) error {
	return forgetUnusedSongs(ctx, tx.tx, owner)
}

func forgetUnusedSongs(ctx context.Context, q queryer, owner string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM songs WHERE owner_user_id = ? AND "+unusedSongs, owner)
	return err
}

// ForgetAllUnusedSongs forgets every song row nothing needs (unusedSongs).
func (d *DB) ForgetAllUnusedSongs(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM songs WHERE "+unusedSongs)
	return err
}

// DeleteOwnerSongs forgets every song row of owner.
func (tx *Tx) DeleteOwnerSongs(ctx context.Context, owner string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM songs WHERE owner_user_id = ?", owner)
	return err
}

// SongContentKept reports whether any song row names the content sha.
func (d *DB) SongContentKept(ctx context.Context, sha string) (bool, error) {
	var used bool
	err := d.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM songs WHERE sha256 = ?)", sha).Scan(&used)
	return used, err
}

// SongRow is a song row as kept, its analysis still encoded (not valid
// while there is none).
type SongRow struct {
	ID, Owner, Sha, Title, ContentType, State, Reason, Waiting, Version string
	Duration                                                            float64
	Analysis                                                            sql.NullString
	Loudness                                                            []byte
}

// Song is song row id; sql.ErrNoRows when there is none.
func (d *DB) Song(ctx context.Context, id string) (SongRow, error) {
	return song(ctx, d.db, id)
}

// Song is DB.Song inside the transaction.
func (tx *Tx) Song(ctx context.Context, id string) (SongRow, error) {
	return song(ctx, tx.tx, id)
}

func song(ctx context.Context, q queryer, id string) (SongRow, error) {
	var row SongRow
	err := q.QueryRowContext(ctx, `SELECT id, owner_user_id, sha256, title, content_type, state, reason, waiting, analysis_version,
		duration, analysis_json, loudness FROM songs WHERE id = ?`, id).
		Scan(&row.ID, &row.Owner, &row.Sha, &row.Title, &row.ContentType, &row.State, &row.Reason, &row.Waiting, &row.Version,
			&row.Duration, &row.Analysis, &row.Loudness)
	return row, err
}
