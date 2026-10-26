// Package files owns Policy-checked access to ordinary A-NAS file spaces.
package files

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/capacity"
)

var (
	ErrForbidden         = errors.New("file space access is forbidden")
	ErrNotFound          = errors.New("file entry not found")
	ErrConflict          = errors.New("file entry already exists")
	ErrInvalidName       = errors.New("invalid file name")
	ErrUnsupportedType   = errors.New("unsupported file type")
	ErrVolumeUnavailable = errors.New("data volume is unavailable")
	ErrInsufficientSpace = errors.New("data volume reserve would be exhausted")
	ErrInvalidQuery      = errors.New("search text must hold 1 to 100 characters")
	ErrInvalidSchedule   = errors.New("invalid snapshot schedule")
)

type EntryKind string

const (
	EntryKindFile      EntryKind = "file"
	EntryKindDirectory EntryKind = "directory"
)

type Entry struct {
	ID         string    `json:"id"`
	SpaceID    string    `json:"spaceId"`
	ParentID   string    `json:"parentId,omitempty"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Content struct {
	Name        string
	SizeBytes   int64
	ModifiedAt  time.Time
	ContentType string
	Reader      *os.File
}

type TrashItem struct {
	ID      string    `json:"id"`
	EntryID string    `json:"entryId"`
	SpaceID string    `json:"spaceId"`
	Name    string    `json:"name"`
	Kind    EntryKind `json:"kind"`
	// SizeBytes totals the files the item holds, as last indexed.
	SizeBytes int64 `json:"sizeBytes"`
	// OriginalPath is the folder the item was deleted from, relative to the
	// space root and empty for the root. Restoring puts it back there, so a
	// folder that has moved since is named where it is now.
	OriginalPath string `json:"originalPath"`
	// OriginalGone says the original folder no longer exists: restoring then
	// puts the item at the space root, and OriginalPath is where the folder
	// was, or empty when that was not recorded.
	OriginalGone bool      `json:"originalGone"`
	DeletedBy    string    `json:"deletedBy"`
	DeletedAt    time.Time `json:"deletedAt"`
	// PurgeAt is when the recycle bin deletes the item for good.
	PurgeAt time.Time `json:"purgeAt"`
}

type Policy interface {
	ListSpaces(context.Context, accounts.User) ([]accounts.Space, error)
	CanAccessSpace(context.Context, accounts.User, string, bool) (bool, error)
}

// VolumeGuard proves that volumeRoot is the intended mounted data volume before
// any space directory is created or accessed. Production callers must provide
// one; tests must opt in explicitly if they use a temporary ordinary directory.
type VolumeGuard interface {
	Check(context.Context, string, bool) error
}

type BtrfsVolumeGuard struct {
	ExpectedFilesystemUUID func(context.Context) (string, error)
}

func (g BtrfsVolumeGuard) Check(ctx context.Context, volumeRoot string, write bool) error {
	info, err := os.Lstat(volumeRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrVolumeUnavailable
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(volumeRoot, &stats); err != nil || uint64(stats.Type) != 0x9123683e {
		return ErrVolumeUnavailable
	}
	const readOnlyMountFlag = 1
	if write && stats.Flags&readOnlyMountFlag != 0 {
		return ErrVolumeUnavailable
	}
	contents, err := os.ReadFile(filepath.Join(volumeRoot, ".a-nas-volume.json"))
	if err != nil {
		return ErrVolumeUnavailable
	}
	var marker struct {
		FilesystemUUID string `json:"filesystemUuid"`
		FormatVersion  int    `json:"formatVersion"`
	}
	if json.Unmarshal(contents, &marker) != nil || marker.FormatVersion != 1 || strings.TrimSpace(marker.FilesystemUUID) == "" {
		return ErrVolumeUnavailable
	}
	if g.ExpectedFilesystemUUID == nil {
		return ErrVolumeUnavailable
	}
	expected, err := g.ExpectedFilesystemUUID(ctx)
	if err != nil || strings.TrimSpace(expected) == "" || marker.FilesystemUUID != expected {
		return ErrVolumeUnavailable
	}
	return nil
}

type Options struct {
	Now                    func() time.Time
	Random                 io.Reader
	DisableCapacityReserve bool
	AllowUnverifiedVolume  bool
	VolumeGuard            VolumeGuard
	SnapshotBackend        SnapshotBackend
	TrashRetention         time.Duration
	// Location is the time zone snapshot schedules are read in; it defaults
	// to the system's.
	Location *time.Location
}

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
CREATE TABLE IF NOT EXISTS entries (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL,
    parent_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'directory')),
    relative_path TEXT NOT NULL,
    inode INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL,
    modified_at TEXT NOT NULL,
	trashed INTEGER NOT NULL DEFAULT 0,
	trash_id TEXT,
    UNIQUE(space_id, relative_path),
    UNIQUE(space_id, inode)
);
CREATE TABLE IF NOT EXISTS trash (
    id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    original_parent_id TEXT NOT NULL DEFAULT '',
    original_name TEXT NOT NULL,
    trash_relative_path TEXT NOT NULL,
    deleted_by TEXT NOT NULL,
    deleted_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL,
    name TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(space_id, name)
);
CREATE TABLE IF NOT EXISTS snapshot_entries (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    parent_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'directory')),
    size_bytes INTEGER NOT NULL,
    backend_key TEXT NOT NULL,
    UNIQUE(snapshot_id, backend_key)
);
CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS snapshot_schedules (
    space_id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL,
    frequency TEXT NOT NULL CHECK (frequency IN ('hourly', 'daily', 'weekly')),
    minute_of_day INTEGER NOT NULL,
    weekday INTEGER NOT NULL,
    keep INTEGER NOT NULL,
    updated_by TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_run_at TEXT NOT NULL DEFAULT '',
    last_status TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    last_snapshot_id TEXT NOT NULL DEFAULT ''
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Columns that catalogs created by earlier versions lack. Totals of -1 are
	// filled in below from the snapshot's entries.
	for _, column := range []struct{ table, name, definition string }{
		{"snapshots", "origin", "TEXT NOT NULL DEFAULT 'manual'"},
		{"snapshots", "file_count", "INTEGER NOT NULL DEFAULT -1"},
		{"snapshots", "size_bytes", "INTEGER NOT NULL DEFAULT -1"},
		{"snapshot_entries", "modified_at", "TEXT NOT NULL DEFAULT ''"},
		{"trash", "original_path", "TEXT NOT NULL DEFAULT ''"},
		{"snapshot_schedules", "retention", "TEXT NOT NULL DEFAULT 'count'"},
		{"snapshot_schedules", "keep_hourly", "INTEGER NOT NULL DEFAULT 0"},
		{"snapshot_schedules", "keep_daily", "INTEGER NOT NULL DEFAULT 0"},
		{"snapshot_schedules", "keep_weekly", "INTEGER NOT NULL DEFAULT 0"},
		{"snapshot_schedules", "keep_monthly", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(ctx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS snapshots_by_space ON snapshots(space_id, created_at);
CREATE INDEX IF NOT EXISTS entries_by_parent ON entries(space_id, parent_id);
`+activity.IndexStatement+`;
UPDATE trash SET original_path = (SELECT relative_path FROM entries p WHERE p.id = trash.original_parent_id AND p.trashed = 0)
WHERE original_path = '' AND original_parent_id <> ''
    AND EXISTS (SELECT 1 FROM entries p WHERE p.id = trash.original_parent_id AND p.trashed = 0);
UPDATE snapshots SET
    file_count = (SELECT COUNT(*) FROM snapshot_entries e WHERE e.snapshot_id = snapshots.id AND e.kind = 'file'),
    size_bytes = (SELECT COALESCE(SUM(e.size_bytes), 0) FROM snapshot_entries e WHERE e.snapshot_id = snapshots.id AND e.kind = 'file')
WHERE file_count < 0 OR size_bytes < 0;`); err != nil {
		return err
	}
	return s.addAuditSpaces(ctx)
}

