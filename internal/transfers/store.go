package transfers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// store keeps every user's transfers in transfers.db on the system disk.
// Running transfers are written when their state changes and every few
// seconds while bytes move, so a restart finds them with recent progress.
type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	const schema = `
CREATE TABLE IF NOT EXISTS transfers (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('upload', 'download')),
    kind TEXT NOT NULL CHECK (kind IN ('file', 'archive')),
    name TEXT NOT NULL,
    location TEXT NOT NULL,
    entries TEXT NOT NULL DEFAULT '[]',
    total_bytes INTEGER NOT NULL,
    transferred_bytes INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    batch TEXT NOT NULL DEFAULT '',
    relative_path TEXT NOT NULL DEFAULT '',
    last_modified INTEGER NOT NULL DEFAULT 0,
    entry_id TEXT NOT NULL DEFAULT '',
    saved_name TEXT NOT NULL DEFAULT '',
    replaced INTEGER NOT NULL DEFAULT 0,
    skipped INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS transfers_owner ON transfers(owner_id, created_at);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

const transferColumns = `id, direction, kind, name, location, entries, total_bytes, transferred_bytes, state,
error_code, error_message, batch, relative_path, last_modified, entry_id, saved_name, replaced, skipped,
created_at, updated_at, finished_at`

func (s *store) save(ctx context.Context, ownerID string, t Transfer) error {
	location, err := json.Marshal(t.Location)
	if err != nil {
		return err
	}
	entries, err := json.Marshal(t.entries)
	if err != nil {
		return err
	}
	var code, message, finished string
	if t.Error != nil {
		code, message = t.Error.Code, t.Error.Message
	}
	if t.FinishedAt != nil {
		finished = formatTime(*t.FinishedAt)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO transfers(owner_id, `+transferColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
name = excluded.name, location = excluded.location, entries = excluded.entries, total_bytes = excluded.total_bytes,
transferred_bytes = excluded.transferred_bytes, state = excluded.state, error_code = excluded.error_code,
error_message = excluded.error_message, entry_id = excluded.entry_id, saved_name = excluded.saved_name,
replaced = excluded.replaced, skipped = excluded.skipped, updated_at = excluded.updated_at, finished_at = excluded.finished_at
WHERE owner_id = excluded.owner_id`,
		ownerID, t.ID, t.Direction, t.Kind, t.Name, string(location), string(entries), t.TotalBytes, t.TransferredBytes, t.State,
		code, message, t.Batch, t.RelativePath, t.LastModified, t.EntryID, t.SavedName, t.Replaced, t.Skipped,
		formatTime(t.CreatedAt), formatTime(t.UpdatedAt), finished)
	return err
}

func (s *store) get(ctx context.Context, ownerID, id string) (Transfer, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+transferColumns+" FROM transfers WHERE id = ? AND owner_id = ?", id, ownerID)
	t, err := scanTransfer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	return t, err
}

// list returns the owner's unfinished transfers and the latest finished ones,
// newest first.
func (s *store) list(ctx context.Context, ownerID string, finishedLimit int) ([]Transfer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+transferColumns+` FROM transfers WHERE owner_id = ?1 AND finished_at = ''
UNION ALL
SELECT * FROM (SELECT `+transferColumns+` FROM transfers WHERE owner_id = ?1 AND finished_at <> '' ORDER BY finished_at DESC LIMIT ?2)
ORDER BY created_at DESC`, ownerID, finishedLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		transfers = append(transfers, t)
	}
	return transfers, rows.Err()
}

func (s *store) delete(ctx context.Context, ownerID, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM transfers WHERE id = ? AND owner_id = ? AND finished_at <> ''", id, ownerID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *store) clearFinished(ctx context.Context, ownerID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM transfers WHERE owner_id = ? AND finished_at <> ''", ownerID)
	return err
}

// recover settles what a restart interrupted. Uploads lost the temporary
// file they wrote to and must start again; a browser may still resume an
// interrupted download, whose content address keeps working.
func (s *store) recover(ctx context.Context, now time.Time) error {
	at := formatTime(now)
	if _, err := s.db.ExecContext(ctx, `
UPDATE transfers SET state = 'failed', error_code = 'service_restarted', error_message = 'the service restarted during the upload',
updated_at = ?1, finished_at = ?1 WHERE direction = 'upload' AND finished_at = ''`, at); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE transfers SET state = 'interrupted', updated_at = ? WHERE direction = 'download' AND state = 'running'", at)
	return err
}

// failUnstarted ends downloads that no browser requested in time, for
// example because a pop-up blocker stopped the link.
func (s *store) failUnstarted(ctx context.Context, cutoff, now time.Time) error {
	at := formatTime(now)
	_, err := s.db.ExecContext(ctx, `
UPDATE transfers SET state = 'failed', error_code = 'not_started', error_message = 'the browser did not start the download',
updated_at = ?1, finished_at = ?1 WHERE direction = 'download' AND state = 'pending' AND created_at < ?2`, at, formatTime(cutoff))
	return err
}

// prune drops finished transfers past their retention and all but each
// owner's newest maxFinished.
func (s *store) prune(ctx context.Context, cutoff time.Time, maxFinished int) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM transfers WHERE finished_at <> '' AND finished_at < ?", formatTime(cutoff)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
DELETE FROM transfers WHERE id IN (
    SELECT id FROM (
        SELECT id, ROW_NUMBER() OVER (PARTITION BY owner_id ORDER BY finished_at DESC) AS position
        FROM transfers WHERE finished_at <> ''
    ) WHERE position > ?
)`, maxFinished)
	return err
}

type rowScanner interface{ Scan(...any) error }

func scanTransfer(row rowScanner) (Transfer, error) {
	var t Transfer
	var location, entries, code, message, created, updated, finished string
	if err := row.Scan(&t.ID, &t.Direction, &t.Kind, &t.Name, &location, &entries, &t.TotalBytes, &t.TransferredBytes, &t.State,
		&code, &message, &t.Batch, &t.RelativePath, &t.LastModified, &t.EntryID, &t.SavedName, &t.Replaced, &t.Skipped,
		&created, &updated, &finished); err != nil {
		return Transfer{}, err
	}
	if err := json.Unmarshal([]byte(location), &t.Location); err != nil {
		return Transfer{}, err
	}
	if err := json.Unmarshal([]byte(entries), &t.entries); err != nil {
		return Transfer{}, err
	}
	if code != "" {
		t.Error = &Failure{Code: code, Message: message}
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if finished != "" {
		at, _ := time.Parse(time.RFC3339Nano, finished)
		t.FinishedAt = &at
	}
	return t, nil
}

// formatTime has a fixed width, so stored times sort as text.
func formatTime(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000000000Z") }
