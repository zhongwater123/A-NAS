// Package workspace keeps the shared workspace (ADR 0040): shared pages and
// their versions, the members an administrator lets use it, one-time
// transfer links for agents, and what each member is looking at. The pages
// are members' HTML; this package never runs it, it stores it, finds where
// an element is written in it, and changes it as told.
package workspace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/capacity"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Limits on what members and agents save.
const (
	MaxPageBytes  = 20 << 20
	MaxTitleRunes = 200
	MaxNoteRunes  = 500
	MaxEdits      = 50
	// AuditDays bounds the workspace's audit trail.
	AuditDays = 180
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("not allowed")
	// ErrInvalid wraps what a request got wrong; its message says what.
	ErrInvalid = errors.New("invalid request")
	// ErrTooLarge reports a page over MaxPageBytes.
	ErrTooLarge = errors.New("a page is at most 20 MB")
	// ErrVolumeFull reports a write the data volume's reserve does not admit.
	ErrVolumeFull = errors.New("the data volume has no room left")
)

// PageChangedError refuses a whole page saved over a version that is no
// longer the latest.
type PageChangedError struct {
	Latest VersionInfo
}

func (e *PageChangedError) Error() string {
	return fmt.Sprintf("the page changed: the latest version is v%d", e.Latest.Version)
}

// Actor is the account acting on the workspace, as the Host Agent named it.
type Actor struct {
	AccountID string
	// Name is how the account is shown now, kept with what it saves.
	Name  string
	Admin bool
}

// ViaKind says how a version was saved: from the desktop, or by which kind
// of agent.
type ViaKind string

const (
	ViaWeb        ViaKind = "web"
	ViaClaudeCode ViaKind = "claude-code"
	ViaCodex      ViaKind = "codex"
	ViaOther      ViaKind = "other"
)

// Via is how a version was saved: the desktop, or a member's agent named by
// its token.
type Via struct {
	Kind ViaKind `json:"kind"`
	// Name is the agent token's name; empty from the desktop.
	Name    string `json:"name,omitempty"`
	TokenID string `json:"-"`
}

// Web is a save from the desktop.
var Web = Via{Kind: ViaWeb}

// Options configures a Store.
type Options struct {
	Now func() time.Time
	// Admits checks the data volume's reserve before a write; nil uses
	// capacity.Admits.
	Admits func(string, int64) (bool, error)
}

// Store is the workspace's database in its subvolume and what members are
// looking at, which is kept in memory only.
type Store struct {
	db        *sql.DB
	directory string
	now       func() time.Time
	admits    func(string, int64) (bool, error)
	// writes serializes changes to pages: a change reads the latest version
	// and adds the next one, and the service is this database's only writer.
	writes sync.Mutex
	live   live
}

// Open opens workspace.db in directory, which the caller verified is the
// service's own store on the data volume; Open never creates directory.
func Open(directory string, options Options) (*Store, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("workspace store: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace store %s is not a directory", directory)
	}
	db, err := sqlitedb.Open(filepath.Join(directory, "workspace.db"), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Admits == nil {
		options.Admits = capacity.Admits
	}
	store := &Store{db: db, directory: directory, now: options.Now, admits: options.Admits}
	if err := sqlitedb.Migrate(context.Background(), db, databaseSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

// databaseSchema is workspace.db's; every step has the Down that undoes it
// (ADR 0034).
var databaseSchema = sqlitedb.Schema{Name: "workspace.db", Steps: []sqlitedb.Step{{
	Up: `
CREATE TABLE pages (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_by_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    latest_version INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX pages_updated ON pages(updated_at DESC, id);
CREATE TABLE versions (
    page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    saved_by TEXT NOT NULL,
    saved_by_name TEXT NOT NULL,
    via_kind TEXT NOT NULL CHECK (via_kind IN ('web', 'claude-code', 'codex', 'other')),
    via_name TEXT NOT NULL DEFAULT '',
    token_id TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    changed TEXT NOT NULL DEFAULT '[]',
    saved_at TEXT NOT NULL,
    -- Last: SQLite reads a column after a large value through all of
    -- its overflow pages.
    content BLOB NOT NULL,
    PRIMARY KEY (page_id, version)
);
CREATE TABLE members (
    account_id TEXT PRIMARY KEY,
    added_by TEXT NOT NULL,
    added_at TEXT NOT NULL
);
CREATE TABLE transfers (
    key_hash TEXT PRIMARY KEY,
    direction TEXT NOT NULL CHECK (direction IN ('download', 'upload')),
    page_id TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 0,
    account_id TEXT NOT NULL,
    account_name TEXT NOT NULL,
    token_id TEXT NOT NULL,
    via_kind TEXT NOT NULL,
    via_name TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL
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
` + activity.IndexStatement,
	Down: `
DROP TABLE audit_events;
DROP TABLE transfers;
DROP TABLE members;
DROP TABLE versions;
DROP TABLE pages;
`,
}}}

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

// audit records an action the activity log reads like every module's
// audit_events table.
func (s *Store) audit(ctx context.Context, q queryer, actor Actor, action, resourceType, resourceID, detail string) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?, ?, ?, ?, ?, ?, ?)",
		newID("audit"), actor.AccountID, action, resourceType, resourceID, formatTime(s.now()), detail)
	return err
}

// admit checks the data volume's reserve for size more bytes.
func (s *Store) admit(size int) error {
	ok, err := s.admits(s.directory, int64(size))
	if err != nil {
		return err
	}
	if !ok {
		return ErrVolumeFull
	}
	return nil
}