// addAuditSpaces records the space of each audit event. Events written before
// the column existed get it from the entry, trash item or snapshot they name;
// what no longer exists stays unknown.
func (s *Store) addAuditSpaces(ctx context.Context) error {
	var present int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('audit_events') WHERE name = 'space_id'").Scan(&present); err != nil {
		return err
	}
	if present > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `ALTER TABLE audit_events ADD COLUMN space_id TEXT NOT NULL DEFAULT '';
UPDATE audit_events SET space_id = COALESCE(
    (SELECT space_id FROM entries WHERE entries.id = audit_events.resource_id),
    (SELECT space_id FROM trash WHERE trash.entry_id = audit_events.resource_id ORDER BY deleted_at DESC LIMIT 1),
    (SELECT space_id FROM snapshots WHERE snapshots.id = audit_events.resource_id),
    '');`)
	return err
}

// ensureColumn adds a column that databases created by earlier versions lack.
func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

type Service struct {
	store                  *Store
	volumeRoot             string
	policy                 Policy
	now                    func() time.Time
	random                 io.Reader
	disableCapacityReserve bool
	allowUnverifiedVolume  bool
	volumeGuard            VolumeGuard
	snapshots              SnapshotBackend
	fs                     FileSystem
	trashRetention         time.Duration
	location               *time.Location
	spaceRootsMu           sync.RWMutex
	spaceRoots             map[string]string
	// scheduledSpaces lets the development snapshot backend find spaces that
	// scheduled snapshots reach before any request prepared them.
	scheduledSpaces map[string]accounts.Space
	// temporaries holds the temporaries of pending uploads and copies by
	// name; reconciliation removes the ones nobody holds.
	temporariesMu sync.Mutex
	temporaries   map[string]struct{}
	// takingSnapshots holds the snapshots being taken. snapshotsMu also
	// orders sweeps against them (see SweepSnapshots).
	snapshotsMu     sync.Mutex
	takingSnapshots map[string]struct{}
	ordersMu        sync.Mutex
	orders          map[string]*spaceOrder
}

// queryer is the catalog or a transaction on it.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NewService serves the spaces beneath volumeRoot through fsys, which
// production makes the File Broker client so that every operation runs as the
// signed-in user (ADR 0008). Without Options.SnapshotBackend snapshots fail
// as unavailable. NewLocalService is the development and test service.
func NewService(store *Store, volumeRoot string, policy Policy, fsys FileSystem, options Options) *Service {
	if fsys == nil {
		panic("files.NewService needs a FileSystem; development and tests use NewLocalService")
	}
	service := newService(store, volumeRoot, policy, fsys, options)
	if options.SnapshotBackend == nil {
		service.snapshots = unconfiguredSnapshots{}
	}
	return service
}

// NewLocalService opens the files beneath volumeRoot in this process and,
// unless Options.SnapshotBackend names another, keeps snapshots as copies in
// a directory there. Development and tests use it; production never does,
// since files must be opened as the signed-in user (ADR 0008).
func NewLocalService(store *Store, volumeRoot string, policy Policy, options Options) *Service {
	service := newService(store, volumeRoot, policy, NewRootFileSystem(volumeRoot, true), options)
	if options.SnapshotBackend == nil {
		service.snapshots = newDirectorySnapshotBackend(service.volumeRoot, service.snapshotSpaceRoot)
	}
	return service
}

func newService(store *Store, volumeRoot string, policy Policy, fsys FileSystem, options Options) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	service := &Service{
		store: store, volumeRoot: filepath.Clean(volumeRoot), policy: policy,
		now: now, random: random, disableCapacityReserve: options.DisableCapacityReserve,
		allowUnverifiedVolume: options.AllowUnverifiedVolume, volumeGuard: options.VolumeGuard,
		spaceRoots: make(map[string]string), trashRetention: options.TrashRetention, fs: fsys, snapshots: options.SnapshotBackend,
		location: options.Location, scheduledSpaces: make(map[string]accounts.Space),
		temporaries: make(map[string]struct{}), takingSnapshots: make(map[string]struct{}),
		orders: make(map[string]*spaceOrder),
	}
	if service.location == nil {
		service.location = time.Local
	}
	if service.trashRetention <= 0 {
		service.trashRetention = 30 * 24 * time.Hour
	}
	return service
}

func (s *Service) List(ctx context.Context, actor accounts.User, spaceID, parentID string) ([]Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return nil, err
	}
	relative := ""
	if parentID != "" {
		if relative, err = s.directoryScope(ctx, space, root, parentID); err != nil {
			return nil, err
		}
	}
	// Only the listed folder's children are reconciled, so listing costs
	// what the folder holds, not what the space holds (#167).
	if _, err := s.reconcile(ctx, spaceID, root, scope{id: parentID, relative: relative, shallow: true}); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at
FROM entries WHERE space_id = ? AND parent_id = ? AND trashed = 0 AND `+placed+` ORDER BY kind, name COLLATE NOCASE`, spaceID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Service) ReconcileVisibleSpaces(ctx context.Context, actor accounts.User) error {
	spaces, err := s.policy.ListSpaces(ctx, actor)
	if err != nil {
		return err
	}
	for _, space := range spaces {
		_, root, err := s.authorizedSpace(ctx, actor, space.ID, false)
		if err != nil {
			return err
		}
		if !s.order(space.ID).wholeFresh(s.now()) {
			if err := s.reconcileWhole(ctx, space, root); err != nil {
				return err
			}
		}
		if err := s.reconcileSambaTrash(ctx, actor, space, root); err != nil {
			return err
		}
		if err := s.purgeExpiredTrash(ctx, actor.ID, space.ID, root); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) OpenContent(ctx context.Context, actor accounts.User, entryID string) (Content, error) {
	var spaceID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryID).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
		return Content{}, ErrNotFound
	} else if err != nil {
		return Content{}, err
	}
	_, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return Content{}, err
	}
	entry, path, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return Content{}, err
	}
	if entry.Kind != EntryKindFile {
		return Content{}, ErrUnsupportedType
	}
	reader, err := s.fs.Open(ctx, path)
	if err != nil {
		return Content{}, err
	}
	return Content{Name: entry.Name, SizeBytes: entry.SizeBytes, ModifiedAt: entry.ModifiedAt, Reader: reader}, nil
}

func (s *Service) Delete(ctx context.Context, actor accounts.User, spaceID, entryID string) (TrashItem, error) {
	_, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return TrashItem{}, err
	}
	done, err := s.change(ctx, spaceID)
	if err != nil {
		return TrashItem{}, err
	}
	defer done()
	entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return TrashItem{}, err
	}
	trashID := s.randomID("trash")
	// Web and Samba recycle share .a-nas-trash/<username>; the Host Agent
	// prepares it with the user's ACL. Creating it here only happens on
	// development volumes without a Host Agent.
	userTrash := filepath.Join(root, ".a-nas-trash", safeSegment(actor.Username))
	if err := s.fs.MkdirAll(ctx, userTrash); err != nil {
		return TrashItem{}, err
	}
	if info, err := s.fs.Lstat(ctx, userTrash); err != nil || info.Kind != KindDirectory {
		return TrashItem{}, ErrUnsupportedType
	}
	trashRelative := path.Join(".a-nas-trash", safeSegment(actor.Username), safeSegment(trashID), "content")
	container := filepath.Join(root, filepath.FromSlash(path.Dir(trashRelative)))
	if err := s.fs.Mkdir(ctx, container); err != nil {
		return TrashItem{}, err
	}
	destination := filepath.Join(root, filepath.FromSlash(trashRelative))
	if err := s.fs.Rename(ctx, source, destination); err != nil {
		_ = s.fs.Remove(context.WithoutCancel(ctx), container)
		return TrashItem{}, err
	}
	rollback := func() {
		cleanup := context.WithoutCancel(ctx)
		if s.fs.Rename(cleanup, destination, source) == nil {
			_ = s.fs.Remove(cleanup, container)
		}
	}
	var oldRelative string
	if err := s.store.db.QueryRowContext(ctx, "SELECT relative_path FROM entries WHERE id = ?", entryID).Scan(&oldRelative); err != nil {
		rollback()
		return TrashItem{}, err
	}
	now := s.now().UTC()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		rollback()
		return TrashItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE entries
SET relative_path = ? || substr(relative_path, length(?) + 1), trashed = 1, trash_id = ?
WHERE space_id = ? AND trashed = 0 AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		trashRelative, oldRelative, trashID, spaceID, oldRelative, oldRelative, oldRelative); err != nil {
		rollback()
		return TrashItem{}, err
	}
	// Earlier items deleted from these folders keep where the folders were.
	if _, err := tx.ExecContext(ctx, `
