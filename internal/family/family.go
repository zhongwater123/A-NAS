// Package family keeps 家庭中心: the family's members and their roles, the
// babies' profiles and everything recorded in their channels. It runs inside
// the family service (ADR 0029), which alone holds family.db and the
// recordings on the data volume. Photos and videos stay in the shared photo
// library: a channel stores only their asset IDs and asks Photos, as the
// person reading, which of them that person can still see.
package family

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

var (
	// ErrNotMember means the caller has not joined the family, so the
	// channels are closed to them.
	ErrNotMember = errors.New("not a member of the family")
	// ErrForbidden means the caller is a member but the action belongs to a
	// guardian or to the author.
	ErrForbidden = errors.New("not allowed for this member")
	ErrNotFound  = errors.New("not found")
	ErrInvalid   = errors.New("invalid request")
	// ErrChanged means someone saved the item after the caller read it.
	ErrChanged = errors.New("changed since it was read")
	// ErrConflict means the request does not fit the current state, such as
	// joining twice.
	ErrConflict = errors.New("conflicts with the current state")
	// ErrLastGuardian refuses to leave the family without a guardian.
	ErrLastGuardian = errors.New("the family needs at least one guardian")
	// ErrPhotosUnavailable means the photo service could not answer, so
	// nothing that depends on photos is written.
	ErrPhotosUnavailable = errors.New("photos are unavailable")
)

// TrashDays is how long deleted records and babies stay restorable.
const TrashDays = 15

// Actor is the signed-in account acting on the family.
type Actor struct {
	AccountID string
	Username  string
	// Session is the caller's session token. It is handed only to Photos,
	// so the photo service authorizes every photo read for this caller; it
	// is never stored.
	Session string
}

// Account is an account that may still use the device.
type Account struct {
	ID       string
	Username string
}

type Options struct {
	Now func() time.Time
	// Accounts lists the accounts that are enabled now. Members whose
	// account is gone or disabled keep their content but lose their place:
	// they no longer count as guardians.
	Accounts func(context.Context) (map[string]Account, error)
	// Photos answers which photo assets a caller can see. Without it,
	// photo records cannot be created and none are shown.
	Photos Photos
}

type Store struct {
	db         *sql.DB
	recordings string
	now        func() time.Time
	accounts   func(context.Context) (map[string]Account, error)
	photos     Photos
}

// Open opens the family store in directory, which holds family.db and the
// recordings directory. The caller has verified that directory is on the
// data volume; Open never creates it.
func Open(directory string, options Options) (*Store, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("family store: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("family store %s is not a directory", directory)
	}
	recordings := filepath.Join(directory, "recordings")
	if err := os.MkdirAll(recordings, 0o700); err != nil {
		return nil, err
	}
	db, err := sqlitedb.Open(filepath.Join(directory, "family.db"), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Accounts == nil {
		return nil, errors.New("family store needs an account lookup")
	}
	store := &Store{db: db, recordings: recordings, now: options.Now, accounts: options.Accounts, photos: options.Photos}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrations run in order; user_version records how many have run.
var migrations = []string{`
CREATE TABLE family (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
);
CREATE TABLE members (
    account_id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    role TEXT NOT NULL,
    guardian INTEGER NOT NULL DEFAULT 0,
    joined_at TEXT NOT NULL,
    approved_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE join_requests (
    account_id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    role TEXT NOT NULL,
    requested_at TEXT NOT NULL
);
CREATE TABLE babies (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    nickname TEXT NOT NULL DEFAULT '',
    sex TEXT NOT NULL CHECK (sex IN ('female', 'male')),
    birth_date TEXT NOT NULL,
    avatar BLOB,
    avatar_version INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    deleted_at TEXT,
    deleted_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE records (
    id TEXT PRIMARY KEY,
    baby_id TEXT NOT NULL REFERENCES babies(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    occurred_on TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    author_id TEXT NOT NULL,
    author_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    deleted_by TEXT NOT NULL DEFAULT '',
    request_key TEXT,
    height_mm INTEGER,
    weight_g INTEGER,
    head_mm INTEGER,
    vaccine_code TEXT,
    vaccine_name TEXT,
    dose INTEGER,
    clinic TEXT,
    batch TEXT,
    title TEXT,
    auto_key TEXT,
    recording_type TEXT,
    recording_bytes INTEGER,
    recording_name TEXT
);
CREATE INDEX records_feed ON records(baby_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX records_request ON records(author_id, request_key) WHERE request_key IS NOT NULL;
CREATE UNIQUE INDEX records_auto ON records(baby_id, auto_key) WHERE auto_key IS NOT NULL AND deleted_at IS NULL;
CREATE TABLE record_photos (
    record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    asset_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('album', 'certificate')),
    taken_at TEXT,
    video INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (record_id, asset_id)
);
CREATE INDEX record_photos_taken ON record_photos(taken_at DESC, asset_id);
CREATE TABLE comments (
    id TEXT PRIMARY KEY,
    baby_id TEXT NOT NULL REFERENCES babies(id) ON DELETE CASCADE,
    record_id TEXT REFERENCES records(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    author_id TEXT NOT NULL,
    author_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    request_key TEXT
);
CREATE INDEX comments_record ON comments(record_id, created_at);
CREATE INDEX comments_wall ON comments(baby_id, created_at) WHERE record_id IS NULL;
CREATE UNIQUE INDEX comments_request ON comments(author_id, request_key) WHERE request_key IS NOT NULL;
CREATE TABLE vaccine_plans (
    baby_id TEXT NOT NULL REFERENCES babies(id) ON DELETE CASCADE,
    vaccine_code TEXT NOT NULL,
    PRIMARY KEY (baby_id, vaccine_code)
);
CREATE TABLE hints_seen (
    account_id TEXT NOT NULL,
    hint_key TEXT NOT NULL,
    seen_at TEXT NOT NULL,
    PRIMARY KEY (account_id, hint_key)
);
CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_events_actor ON audit_events(actor_user_id, occurred_at DESC, id DESC);
`}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("family.db is version %d, newer than this program (%d)", version, len(migrations))
	}
	for ; version < len(migrations); version++ {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[version]); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate family.db to version %d: %w", version+1, err)
		}
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := apply(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func newID(prefix string) string { return prefix + "_" + strings.ToLower(rand.Text()) }

func formatTime(value time.Time) string { return sqlitedb.FormatTime(value) }

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func parseOptionalTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseTime(value.String)
	return &parsed
}

// audit records an action in the family's audit trail, which the activity
// log reads like every module's audit_events table.
func (s *Store) audit(ctx context.Context, q queryer, actor Actor, action, resourceID, detail string) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?, ?, ?, ?, ?, ?, ?)",
		newID("audit"), actor.AccountID, action, "family", resourceID, formatTime(s.now()), detail)
	return err
}

// trashCutoff is the deletion time before which trashed items are gone.
func (s *Store) trashCutoff() string {
	return formatTime(s.now().AddDate(0, 0, -TrashDays))
}
