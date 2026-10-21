package voice

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is voice.db in the Product Service state directory. Transcripts,
// translations and summaries are derived from the recordings and can be made
// again; speaker names, people, their voiceprints, edits to the transcript
// and the recognition service's credentials are user data.
type Store struct {
	db *sql.DB
}

func OpenSQLite(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	// Transcripts are rewritten as a whole, so deleting them should return
	// the space.
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_auto_vacuum=full")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS recordings (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    name TEXT NOT NULL,
    title TEXT NOT NULL,
    title_edited INTEGER NOT NULL DEFAULT 0,
    size_bytes INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    language TEXT NOT NULL DEFAULT '',
    template TEXT NOT NULL DEFAULT '',
    translate_to TEXT NOT NULL DEFAULT '',
    hotwords TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    stage TEXT NOT NULL DEFAULT '',
    waiting TEXT NOT NULL DEFAULT '',
    progress REAL NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    transcode INTEGER NOT NULL DEFAULT 0,
    text_revision INTEGER NOT NULL DEFAULT 0,
    names_revision INTEGER NOT NULL DEFAULT 0,
    voiceprint_model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    processed_at TEXT NOT NULL DEFAULT '',
    UNIQUE (owner_id, file_id)
);
CREATE INDEX IF NOT EXISTS recordings_status ON recordings(status, created_at);
CREATE TABLE IF NOT EXISTS utterances (
    recording_id TEXT NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    start_ms INTEGER NOT NULL,
    end_ms INTEGER NOT NULL,
    speaker TEXT NOT NULL,
    text TEXT NOT NULL,
    edited INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (recording_id, seq)
);
CREATE TABLE IF NOT EXISTS people (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (owner_id, name)
);
CREATE TABLE IF NOT EXISTS speakers (
    recording_id TEXT NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    number INTEGER NOT NULL,
    person_id TEXT REFERENCES people(id) ON DELETE SET NULL,
    assigned TEXT NOT NULL DEFAULT '',
    score REAL NOT NULL DEFAULT 0,
    suggested_id TEXT REFERENCES people(id) ON DELETE SET NULL,
    suggested_score REAL NOT NULL DEFAULT 0,
    speech_ms INTEGER NOT NULL DEFAULT 0,
    profile BLOB,
    profile_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (recording_id, key)
);
CREATE INDEX IF NOT EXISTS speakers_person ON speakers(person_id);
-- A member's "不是他": the speaker is never named after that person again.
CREATE TABLE IF NOT EXISTS rejections (
    recording_id TEXT NOT NULL,
    key TEXT NOT NULL,
    person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    PRIMARY KEY (recording_id, key, person_id),
    FOREIGN KEY (recording_id, key) REFERENCES speakers(recording_id, key) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS translations (
    recording_id TEXT NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    language TEXT NOT NULL,
    status TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    progress REAL NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    lines TEXT NOT NULL DEFAULT '[]',
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (recording_id, language)
);
CREATE TABLE IF NOT EXISTS summaries (
    recording_id TEXT NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    template TEXT NOT NULL,
    status TEXT NOT NULL,
    text_revision INTEGER NOT NULL DEFAULT 0,
    names_revision INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (recording_id, template)
);
-- The recognition service's credentials, which an administrator enters.
CREATE TABLE IF NOT EXISTS recognition_tasks (
    recording_id TEXT NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    piece INTEGER NOT NULL,
    task TEXT NOT NULL,
    submitted_at TEXT NOT NULL,
    PRIMARY KEY (recording_id, piece)
);
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func optionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed := parseTime(value)
	return &parsed
}

func encodeVector(vector []float32) []byte {
	if len(vector) == 0 {
		return nil
	}
	data := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(value))
	}
	return data
}

func decodeVector(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	vector := make([]float32, len(data)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
	}
	return vector
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func failureOf(code, message string) *Failure {
	if code == "" {
		return nil
	}
	return &Failure{Code: code, Message: message}
}

func (s *Store) setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