UPDATE trash SET original_path = (
    SELECT ? || substr(p.relative_path, length(?) + 1) FROM entries p WHERE p.id = trash.original_parent_id)
WHERE original_parent_id IN (SELECT id FROM entries WHERE trash_id = ? AND kind = 'directory')`,
		oldRelative, trashRelative, trashID); err != nil {
		rollback()
		return TrashItem{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO trash(id, entry_id, space_id, original_parent_id, original_path, original_name, trash_relative_path, deleted_by, deleted_at)
VALUES(?,?,?,?,?,?,?,?,?)`, trashID, entry.ID, spaceID, entry.ParentID, folderOf(oldRelative), entry.Name, trashRelative, actor.ID, formatTime(now)); err != nil {
		rollback()
		return TrashItem{}, err
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return TrashItem{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.trashed", spaceID, entry.ID, entry.Name)
	return s.trashItem(ctx, trashID)
}

// folderOf is the folder of a space-relative path, empty for the root.
func folderOf(relative string) string {
	if folder := path.Dir(relative); folder != "." {
		return folder
	}
	return ""
}

func (s *Service) DeleteByID(ctx context.Context, actor accounts.User, entryID string) (TrashItem, error) {
	spaceID, err := s.spaceOf(ctx, entryID)
	if err != nil {
		return TrashItem{}, err
	}
	return s.Delete(ctx, actor, spaceID, entryID)
}

// spaceOf returns the space of an entry that is not in the recycle bin.
func (s *Service) spaceOf(ctx context.Context, entryID string) (string, error) {
	var spaceID string
	err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryID).Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return spaceID, err
}

func (s *Service) ListTrash(ctx context.Context, actor accounts.User) ([]TrashItem, error) {
	spaces, err := s.policy.ListSpaces(ctx, actor)
	if err != nil {
		return nil, ErrForbidden
	}
	visible := make(map[string]accounts.Space, len(spaces))
	for _, space := range spaces {
		visible[space.ID] = space
		_, root, authorizeErr := s.authorizedSpace(ctx, actor, space.ID, false)
		if authorizeErr != nil {
			return nil, authorizeErr
		}
		if err := s.reconcileSambaTrash(ctx, actor, space, root); err != nil {
			return nil, err
		}
		if err := s.purgeExpiredTrash(ctx, actor.ID, space.ID, root); err != nil {
			return nil, err
		}
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+trashItemColumns+" ORDER BY t.deleted_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TrashItem
	for rows.Next() {
		item, err := s.scanTrashItem(rows)
		if err != nil {
			return nil, err
		}
		// Deleted files live in the deleter's own trash directory, which no
		// one else can read (ADR 0008), so only the deleter sees them.
		if _, ok := visible[item.SpaceID]; !ok || item.DeletedBy != actor.ID {
			continue
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// trashItemColumns reads a trash item with its kind, the size of the files
// it holds and the current path of its original folder while that is still
// in the space.
const trashItemColumns = `t.id, t.entry_id, t.space_id, t.original_name, IFNULL(e.kind, 'file'),
    (SELECT IFNULL(SUM(f.size_bytes), 0) FROM entries f WHERE f.trash_id = t.id AND f.kind = 'file'),
    t.original_parent_id, t.original_path, p.relative_path, t.deleted_by, t.deleted_at
FROM trash t
LEFT JOIN entries e ON e.id = t.entry_id
LEFT JOIN entries p ON t.original_parent_id <> '' AND p.id = t.original_parent_id AND p.trashed = 0`

func (s *Service) scanTrashItem(row interface{ Scan(...any) error }) (TrashItem, error) {
	var item TrashItem
	var parentID, recordedPath, deletedAt string
	var livePath sql.NullString
	if err := row.Scan(&item.ID, &item.EntryID, &item.SpaceID, &item.Name, &item.Kind, &item.SizeBytes,
		&parentID, &recordedPath, &livePath, &item.DeletedBy, &deletedAt); err != nil {
		return TrashItem{}, err
	}
	switch {
	case livePath.Valid:
		item.OriginalPath = livePath.String
	case parentID != "" || recordedPath != "":
		// The folder was deleted or purged, or was already gone when SMB
		// recycle reported the file.
		item.OriginalPath, item.OriginalGone = recordedPath, true
	}
	item.DeletedAt, _ = time.Parse(time.RFC3339Nano, deletedAt)
	item.PurgeAt = item.DeletedAt.Add(s.trashRetention)
	return item, nil
}

func (s *Service) trashItem(ctx context.Context, trashID string) (TrashItem, error) {
	item, err := s.scanTrashItem(s.store.db.QueryRowContext(ctx, "SELECT "+trashItemColumns+" WHERE t.id = ?", trashID))
	if errors.Is(err, sql.ErrNoRows) {
		return TrashItem{}, ErrNotFound
	}
	return item, err
}

// purgeExpiredTrash removes the actor's own expired trash items. Items in a
// trash directory the actor cannot reach are left for their owner.
func (s *Service) purgeExpiredTrash(ctx context.Context, actorID, spaceID, root string) error {
	cutoff := formatTime(s.now().UTC().Add(-s.trashRetention))
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, entry_id, trash_relative_path, original_name
FROM trash WHERE space_id = ? AND deleted_by = ? AND deleted_at <= ?`, spaceID, actorID, cutoff)
	if err != nil {
		return err
	}
	type expiredItem struct{ id, entryID, relative, name string }
	var expired []expiredItem
	for rows.Next() {
		var item expiredItem
		if err := rows.Scan(&item.id, &item.entryID, &item.relative, &item.name); err != nil {
			_ = rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range expired {
		trashPath := filepath.Join(root, filepath.FromSlash(item.relative))
		if !withinRoot(root, trashPath) {
			return ErrNotFound
		}
		purged, err := s.purgeTrashContent(ctx, spaceID, root, item.id, item.relative)
		if err != nil {
			return err
		}
		if purged {
			_ = s.audit(ctx, "system", "file.purged.retention", spaceID, item.entryID, item.name)
		}
	}
	return nil
}

// purgeTrashContent removes a recycle-bin item's files and then its rows. It
// reports false, leaving both, when the actor may not remove the files.
func (s *Service) purgeTrashContent(ctx context.Context, spaceID, root, trashID, trashRelative string) (bool, error) {
	done, err := s.change(ctx, spaceID)
	if err != nil {
		return false, err
	}
	defer done()
	err = s.fs.RemoveAll(ctx, filepath.Join(root, filepath.FromSlash(trashRelative)))
	if errors.Is(err, ErrForbidden) || errors.Is(err, fs.ErrPermission) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	s.removeEmptyTrashParents(ctx, root, trashRelative)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE trash_id = ?", trashID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM trash WHERE id = ?", trashID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// reconcileSambaTrash imports files that Samba recycle moved into the actor's
// .a-nas-trash/<username>. recycle:keeptree mirrors the deleted path, so every
// file becomes its own trash item and its original directory is restored
// when it still exists. Web trash items in the same directory are skipped.
func (s *Service) reconcileSambaTrash(ctx context.Context, actor accounts.User, space accounts.Space, root string) error {
	// A Web deletion or restore in flight has moved files that its rows do
	// not record yet; importing them would split a deleted folder into
	// items of its files. The next pass sees them settled.
	order := s.order(space.ID)
	generation, ok := order.observe()
	if !ok {
		return nil
	}
	userRelative := path.Join(".a-nas-trash", safeSegment(actor.Username))
	entries, err := s.fs.Scan(ctx, filepath.Join(root, filepath.FromSlash(userRelative)), ScanSkip{})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrForbidden) {
		return nil
	}
	if err != nil {
		return err
	}
	if !order.beginWrite(generation) {
		return nil
	}
	imported := false
	defer func() { order.endWrite(imported) }()
	known, err := s.trashPaths(ctx, space.ID)
	if err != nil {
		return err
	}
	var knownDirectories []string
	for _, item := range entries {
		trashRelative := userRelative + "/" + item.Path
		if slices.ContainsFunc(knownDirectories, func(prefix string) bool { return strings.HasPrefix(trashRelative, prefix) }) {
			continue
		}
		if known[trashRelative] {
			if item.Info.Kind == KindDirectory {
				knownDirectories = append(knownDirectories, trashRelative+"/")
			}
			continue
		}
		if item.Info.Kind != KindFile {
			continue
		}
		imported = true
		if err := s.importSambaTrashFile(ctx, space.ID, trashRelative, path.Dir(item.Path), actor.ID, item.Info); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) trashPaths(ctx context.Context, spaceID string) (map[string]bool, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT trash_relative_path FROM trash WHERE space_id = ?", spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]bool)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths[path] = true
	}
	return paths, rows.Err()
}

func (s *Service) importSambaTrashFile(ctx context.Context, spaceID, trashRelative, deletedFrom, deletedBy string, info FileInfo) error {
	originalParentID := ""
	if deletedFrom != "." {
		err := s.store.db.QueryRowContext(ctx, `SELECT id FROM entries
WHERE space_id = ? AND relative_path = ? AND kind = ? AND trashed = 0`, spaceID, deletedFrom, EntryKindDirectory).Scan(&originalParentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	trashID := s.randomID("trash")
	entryID := s.randomID("file")
	queryErr := s.store.db.QueryRowContext(ctx, "SELECT id FROM entries WHERE space_id = ? AND inode = ? AND kind = ?",
		spaceID, info.Inode, EntryKindFile).Scan(&entryID)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	switch {
	case queryErr == nil:
		_, err = tx.ExecContext(ctx, "UPDATE entries SET relative_path = ?, trashed = 1, trash_id = ? WHERE id = ?",
			trashRelative, trashID, entryID)
	case errors.Is(queryErr, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO entries
(id, space_id, parent_id, name, kind, relative_path, inode, size_bytes, modified_at, trashed, trash_id)
VALUES(?,?,?,?,?,?,?,?,?,1,?)`, entryID, spaceID, "", info.Name, EntryKindFile, trashRelative, info.Inode, info.Size, formatTime(info.ModTime), trashID)
	default:
		err = queryErr
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO trash
(id, entry_id, space_id, original_parent_id, original_path, original_name, trash_relative_path, deleted_by, deleted_at)
VALUES(?,?,?,?,?,?,?,?,?)`, trashID, entryID, spaceID, originalParentID, folderOf(path.Join(deletedFrom, info.Name)), info.Name, trashRelative, deletedBy, formatTime(s.now().UTC())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = s.audit(ctx, deletedBy, "file.trashed.smb", spaceID, entryID, info.Name)
	return nil
}

// removeEmptyTrashParents removes directories left empty after a trash item
// is restored or purged, but never .a-nas-trash or a per-user trash
// directory: Samba recycle needs the latter to keep the Host Agent's ACL.
func (s *Service) removeEmptyTrashParents(ctx context.Context, root, trashRelative string) {
	parts := strings.Split(trashRelative, "/")
	if len(parts) < 3 || parts[0] != ".a-nas-trash" {
		return
	}
	keep := 2 // .a-nas-trash/<username>
	if len(parts[1]) > 32 {
		keep = 1 // rc.5 Web layout: .a-nas-trash/<trash-id>/content
	}
	cleanup := context.WithoutCancel(ctx)
	for depth := len(parts) - 1; depth > keep; depth-- {
		if s.fs.Remove(cleanup, filepath.Join(root, filepath.FromSlash(strings.Join(parts[:depth], "/")))) != nil {
			return
		}
	}
}

// Restored is a recycle-bin item back in its space.
type Restored struct {
	Entry
	// Replaced is set when the item took the place of a file of its name,
	// which went to the recycle bin.
	Replaced bool `json:"replaced,omitempty"`
}

// Restore puts a recycle-bin item back, in its original folder when
// targetParentID is empty, under targetName or its own name. conflict says
// what happens when that name is taken there: ConflictAsk fails with
// ErrConflict; ConflictRename keeps both, the item taking the first free
// "name (恢复).ext", "name (恢复 2).ext"…; ConflictReplace, for a file only,
// sends the file of that name to the recycle bin first. A folder in the way
// is never replaced: the file then comes back beside it, as with uploads.
func (s *Service) Restore(ctx context.Context, actor accounts.User, trashID, targetParentID, targetName string, conflict ConflictPolicy) (Restored, error) {
	switch conflict {
	case "":
		conflict = ConflictAsk
	case ConflictAsk, ConflictRename, ConflictReplace:
	default:
		return Restored{}, ErrInvalidName
	}
	var item TrashItem
	var originalParentID, trashRelative, deletedAt string
	err := s.store.db.QueryRowContext(ctx, `
SELECT t.id, t.entry_id, t.space_id, t.original_name, IFNULL(e.kind, 'file'), t.original_parent_id, t.trash_relative_path, t.deleted_by, t.deleted_at
FROM trash t LEFT JOIN entries e ON e.id = t.entry_id WHERE t.id = ?`, trashID).Scan(
		&item.ID, &item.EntryID, &item.SpaceID, &item.Name, &item.Kind, &originalParentID, &trashRelative, &item.DeletedBy, &deletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Restored{}, ErrNotFound
	}
	if err != nil {
		return Restored{}, err
	}
	item.DeletedAt, _ = time.Parse(time.RFC3339Nano, deletedAt)
	if conflict == ConflictReplace && item.Kind != EntryKindFile {
		return Restored{}, ErrUnsupportedType
	}
	_, root, err := s.authorizedSpace(ctx, actor, item.SpaceID, true)
	if err != nil {
		return Restored{}, err
	}
	if item.DeletedBy != actor.ID {
		return Restored{}, ErrForbidden
	}
	done, err := s.change(ctx, item.SpaceID)
	if err != nil {
		return Restored{}, err
	}
	defer done()
	toOriginal := targetParentID == ""
	if toOriginal {
		targetParentID = originalParentID
	}
	if targetName == "" {
		targetName = item.Name
	}
	if err := checkName(targetName, item.Name); err != nil {
		return Restored{}, err
	}
	parent, err := s.parentPath(ctx, root, item.SpaceID, targetParentID)
	if errors.Is(err, ErrNotFound) && toOriginal {
		// The original folder was deleted or purged since: the item comes
		// back at the space root, as the recycle bin says beforehand.
		targetParentID, parent, err = "", root, nil
	}
	if err != nil {
		return Restored{}, err
	}
	source := filepath.Join(root, filepath.FromSlash(trashRelative))
	if !withinRoot(root, source) || !withinRoot(root, filepath.Join(parent, targetName)) {
		return Restored{}, ErrNotFound
	}
	replaced := false
	if conflict == ConflictReplace {
		existing := targetName
		if twin, found, err := s.caseTwin(ctx, item.SpaceID, targetParentID, targetName, item.EntryID); err != nil {
			return Restored{}, err
		} else if found && twin.Kind == EntryKindFile {
			existing = twin.Name
		}
		if replaced, err = s.trashForReplacement(ctx, actor, item.SpaceID, root, targetParentID, filepath.Join(parent, existing)); err != nil {
			return Restored{}, err
		}
	}
	// Renames never replace, so a name taken in the meantime keeps both
	// unless the caller asked to be told.
	var target string
	for number := 0; ; number++ {
		if number > maxNumberedCopies || (number > 0 && conflict == ConflictAsk) {
			return Restored{}, ErrConflict
		}
		name := restoredName(targetName, item.Kind, number)
		if _, taken, err := s.caseTwin(ctx, item.SpaceID, targetParentID, name, item.EntryID); err != nil {
			return Restored{}, err
		} else if taken {
			continue
		}
		target = filepath.Join(parent, name)
		err := s.fs.Rename(ctx, source, target)
		if err == nil {
			targetName = name
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return Restored{}, err
		}
	}
	rollback := func() { _ = s.fs.Rename(context.WithoutCancel(ctx), target, source) }
	newRelative, _ := filepath.Rel(root, target)
	newRelative = filepath.ToSlash(newRelative)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		rollback()
		return Restored{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// A stale scan of an earlier version could write a row of the item back
	// to its old path in the space (#164), so its path no longer tells where
	// it is in the item. Such rows come back under a placeholder path that
	// the next reconciliation replaces by finding their inode, which keeps
	// their IDs.
	if _, err := tx.ExecContext(ctx, `
UPDATE entries SET relative_path = '.a-nas-stale/' || id, trashed = 0, trash_id = NULL
WHERE trash_id = ?1 AND id <> ?2 AND relative_path <> ?3 AND substr(relative_path, 1, length(?3) + 1) <> ?3 || '/'`,
		trashID, item.EntryID, trashRelative); err != nil {
		rollback()
		return Restored{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE entries
SET relative_path = ? || substr(relative_path, length(?) + 1), trashed = 0, trash_id = NULL
WHERE trash_id = ? AND id <> ?`, newRelative, trashRelative, trashID, item.EntryID); err != nil {
		rollback()
		return Restored{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE entries SET relative_path = ?, parent_id = ?, name = ?, trashed = 0, trash_id = NULL
WHERE id = ? AND trash_id = ?`, newRelative, targetParentID, targetName, item.EntryID, trashID); err != nil {
		rollback()
		return Restored{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM trash WHERE id = ?", trashID); err != nil {
		rollback()
		return Restored{}, err
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return Restored{}, err
	}
	s.removeEmptyTrashParents(ctx, root, trashRelative)
	detail := targetName
	if replaced {
		detail += " (replaced)"
	}
	_ = s.audit(ctx, actor.ID, "file.restored", item.SpaceID, item.EntryID, detail)
	entry, _, err := s.entryPath(ctx, root, item.SpaceID, item.EntryID)
	return Restored{Entry: entry, Replaced: replaced}, err
}

// restoredName is the name a restored item takes beside one already there:
// itself for 0, then "计划 (恢复).txt", "计划 (恢复 2).txt"; a folder's name
// keeps any dot it has.
func restoredName(name string, kind EntryKind, number int) string {
	switch number {
	case 0:
		return name
	case 1:
		return withSuffix(name, " (恢复)", kind == EntryKindFile)
	default:
		return withSuffix(name, fmt.Sprintf(" (恢复 %d)", number), kind == EntryKindFile)
	}
}

func (s *Service) Purge(ctx context.Context, actor accounts.User, trashID string) error {
	var item TrashItem
	var trashRelative, deletedAt string
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, entry_id, space_id, original_name, trash_relative_path, deleted_by, deleted_at
FROM trash WHERE id = ?`, trashID).Scan(
		&item.ID, &item.EntryID, &item.SpaceID, &item.Name, &trashRelative, &item.DeletedBy, &deletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	item.DeletedAt, _ = time.Parse(time.RFC3339Nano, deletedAt)
	_, root, err := s.authorizedSpace(ctx, actor, item.SpaceID, true)
	if err != nil {
		return err
	}
	if item.DeletedBy != actor.ID {
		return ErrForbidden
	}
	trashPath := filepath.Join(root, filepath.FromSlash(trashRelative))
	if !withinRoot(root, trashPath) {
		return ErrNotFound
	}
	if purged, err := s.purgeTrashContent(ctx, item.SpaceID, root, trashID, trashRelative); err != nil {
		return err
	} else if !purged {
		return ErrForbidden
	}
	return s.audit(ctx, actor.ID, "file.purged", item.SpaceID, item.EntryID, item.Name)
}

// Move moves an entry to a folder of its own space or, given targetSpaceID,
// of another space. Spaces are separate Btrfs subvolumes with their own root
// ACLs, so a move between spaces copies the entry, letting the copy inherit
// the target's ACL, and then removes the original.
func (s *Service) Move(ctx context.Context, actor accounts.User, entryID, targetSpaceID, targetParentID, targetName string) (Entry, error) {
	spaceID, err := s.spaceOf(ctx, entryID)
	if err != nil {
		return Entry{}, err
	}
	if targetSpaceID != "" && targetSpaceID != spaceID {
		return s.moveBetweenSpaces(ctx, actor, entryID, spaceID, targetSpaceID, targetParentID, targetName)
	}
	_, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	done, err := s.change(ctx, spaceID)
	if err != nil {
		return Entry{}, err
	}
	defer done()
	entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return Entry{}, err
	}
	if targetName == "" {
		targetName = entry.Name
	}
	if err := checkName(targetName, entry.Name); err != nil {
		return Entry{}, err
	}
	parent, err := s.parentPath(ctx, root, spaceID, targetParentID)
	if err != nil {
		return Entry{}, err
	}
	if entry.Kind == EntryKindDirectory {
		relative, relErr := filepath.Rel(source, parent)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Entry{}, ErrConflict
		}
	}
	target := filepath.Join(parent, targetName)
	if source == target {
		return entry, nil
	}
	if err := s.refuseCaseTwin(ctx, spaceID, targetParentID, targetName, entryID); err != nil {
		return Entry{}, err
	}
	var oldRelative string
	if err := s.store.db.QueryRowContext(ctx, "SELECT relative_path FROM entries WHERE id = ?", entryID).Scan(&oldRelative); err != nil {
		return Entry{}, err
	}
	newRelative, _ := filepath.Rel(root, target)
	newRelative = filepath.ToSlash(newRelative)
	if err := s.fs.Rename(ctx, source, target); errors.Is(err, fs.ErrExist) {
		return Entry{}, ErrConflict
	} else if err != nil {
		return Entry{}, err
	}
	rollback := func() { _ = s.fs.Rename(context.WithoutCancel(ctx), target, source) }
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		rollback()
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE entries SET relative_path = ? || substr(relative_path, length(?) + 1)
WHERE space_id = ? AND trashed = 0 AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		newRelative, oldRelative, spaceID, oldRelative, oldRelative, oldRelative); err != nil {
		rollback()
		return Entry{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE entries SET parent_id = ?, name = ? WHERE id = ?", targetParentID, targetName, entryID); err != nil {
		rollback()
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.moved", spaceID, entryID, targetName)
	moved, _, err := s.entryPath(ctx, root, spaceID, entryID)
	return moved, err
}

// moveBetweenSpaces copies an entry into another space and then removes the
// original, which needs write access to both spaces. The copy gets new IDs.
func (s *Service) moveBetweenSpaces(ctx context.Context, actor accounts.User, entryID, spaceID, targetSpaceID, targetParentID, targetName string) (Entry, error) {
	moved, source, err := s.copyEntry(ctx, actor, entryID, spaceID, targetSpaceID, targetParentID, targetName, true)
	if err != nil {
		return Entry{}, err
	}
	// The copy is complete, so finish the move even if the client has gone.
	// If removing fails part way, the next listing drops what was removed.
	cleanup := context.WithoutCancel(ctx)
	done, err := s.change(cleanup, spaceID)
	if err != nil {
		return Entry{}, err
	}
	defer done()
	var oldRelative string
	if err := s.store.db.QueryRowContext(ctx, "SELECT relative_path FROM entries WHERE id = ?", entryID).Scan(&oldRelative); err != nil {
		return Entry{}, err
	}
	if err := s.fs.RemoveAll(cleanup, source); err != nil {
		return Entry{}, err
	}
	if _, err := s.store.db.ExecContext(cleanup, `
DELETE FROM entries
WHERE space_id = ? AND trashed = 0 AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		spaceID, oldRelative, oldRelative, oldRelative); err != nil {
		return Entry{}, err
	}
	_ = s.audit(cleanup, actor.ID, "file.moved", moved.SpaceID, moved.ID, moved.Name)
	return moved, nil
}

// Copy copies an entry to a folder of its own space or, given targetSpaceID,
// of another space; the copy inherits the target's ACL.
func (s *Service) Copy(ctx context.Context, actor accounts.User, entryID, targetSpaceID, targetParentID, targetName string) (Entry, error) {
	spaceID, err := s.spaceOf(ctx, entryID)
	if err != nil {
		return Entry{}, err
	}
	copied, _, err := s.copyEntry(ctx, actor, entryID, spaceID, targetSpaceID, targetParentID, targetName, false)
	if err == nil {
		_ = s.audit(ctx, actor.ID, "file.copied", copied.SpaceID, copied.ID, copied.Name)
	}
	return copied, err
}

// copyEntry copies an entry into the target space and returns the copy and
// the original's path. Reading the original is enough when the copy goes to
// another space and the original stays.
func (s *Service) copyEntry(ctx context.Context, actor accounts.User, entryID, spaceID, targetSpaceID, targetParentID, targetName string, removingSource bool) (Entry, string, error) {
	if targetSpaceID == "" {
		targetSpaceID = spaceID
	}
	_, sourceRoot, err := s.authorizedSpace(ctx, actor, spaceID, removingSource || targetSpaceID == spaceID)
	if err != nil {
		return Entry{}, "", err
	}
	entry, source, err := s.entryPath(ctx, sourceRoot, spaceID, entryID)
	if err != nil {
		return Entry{}, "", err
	}
	root := sourceRoot
	if targetSpaceID != spaceID {
		if _, root, err = s.authorizedSpace(ctx, actor, targetSpaceID, true); err != nil {
			return Entry{}, "", err
		}
	}
	if targetName == "" {
		targetName = entry.Name
	}
	if err := checkName(targetName, entry.Name); err != nil {
		return Entry{}, "", err
	}
	parent, err := s.parentPath(ctx, root, targetSpaceID, targetParentID)
	if err != nil {
		return Entry{}, "", err
	}
	// The original counts too: a copy beside it may not differ only in case.
	if err := s.refuseCaseTwin(ctx, targetSpaceID, targetParentID, targetName, ""); err != nil {
		return Entry{}, "", err
	}
	if entry.Kind == EntryKindDirectory {
		relative, relErr := filepath.Rel(source, parent)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Entry{}, "", ErrConflict
		}
	}
	target := filepath.Join(parent, targetName)
	if _, err := s.fs.Lstat(ctx, target); err == nil {
		return Entry{}, "", ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Entry{}, "", err
	}
	// A directory's own size says nothing about its contents.
	size, err := s.fs.Usage(ctx, source)
	if err != nil {
		return Entry{}, "", err
	}
	if err := s.ensureCapacity(size); err != nil {
		return Entry{}, "", err
	}
	temporary := filepath.Join(parent, copyTemporaryPrefix+safeSegment(s.randomID("copy")))
	// Held from before it exists until it is renamed or removed; one that a
	// restart leaves behind, reconciliation removes.
	s.holdTemporary(filepath.Base(temporary), true)
	defer s.holdTemporary(filepath.Base(temporary), false)
	if err := s.fs.Copy(ctx, source, temporary); err != nil {
		_ = s.fs.RemoveAll(context.WithoutCancel(ctx), temporary)
		return Entry{}, "", err
	}
	// Reconciliation skips the temporary, so the copy becomes a change only
	// when it takes its name.
	done, err := s.change(ctx, targetSpaceID)
	if err != nil {
		_ = s.fs.RemoveAll(context.WithoutCancel(ctx), temporary)
		return Entry{}, "", err
	}
	defer done()
	if err := s.fs.Rename(ctx, temporary, target); err != nil {
		_ = s.fs.RemoveAll(context.WithoutCancel(ctx), temporary)
		if errors.Is(err, fs.ErrExist) {
			return Entry{}, "", ErrConflict
		}
		return Entry{}, "", err
	}
	copied, err := s.registerTree(ctx, targetSpaceID, targetParentID, root, target)
	return copied, source, err
}

// registerTree catalogs a file or folder that A-NAS has just put at target,
// with everything below it, as reconciliation would.
func (s *Service) registerTree(ctx context.Context, spaceID, parentID, root, target string) (Entry, error) {
	info, err := s.fs.Lstat(ctx, target)
	if err != nil {
		return Entry{}, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return Entry{}, ErrNotFound
	}
	relative = filepath.ToSlash(relative)
	var below []ScanEntry
	if info.Kind == KindDirectory {
		if below, err = s.fs.Scan(ctx, target, reconcileSkip); err != nil {
			return Entry{}, err
		}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := newInode(ctx, tx, spaceID, info.Inode); err != nil {
		return Entry{}, err
	}
	entry, err := s.registerInfo(ctx, tx, root, spaceID, parentID, relative, info)
	if err != nil {
		return Entry{}, err
	}
	if _, _, err := s.registerScan(ctx, tx, root, spaceID, relative, entry.ID, below, nil, true); err != nil {
		return Entry{}, err
	}
	return entry, tx.Commit()
}

func (s *Service) CreateDirectory(ctx context.Context, actor accounts.User, spaceID, parentID, name string) (Entry, error) {
	return s.createDirectory(ctx, actor, spaceID, parentID, name, nameChosen)
}

// createDirectory creates a folder named as naming allows.
func (s *Service) createDirectory(ctx context.Context, actor accounts.User, spaceID, parentID, name string, naming naming) (Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	if err := naming.check(name); err != nil {
		return Entry{}, err
	}
	done, err := s.change(ctx, spaceID)
	if err != nil {
		return Entry{}, err
	}
	defer done()
	parentPath, err := s.parentPath(ctx, root, spaceID, parentID)
	if err != nil {
		return Entry{}, err
	}
	if naming.twins {
		if err := s.refuseCaseTwin(ctx, spaceID, parentID, name, ""); err != nil {
			return Entry{}, err
		}
	}
	if err := s.ensureCapacity(0); err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parentPath, name)
	if !withinRoot(root, target) {
		return Entry{}, ErrInvalidName
	}
	if err := s.fs.Mkdir(ctx, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Entry{}, ErrConflict
		}
		return Entry{}, err
	}
	entry, err := s.registerCreated(ctx, space.ID, parentID, root, target)
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.directory_created", space.ID, entry.ID, entry.Name)
	return entry, nil
}

func (s *Service) Upload(ctx context.Context, actor accounts.User, spaceID, parentID, name string, contents io.Reader) (Entry, error) {
	return s.upload(ctx, actor, spaceID, parentID, name, contents, nameChosen, time.Time{})
}

// upload writes a file named as naming allows. A modTime that is not zero
// becomes the file's modification time, as far as the file system lets it.
func (s *Service) upload(ctx context.Context, actor accounts.User, spaceID, parentID, name string, contents io.Reader, naming naming, modTime time.Time) (Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	if err := naming.check(name); err != nil {
		return Entry{}, err
	}
	parentPath, err := s.parentPath(ctx, root, spaceID, parentID)
	if err != nil {
		return Entry{}, err
	}
	if naming.twins {
		if err := s.refuseCaseTwin(ctx, spaceID, parentID, name, ""); err != nil {
			return Entry{}, err
		}
	}
	if err := s.ensureCapacity(0); err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parentPath, name)
	if !withinRoot(root, target) {
		return Entry{}, ErrInvalidName
	}
	if _, err := s.fs.Lstat(ctx, target); err == nil {
		return Entry{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Entry{}, err
	}
	temporary, temporaryPath, err := s.fs.CreateTemp(ctx, parentPath, uploadTemporaryPrefix)
	if err != nil {
		return Entry{}, err
	}
	s.holdTemporary(filepath.Base(temporaryPath), true)
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = s.fs.Remove(context.WithoutCancel(ctx), temporaryPath)
		}
		s.holdTemporary(filepath.Base(temporaryPath), false)
	}()
	buffer := make([]byte, 1024*1024)
	for {
		count, readErr := contents.Read(buffer)
		if count > 0 {
			// Bytes already written are reflected in the free space, so only
			// the next chunk is checked against the reserve.
			if err := s.ensureCapacity(int64(count)); err != nil {
				return Entry{}, err
			}
			if _, err := temporary.Write(buffer[:count]); err != nil {
				return Entry{}, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Entry{}, readErr
		}
	}
	if err := temporary.Sync(); err != nil {
		return Entry{}, err
	}
	if err := temporary.Close(); err != nil {
		return Entry{}, err
	}
	s.keepModTime(ctx, temporaryPath, modTime)
	// Reconciliation does not catalog the temporary, so the upload becomes a
	// change only when it takes its name.
	done, err := s.change(ctx, space.ID)
	if err != nil {
		return Entry{}, err
	}
	defer done()
	if err := s.fs.Rename(ctx, temporaryPath, target); errors.Is(err, fs.ErrExist) {
		return Entry{}, ErrConflict
	} else if err != nil {
		return Entry{}, err
	}
	committed = true
	entry, err := s.registerCreated(ctx, space.ID, parentID, root, target)
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.uploaded", space.ID, entry.ID, entry.Name)
	return entry, nil
}

// keepModTime gives a restored file the modification time it was saved
// with. It is best effort: the content is what a restore must bring back.
func (s *Service) keepModTime(ctx context.Context, path string, modTime time.Time) {
	if !modTime.IsZero() {
		_ = s.fs.SetModTime(ctx, path, modTime)
	}
}

func (s *Service) authorizedSpace(ctx context.Context, actor accounts.User, spaceID string, write bool) (accounts.Space, string, error) {
	if s.policy == nil {
		return accounts.Space{}, "", ErrForbidden
	}
	allowed, err := s.policy.CanAccessSpace(ctx, actor, spaceID, write)
	if err != nil || !allowed {
		return accounts.Space{}, "", ErrForbidden
	}
	spaces, err := s.policy.ListSpaces(ctx, actor)
	if err != nil {
		return accounts.Space{}, "", err
	}
	for _, space := range spaces {
		if space.ID != spaceID {
			continue
		}
		if !s.allowUnverifiedVolume {
			if s.volumeGuard == nil {
				return accounts.Space{}, "", ErrVolumeUnavailable
			}
			if err := s.volumeGuard.Check(ctx, s.volumeRoot, write); err != nil {
				return accounts.Space{}, "", ErrVolumeUnavailable
			}
		}
		root := s.spaceRoot(space)
		s.spaceRootsMu.RLock()
		prepared := s.spaceRoots[space.ID] == root
		s.spaceRootsMu.RUnlock()
		// On the data volume the Host Agent creates space roots; only a fresh
		// development root needs this. Do it once per space instead of as an
		// extra File Broker request on every operation.
		if !prepared {
			if err := s.fs.MkdirAll(ctx, root); err != nil {
				return accounts.Space{}, "", fmt.Errorf("prepare data space: %w", err)
			}
			s.spaceRootsMu.Lock()
			s.spaceRoots[space.ID] = root
			s.spaceRootsMu.Unlock()
		}
		return space, root, nil
	}
	return accounts.Space{}, "", ErrForbidden
}

func (s *Service) registeredSpaceRoot(spaceID string) (string, error) {
	s.spaceRootsMu.RLock()
	defer s.spaceRootsMu.RUnlock()
	root, ok := s.spaceRoots[spaceID]
	if !ok {
		return "", ErrNotFound
	}
	return root, nil
}

// snapshotSpaceRoot resolves a space for the development snapshot backend.
func (s *Service) snapshotSpaceRoot(spaceID string) (string, error) {
	if root, err := s.registeredSpaceRoot(spaceID); err == nil {
		return root, nil
	}
	s.spaceRootsMu.RLock()
	space, ok := s.scheduledSpaces[spaceID]
	s.spaceRootsMu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	return s.spaceRoot(space), nil
}

func (s *Service) spaceRoot(space accounts.Space) string {
	if space.Kind == accounts.SpaceKindShared {
		return filepath.Join(s.volumeRoot, "spaces", "shared")
	}
	return filepath.Join(s.volumeRoot, "spaces", "private", safeSegment(space.Name))
}

func (s *Service) parentPath(ctx context.Context, root, spaceID, parentID string) (string, error) {
	if parentID == "" {
		return root, nil
	}
	entry, path, err := s.entryPath(ctx, root, spaceID, parentID)
	if err != nil {
		return "", err
	}
	if entry.Kind != EntryKindDirectory {
		return "", ErrNotFound
	}
	return path, nil
}

func (s *Service) entryPath(ctx context.Context, root, spaceID, entryID string) (Entry, string, error) {
	var entry Entry
	var relativePath, modifiedAt string
	var inode uint64
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at, relative_path, inode
FROM entries WHERE id = ? AND space_id = ? AND trashed = 0`, entryID, spaceID).Scan(
		&entry.ID, &entry.SpaceID, &entry.ParentID, &entry.Name, &entry.Kind,
		&entry.SizeBytes, &modifiedAt, &relativePath, &inode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, "", ErrNotFound
	}
	if err != nil {
		return Entry{}, "", err
	}
	entry.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modifiedAt)
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if !withinRoot(root, path) {
		return Entry{}, "", ErrNotFound
	}
	info, err := s.fs.Lstat(ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, "", errMoved
	}
	if err != nil {
		return Entry{}, "", err
	}
	// A row the catalog has not caught up with may name a path that now
	// holds another object, for example a file replaced over SMB.
	if info.Kind == KindSymlink || info.Inode != inode {
		return Entry{}, "", errMoved
	}
	return entry, path, nil
}

// registerCreated catalogs a file or folder that A-NAS has just created at
// path (see newInode).
func (s *Service) registerCreated(ctx context.Context, spaceID, parentID, root, path string) (Entry, error) {
	info, err := s.fs.Lstat(ctx, path)
	if err != nil {
		return Entry{}, err
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil || relativePath == "." || strings.HasPrefix(relativePath, "..") {
		return Entry{}, ErrNotFound
	}
	if err := newInode(ctx, s.store.db, spaceID, info.Inode); err != nil {
		return Entry{}, err
	}
	return s.registerInfo(ctx, s.store.db, root, spaceID, parentID, filepath.ToSlash(relativePath), info)
}

// newInode drops the rows that hold the inode of an object A-NAS has just
// created. Its inode is new, so such a row belongs to a file that is gone and
// whose number the file system gave out again: one deleted outside A-NAS
// that waits, unplaced, for a pass over the whole space, or a recycle-bin row
// whose file was removed outside A-NAS. The new object must not take that
// row's ID, which shares, transfers and libraries may still name.
func newInode(ctx context.Context, q queryer, spaceID string, inode uint64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM entries WHERE space_id = ? AND inode = ?", spaceID, inode)
	return err
}

// errHeldInTrash says that a file found in a space is also, under the same
// inode, in a recycle-bin item, which keeps it.
var errHeldInTrash = fmt.Errorf("%w: the recycle bin holds the same file", ErrConflict)

func (s *Service) registerInfo(ctx context.Context, q queryer, root, spaceID, parentID, relativePath string, info FileInfo) (Entry, error) {
	kind := EntryKindFile
	switch info.Kind {
	case KindFile:
	case KindDirectory:
		kind = EntryKindDirectory
	default:
		return Entry{}, ErrUnsupportedType
	}
	entry := Entry{
		ID: s.randomID("file"), SpaceID: spaceID, ParentID: parentID, Name: info.Name,
		Kind: kind, SizeBytes: info.Size, ModifiedAt: info.ModTime.UTC(),
	}
	// The row of the inode is updated below, so it must not be a recycle-bin
	// row: that would rewrite the item's path and hide the file.
	moved, err := s.releaseTrashedInode(ctx, q, root, spaceID, info.Inode)
	if err != nil {
		return Entry{}, err
	}
	// Nothing keeps its inode and changes between file and folder: the row
	// is of a file that is gone, its number given out again.
	if moved.id != "" && moved.kind != kind {
		if _, err := q.ExecContext(ctx, "DELETE FROM entries WHERE id = ?", moved.id); err != nil {
			return Entry{}, err
		}
		moved = knownRow{}
	}
	// A path now held by a different inode (for example a file replaced over
	// SMB) must release the old row, or UNIQUE(space_id, relative_path) blocks
	// reconciliation forever. The old row is matched again by inode if it
	// still exists elsewhere, and deleted as missing otherwise. A recycle-bin
	// row holds a path of the space only when a stale scan of an earlier
	// version wrote it there; restoring its item puts it right. A folder's
	// rows below the path go with it, so they leave the new folder's paths.
	var heldID string
	var heldKind EntryKind
	var heldTrashed bool
	err = q.QueryRowContext(ctx, "SELECT id, kind, trashed FROM entries WHERE space_id = ? AND relative_path = ? AND inode <> ?",
		spaceID, relativePath, info.Inode).Scan(&heldID, &heldKind, &heldTrashed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Entry{}, err
	default:
		if _, err := q.ExecContext(ctx, "UPDATE entries SET relative_path = ? WHERE id = ?", unplacedPath(heldID), heldID); err != nil {
			return Entry{}, err
		}
		if heldKind == EntryKindDirectory && !heldTrashed {
			if err := movePrefix(ctx, q, spaceID, relativePath, unplacedPath(heldID)); err != nil {
				return Entry{}, err
			}
		}
	}
	if _, err := q.ExecContext(ctx, `
INSERT INTO entries(id, space_id, parent_id, name, kind, relative_path, inode, size_bytes, modified_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(space_id, inode) DO UPDATE SET
parent_id=excluded.parent_id, name=excluded.name, kind=excluded.kind,
relative_path=excluded.relative_path, size_bytes=excluded.size_bytes, modified_at=excluded.modified_at`,
		entry.ID, entry.SpaceID, entry.ParentID, entry.Name, entry.Kind, relativePath, info.Inode,
		entry.SizeBytes, formatTime(entry.ModifiedAt)); err != nil {
		return Entry{}, err
	}
	found, err := s.findByInode(ctx, q, spaceID, info.Inode)
	if err != nil {
		return Entry{}, err
	}
	// A folder renamed or moved outside A-NAS takes its contents along,
	// which a pass that lists only the folder's parent does not reach.
	if moved.kind == EntryKindDirectory && kind == EntryKindDirectory && moved.relativePath != relativePath {
		if err := moveBelow(ctx, q, spaceID, found.ID, moved.relativePath, relativePath); err != nil {
			return Entry{}, err
		}
	}
	return found, nil
}

// releaseTrashedInode returns the row of a file found in a space, when one
// outside the recycle bin has its inode. A recycle-bin row that claims the
// inode but no longer holds it lets the file have it: the item's file was
// removed outside A-NAS and the inode number used again, and the stale row
// goes. While the recycle bin still holds the inode, or the actor cannot
// see whether it does, the row stays and errHeldInTrash is returned.
func (s *Service) releaseTrashedInode(ctx context.Context, q queryer, root, spaceID string, inode uint64) (knownRow, error) {
	var row knownRow
	var trashed bool
	err := q.QueryRowContext(ctx, "SELECT id, kind, relative_path, trashed FROM entries WHERE space_id = ? AND inode = ?",
		spaceID, inode).Scan(&row.id, &row.kind, &row.relativePath, &trashed)
	if errors.Is(err, sql.ErrNoRows) {
		return knownRow{}, nil
	}
	if err != nil || !trashed {
		return row, err
	}
	if strings.HasPrefix(row.relativePath, ".a-nas-trash/") {
		info, err := s.fs.Lstat(ctx, filepath.Join(root, filepath.FromSlash(row.relativePath)))
		if (err == nil && info.Inode == inode) || (err != nil && !errors.Is(err, fs.ErrNotExist)) {
			return knownRow{}, errHeldInTrash
		}
	}
	_, err = q.ExecContext(ctx, "DELETE FROM entries WHERE id = ?", row.id)
	return knownRow{}, err
}

func (s *Service) findByInode(ctx context.Context, q queryer, spaceID string, inode uint64) (Entry, error) {
	row := q.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at
FROM entries WHERE space_id = ? AND inode = ?`, spaceID, inode)
	return scanEntry(row)
}

type rowScanner interface{ Scan(...any) error }

func scanEntry(row rowScanner) (Entry, error) {
	var entry Entry
	var modifiedAt string
	if err := row.Scan(&entry.ID, &entry.SpaceID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &modifiedAt); err != nil {
		return Entry{}, err
	}
	entry.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modifiedAt)
	return entry, nil
}

// copyTemporaryPrefix names the hidden duplicate a copy writes before it
// takes the target's name.
const copyTemporaryPrefix = ".a-nas-copy-"

// reconcileSkip hides A-NAS bookkeeping. Upload temporaries are scanned and
// copy temporaries listed without their contents, but neither is
// catalogued, so that stale ones are found.
var reconcileSkip = ScanSkip{
	Names:  []string{".a-nas-trash", ".a-nas-snapshots", ".a-nas-stale"},
	Opaque: []string{copyTemporaryPrefix},
}

// reconcileNow reconciles for a caller that depends on the result: it waits
// for changes in flight and holds off new ones until it is done.
func (s *Service) reconcileNow(ctx context.Context, space accounts.Space, root string) error {
	return s.scanAlone(ctx, space.ID, func() error {
		scanned, err := s.fs.Scan(ctx, root, reconcileSkip)
		if err != nil {
			return err
		}
		_, err = s.writeScan(ctx, space.ID, root, scope{}, scanned)
		return err
	})
}

// writeScan catalogs a scan of a scope of a space in one transaction,
// writing only the rows that do not already say what the scan found, drops
// or unplaces the rows of what the scope no longer holds (see dropMissing)
// and removes stale upload and copy temporaries. changed reports whether
// it wrote any row.
func (s *Service) writeScan(ctx context.Context, spaceID, root string, sc scope, scanned []ScanEntry) (changed bool, err error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	known, err := scopeRows(ctx, tx, spaceID, sc)
	if err != nil {
		return false, err
	}
	seen, registered, err := s.registerScan(ctx, tx, root, spaceID, sc.relative, sc.id, scanned, known, false)
	if err != nil {
		return false, err
	}
	dropped, err := dropMissing(ctx, tx, spaceID, sc, known, seen)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	var temporaries, copies []ScanEntry
	for _, item := range scopedEntries(sc, scanned) {
		switch {
		case isUploadTemporary(item.Path):
			temporaries = append(temporaries, item)
		case isCopyTemporary(item.Path):
			copies = append(copies, item)
		}
	}
	s.removeStaleUploads(ctx, root, temporaries)
	s.removeStaleCopies(ctx, root, copies)
	return registered > 0 || dropped, nil
}

// registerScan catalogs what a scan found below base (empty for the space
// root), whose row is baseID, and returns the inodes it catalogued and how
// many rows it wrote. Rows in known, by inode, that already say what the
// scan found are left as they are. created says A-NAS has just made all of
// it (see newInode). Upload and copy temporaries are left out, and so is a
// file the recycle bin still holds, with everything below it.
func (s *Service) registerScan(ctx context.Context, q queryer, root, spaceID, base, baseID string, scanned []ScanEntry, known map[uint64]knownRow, created bool) (map[uint64]struct{}, int, error) {
	parentIDs := map[string]string{".": baseID}
	seen := make(map[uint64]struct{})
	written := 0
	for _, item := range scanned {
		if isUploadTemporary(item.Path) || isCopyTemporary(item.Path) || (item.Info.Kind != KindFile && item.Info.Kind != KindDirectory) {
			continue
		}
		parentID, ok := parentIDs[path.Dir(item.Path)]
		if !ok {
			continue
		}
		relativePath := path.Join(base, item.Path)
		id := ""
		if row, ok := known[item.Info.Inode]; ok && row.unchanged(parentID, relativePath, item.Info) {
			id = row.id
		} else {
			if created {
				if err := newInode(ctx, q, spaceID, item.Info.Inode); err != nil {
					return nil, written, err
				}
			}
			entry, err := s.registerInfo(ctx, q, root, spaceID, parentID, relativePath, item.Info)
			if errors.Is(err, errHeldInTrash) {
				continue
			}
			if err != nil {
				return nil, written, err
			}
			id = entry.ID
			written++
		}
		seen[item.Info.Inode] = struct{}{}
		if item.Info.Kind == KindDirectory {
			parentIDs[item.Path] = id
		}
	}
	return seen, written, nil
}

// removeStaleCopies deletes copy temporaries that no copy holds. A copy
// stopped by a restart or a crash leaves a hidden duplicate, which SMB
// would still show. Ones the actor may not delete are left for someone who may.
func (s *Service) removeStaleCopies(ctx context.Context, root string, copies []ScanEntry) {
	for _, item := range copies {
		if s.temporaryHeld(item.Info.Name) {
			continue
		}
		_ = s.fs.RemoveAll(ctx, filepath.Join(root, filepath.FromSlash(item.Path)))
	}
}

func isCopyTemporary(name string) bool {
	return strings.HasPrefix(path.Base(name), copyTemporaryPrefix)
}

// ensureCapacity checks the whole data volume, which the Product Service can
// stat without access to any space.
func (s *Service) ensureCapacity(incoming int64) error {
	if s.disableCapacityReserve {
		return nil
	}
	ok, err := capacity.Admits(s.volumeRoot, incoming)
	if err != nil {
		return ErrVolumeUnavailable
	}
	if !ok {
		return ErrInsufficientSpace
	}
	return nil
}

// audit records an event in the space it happened in, which decides who may
// see its names in the activity log.
func (s *Service) audit(ctx context.Context, actorID, action, spaceID, resourceID, detail string) error {
	_, err := s.store.db.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail, space_id) VALUES(?,?,?,?,?,?,?,?)",
		s.randomID("audit"), actorID, action, "file", resourceID, formatTime(s.now().UTC()), detail, spaceID)
	return err
}

func (s *Service) randomID(prefix string) string {
	value := make([]byte, 16)
	if _, err := io.ReadFull(s.random, value); err != nil {
		panic(err)
	}
	return prefix + ":" + hex.EncodeToString(value)
}

func validName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeSegment(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return "space"
	}
	return builder.String()
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
