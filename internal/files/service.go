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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhongwater123/A-NAS/internal/accounts"
)

var (
	ErrForbidden         = errors.New("file space access is forbidden")
	ErrNotFound          = errors.New("file entry not found")
	ErrConflict          = errors.New("file entry already exists")
	ErrInvalidName       = errors.New("invalid file name")
	ErrUnsupportedType   = errors.New("unsupported file type")
	ErrVolumeUnavailable = errors.New("data volume is unavailable")
	ErrInsufficientSpace = errors.New("data volume reserve would be exhausted")
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
	ID        string    `json:"id"`
	EntryID   string    `json:"entryId"`
	SpaceID   string    `json:"spaceId"`
	Name      string    `json:"name"`
	DeletedBy string    `json:"deletedBy"`
	DeletedAt time.Time `json:"deletedAt"`
}

type AuditEvent struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
}

type Policy interface {
	ListSpaces(context.Context, accounts.User) ([]accounts.Space, error)
	CanAccessSpace(context.Context, accounts.User, string, bool) (bool, error)
}

type trashActorResolver interface {
	UserIDForUsername(context.Context, string) (string, error)
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
`
	_, err := s.db.ExecContext(ctx, schema)
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
	trashRetention         time.Duration
	spaceRootsMu           sync.RWMutex
	spaceRoots             map[string]string
}

func NewService(store *Store, volumeRoot string, policy Policy, options Options) *Service {
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
		spaceRoots: make(map[string]string), trashRetention: options.TrashRetention,
	}
	if service.trashRetention <= 0 {
		service.trashRetention = 30 * 24 * time.Hour
	}
	if options.SnapshotBackend != nil {
		service.snapshots = options.SnapshotBackend
	} else {
		service.snapshots = newDirectorySnapshotBackend(service.volumeRoot, service.registeredSpaceRoot)
	}
	return service
}

func (s *Service) List(ctx context.Context, actor accounts.User, spaceID, parentID string) ([]Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return nil, err
	}
	if err := s.reconcile(ctx, space, root); err != nil {
		return nil, err
	}
	if parentID != "" {
		parent, _, err := s.entryPath(ctx, root, spaceID, parentID)
		if err != nil {
			return nil, err
		}
		if parent.Kind != EntryKindDirectory {
			return nil, ErrNotFound
		}
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at
FROM entries WHERE space_id = ? AND parent_id = ? AND trashed = 0 ORDER BY kind, name COLLATE NOCASE`, spaceID, parentID)
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
		if err := s.reconcile(ctx, space, root); err != nil {
			return err
		}
		if err := s.reconcileSambaTrash(ctx, space, root); err != nil {
			return err
		}
		if err := s.purgeExpiredTrash(ctx, space.ID, root); err != nil {
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
	reader, err := os.Open(path)
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
	entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return TrashItem{}, err
	}
	trashID := s.randomID("trash")
	trashRelative := filepath.ToSlash(filepath.Join(".a-nas-trash", safeSegment(trashID), "content"))
	trashContainer := filepath.Join(root, filepath.FromSlash(filepath.Dir(trashRelative)))
	if err := os.MkdirAll(trashContainer, 0o700); err != nil {
		return TrashItem{}, err
	}
	destination := filepath.Join(root, filepath.FromSlash(trashRelative))
	if err := os.Rename(source, destination); err != nil {
		return TrashItem{}, err
	}
	var oldRelative string
	if err := s.store.db.QueryRowContext(ctx, "SELECT relative_path FROM entries WHERE id = ?", entryID).Scan(&oldRelative); err != nil {
		_ = os.Rename(destination, source)
		return TrashItem{}, err
	}
	now := s.now().UTC()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		_ = os.Rename(destination, source)
		return TrashItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE entries
SET relative_path = ? || substr(relative_path, length(?) + 1), trashed = 1, trash_id = ?
WHERE space_id = ? AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		trashRelative, oldRelative, trashID, spaceID, oldRelative, oldRelative, oldRelative); err != nil {
		_ = os.Rename(destination, source)
		return TrashItem{}, err
	}
	item := TrashItem{ID: trashID, EntryID: entry.ID, SpaceID: spaceID, Name: entry.Name, DeletedBy: actor.ID, DeletedAt: now}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO trash(id, entry_id, space_id, original_parent_id, original_name, trash_relative_path, deleted_by, deleted_at)
VALUES(?,?,?,?,?,?,?,?)`, item.ID, item.EntryID, item.SpaceID, entry.ParentID, entry.Name, trashRelative, item.DeletedBy, formatTime(now)); err != nil {
		_ = os.Rename(destination, source)
		return TrashItem{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Rename(destination, source)
		return TrashItem{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.trashed", entry.ID, entry.Name)
	return item, nil
}

func (s *Service) DeleteByID(ctx context.Context, actor accounts.User, entryID string) (TrashItem, error) {
	var spaceID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryID).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
		return TrashItem{}, ErrNotFound
	} else if err != nil {
		return TrashItem{}, err
	}
	return s.Delete(ctx, actor, spaceID, entryID)
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
		if err := s.reconcileSambaTrash(ctx, space, root); err != nil {
			return nil, err
		}
		if err := s.purgeExpiredTrash(ctx, space.ID, root); err != nil {
			return nil, err
		}
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, entry_id, space_id, original_name, deleted_by, deleted_at FROM trash ORDER BY deleted_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TrashItem
	for rows.Next() {
		var item TrashItem
		var deletedAt string
		if err := rows.Scan(&item.ID, &item.EntryID, &item.SpaceID, &item.Name, &item.DeletedBy, &deletedAt); err != nil {
			return nil, err
		}
		space, ok := visible[item.SpaceID]
		if !ok || (space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin && item.DeletedBy != actor.ID) {
			continue
		}
		item.DeletedAt, _ = time.Parse(time.RFC3339Nano, deletedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) ListAudit(ctx context.Context, actor accounts.User) ([]AuditEvent, error) {
	if actor.Role != accounts.RoleAdmin || actor.Status != accounts.UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, actor_user_id, action, resource_type, resource_id, occurred_at, detail
FROM audit_events ORDER BY occurred_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var occurredAt string
		if err := rows.Scan(&event.ID, &event.ActorUserID, &event.Action, &event.ResourceType, &event.ResourceID, &occurredAt, &event.Detail); err != nil {
			return nil, err
		}
		event.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Service) purgeExpiredTrash(ctx context.Context, spaceID, root string) error {
	cutoff := formatTime(s.now().UTC().Add(-s.trashRetention))
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, entry_id, trash_relative_path, original_name
FROM trash WHERE space_id = ? AND deleted_at <= ?`, spaceID, cutoff)
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
		path := filepath.Join(root, filepath.FromSlash(item.relative))
		if !withinRoot(root, path) {
			return ErrNotFound
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM entries WHERE trash_id = ?", item.id); err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM trash WHERE id = ?", item.id)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = s.audit(ctx, "system", "file.purged.retention", item.entryID, item.name)
	}
	return nil
}

func (s *Service) reconcileSambaTrash(ctx context.Context, space accounts.Space, root string) error {
	resolver, ok := s.policy.(trashActorResolver)
	if !ok {
		return nil
	}
	recycleRoot := filepath.Join(root, ".a-nas-trash")
	users, err := os.ReadDir(recycleRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, userDirectory := range users {
		if !userDirectory.IsDir() || safeSegment(userDirectory.Name()) != userDirectory.Name() {
			continue
		}
		deletedBy, err := resolver.UserIDForUsername(ctx, userDirectory.Name())
		if err != nil {
			continue
		}
		userRoot := filepath.Join(recycleRoot, userDirectory.Name())
		items, err := os.ReadDir(userRoot)
		if err != nil {
			return err
		}
		for _, item := range items {
			path := filepath.Join(userRoot, item.Name())
			trashRelative, relErr := filepath.Rel(root, path)
			if relErr != nil || !withinRoot(root, path) {
				continue
			}
			trashRelative = filepath.ToSlash(trashRelative)
			var count int
			if err := s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM trash WHERE space_id = ? AND trash_relative_path = ?", space.ID, trashRelative).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				continue
			}
			inode, err := inodeOf(info)
			if err != nil {
				return err
			}
			trashID := s.randomID("trash")
			entryID := s.randomID("file")
			kind := EntryKindFile
			if info.IsDir() {
				kind = EntryKindDirectory
			}
			var oldRelative string
			queryErr := s.store.db.QueryRowContext(ctx, "SELECT id, relative_path FROM entries WHERE space_id = ? AND inode = ?", space.ID, inode).Scan(&entryID, &oldRelative)
			tx, err := s.store.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if queryErr == nil {
				_, err = tx.ExecContext(ctx, `UPDATE entries
SET relative_path = ? || substr(relative_path, length(?) + 1), trashed = 1, trash_id = ?
WHERE space_id = ? AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
					trashRelative, oldRelative, trashID, space.ID, oldRelative, oldRelative, oldRelative)
			} else if errors.Is(queryErr, sql.ErrNoRows) {
				_, err = tx.ExecContext(ctx, `INSERT INTO entries
(id, space_id, parent_id, name, kind, relative_path, inode, size_bytes, modified_at, trashed, trash_id)
VALUES(?,?,?,?,?,?,?,?,?,1,?)`, entryID, space.ID, "", info.Name(), kind, trashRelative, inode, info.Size(), formatTime(info.ModTime()), trashID)
			} else {
				err = queryErr
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO trash
(id, entry_id, space_id, original_parent_id, original_name, trash_relative_path, deleted_by, deleted_at)
VALUES(?,?,?,?,?,?,?,?)`, trashID, entryID, space.ID, "", info.Name(), trashRelative, deletedBy, formatTime(s.now().UTC()))
			}
			if err != nil {
				_ = tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			_ = s.audit(ctx, deletedBy, "file.trashed.smb", entryID, item.Name())
		}
	}
	return nil
}

func (s *Service) Restore(ctx context.Context, actor accounts.User, trashID, targetParentID, targetName string) (Entry, error) {
	var item TrashItem
	var originalParentID, trashRelative, deletedAt string
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, entry_id, space_id, original_name, original_parent_id, trash_relative_path, deleted_by, deleted_at
FROM trash WHERE id = ?`, trashID).Scan(
		&item.ID, &item.EntryID, &item.SpaceID, &item.Name, &originalParentID, &trashRelative, &item.DeletedBy, &deletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	item.DeletedAt, _ = time.Parse(time.RFC3339Nano, deletedAt)
	space, root, err := s.authorizedSpace(ctx, actor, item.SpaceID, true)
	if err != nil {
		return Entry{}, err
	}
	if space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin && item.DeletedBy != actor.ID {
		return Entry{}, ErrForbidden
	}
	if targetParentID == "" {
		targetParentID = originalParentID
	}
	if targetName == "" {
		targetName = item.Name
	}
	if !validName(targetName) {
		return Entry{}, ErrInvalidName
	}
	parent, err := s.parentPath(ctx, root, item.SpaceID, targetParentID)
	if err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parent, targetName)
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	source := filepath.Join(root, filepath.FromSlash(trashRelative))
	if !withinRoot(root, source) || !withinRoot(root, target) {
		return Entry{}, ErrNotFound
	}
	if err := os.Rename(source, target); err != nil {
		return Entry{}, err
	}
	newRelative, _ := filepath.Rel(root, target)
	newRelative = filepath.ToSlash(newRelative)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE entries
SET relative_path = ? || substr(relative_path, length(?) + 1), trashed = 0, trash_id = NULL
WHERE trash_id = ?`, newRelative, trashRelative, trashID); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE entries SET parent_id = ?, name = ? WHERE id = ?", targetParentID, targetName, item.EntryID); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM trash WHERE id = ?", trashID); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	_ = os.Remove(filepath.Dir(source))
	_ = s.audit(ctx, actor.ID, "file.restored", item.EntryID, targetName)
	entry, _, err := s.entryPath(ctx, root, item.SpaceID, item.EntryID)
	return entry, err
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
	space, root, err := s.authorizedSpace(ctx, actor, item.SpaceID, true)
	if err != nil {
		return err
	}
	if space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin && item.DeletedBy != actor.ID {
		return ErrForbidden
	}
	trashPath := filepath.Join(root, filepath.FromSlash(trashRelative))
	if !withinRoot(root, trashPath) {
		return ErrNotFound
	}
	if err := os.RemoveAll(trashPath); err != nil {
		return err
	}
	parent := filepath.Dir(trashPath)
	if strings.HasPrefix(filepath.Base(parent), "trash-") {
		_ = os.Remove(parent)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE trash_id = ?", trashID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM trash WHERE id = ?", trashID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.audit(ctx, actor.ID, "file.purged", item.EntryID, item.Name)
}

func (s *Service) Move(ctx context.Context, actor accounts.User, entryID, targetParentID, targetName string) (Entry, error) {
	var spaceID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryID).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	} else if err != nil {
		return Entry{}, err
	}
	_, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return Entry{}, err
	}
	if targetName == "" {
		targetName = entry.Name
	}
	if !validName(targetName) {
		return Entry{}, ErrInvalidName
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
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	var oldRelative string
	if err := s.store.db.QueryRowContext(ctx, "SELECT relative_path FROM entries WHERE id = ?", entryID).Scan(&oldRelative); err != nil {
		return Entry{}, err
	}
	newRelative, _ := filepath.Rel(root, target)
	newRelative = filepath.ToSlash(newRelative)
	if err := os.Rename(source, target); err != nil {
		return Entry{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
UPDATE entries SET relative_path = ? || substr(relative_path, length(?) + 1)
WHERE space_id = ? AND trashed = 0 AND (relative_path = ? OR substr(relative_path, 1, length(?) + 1) = ? || '/')`,
		newRelative, oldRelative, spaceID, oldRelative, oldRelative, oldRelative); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE entries SET parent_id = ?, name = ? WHERE id = ?", targetParentID, targetName, entryID); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Rename(target, source)
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.moved", entryID, targetName)
	moved, _, err := s.entryPath(ctx, root, spaceID, entryID)
	return moved, err
}

func (s *Service) Copy(ctx context.Context, actor accounts.User, entryID, targetParentID, targetName string) (Entry, error) {
	var spaceID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryID).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	} else if err != nil {
		return Entry{}, err
	}
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
	if err != nil {
		return Entry{}, err
	}
	if targetName == "" {
		targetName = entry.Name
	}
	if !validName(targetName) {
		return Entry{}, ErrInvalidName
	}
	parent, err := s.parentPath(ctx, root, spaceID, targetParentID)
	if err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parent, targetName)
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	if err := s.ensureCapacity(root, entry.SizeBytes); err != nil {
		return Entry{}, err
	}
	temporary := filepath.Join(parent, ".a-nas-copy-"+safeSegment(s.randomID("copy")))
	if err := copyTree(source, temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return Entry{}, err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.RemoveAll(temporary)
		return Entry{}, err
	}
	if err := s.reconcile(ctx, space, root); err != nil {
		return Entry{}, err
	}
	relative, _ := filepath.Rel(root, target)
	copied, err := s.entryByRelative(ctx, spaceID, filepath.ToSlash(relative))
	if err == nil {
		_ = s.audit(ctx, actor.ID, "file.copied", copied.ID, copied.Name)
	}
	return copied, err
}

func (s *Service) CreateDirectory(ctx context.Context, actor accounts.User, spaceID, parentID, name string) (Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	if !validName(name) {
		return Entry{}, ErrInvalidName
	}
	parentPath, err := s.parentPath(ctx, root, spaceID, parentID)
	if err != nil {
		return Entry{}, err
	}
	if err := s.ensureCapacity(root, 0); err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parentPath, name)
	if !withinRoot(root, target) {
		return Entry{}, ErrInvalidName
	}
	if err := os.Mkdir(target, 0o770); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Entry{}, ErrConflict
		}
		return Entry{}, err
	}
	entry, err := s.registerPath(ctx, space.ID, parentID, root, target)
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.directory_created", entry.ID, entry.Name)
	return entry, nil
}

func (s *Service) Upload(ctx context.Context, actor accounts.User, spaceID, parentID, name string, contents io.Reader) (Entry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, true)
	if err != nil {
		return Entry{}, err
	}
	if !validName(name) {
		return Entry{}, ErrInvalidName
	}
	parentPath, err := s.parentPath(ctx, root, spaceID, parentID)
	if err != nil {
		return Entry{}, err
	}
	if err := s.ensureCapacity(root, 0); err != nil {
		return Entry{}, err
	}
	target := filepath.Join(parentPath, name)
	if !withinRoot(root, target) {
		return Entry{}, ErrInvalidName
	}
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	temporary, err := os.CreateTemp(parentPath, ".a-nas-upload-")
	if err != nil {
		return Entry{}, err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	buffer := make([]byte, 1024*1024)
	var written int64
	for {
		count, readErr := contents.Read(buffer)
		if count > 0 {
			if _, err := temporary.Write(buffer[:count]); err != nil {
				return Entry{}, err
			}
			written += int64(count)
			if err := s.ensureCapacity(root, written); err != nil {
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
	if err := os.Chmod(temporaryPath, 0o660); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return Entry{}, err
	}
	committed = true
	entry, err := s.registerPath(ctx, space.ID, parentID, root, target)
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "file.uploaded", entry.ID, entry.Name)
	return entry, nil
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
		if err := os.MkdirAll(root, 0o770); err != nil {
			return accounts.Space{}, "", fmt.Errorf("prepare data space: %w", err)
		}
		s.spaceRootsMu.Lock()
		s.spaceRoots[space.ID] = root
		s.spaceRootsMu.Unlock()
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
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at, relative_path
FROM entries WHERE id = ? AND space_id = ? AND trashed = 0`, entryID, spaceID).Scan(
		&entry.ID, &entry.SpaceID, &entry.ParentID, &entry.Name, &entry.Kind,
		&entry.SizeBytes, &modifiedAt, &relativePath,
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
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Entry{}, "", ErrNotFound
	}
	return entry, path, nil
}

func (s *Service) registerPath(ctx context.Context, spaceID, parentID, root, path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return Entry{}, ErrUnsupportedType
	}
	inode, err := inodeOf(info)
	if err != nil {
		return Entry{}, err
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil || relativePath == "." || strings.HasPrefix(relativePath, "..") {
		return Entry{}, ErrNotFound
	}
	kind := EntryKindFile
	if info.IsDir() {
		kind = EntryKindDirectory
	}
	entry := Entry{
		ID: s.randomID("file"), SpaceID: spaceID, ParentID: parentID, Name: info.Name(),
		Kind: kind, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(),
	}
	_, err = s.store.db.ExecContext(ctx, `
INSERT INTO entries(id, space_id, parent_id, name, kind, relative_path, inode, size_bytes, modified_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(space_id, inode) DO UPDATE SET
parent_id=excluded.parent_id, name=excluded.name, kind=excluded.kind,
relative_path=excluded.relative_path, size_bytes=excluded.size_bytes, modified_at=excluded.modified_at`,
		entry.ID, entry.SpaceID, entry.ParentID, entry.Name, entry.Kind, filepath.ToSlash(relativePath), inode,
		entry.SizeBytes, formatTime(entry.ModifiedAt))
	if err != nil {
		return Entry{}, err
	}
	return s.findByInode(ctx, spaceID, inode)
}

func (s *Service) findByInode(ctx context.Context, spaceID string, inode uint64) (Entry, error) {
	row := s.store.db.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at
FROM entries WHERE space_id = ? AND inode = ?`, spaceID, inode)
	return scanEntry(row)
}

func (s *Service) entryByRelative(ctx context.Context, spaceID, relativePath string) (Entry, error) {
	row := s.store.db.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at
FROM entries WHERE space_id = ? AND relative_path = ? AND trashed = 0`, spaceID, relativePath)
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

func (s *Service) reconcile(ctx context.Context, space accounts.Space, root string) error {
	parentIDs := map[string]string{".": ""}
	seen := make(map[uint64]struct{})
	err := filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if item.Name() == ".a-nas-trash" || item.Name() == ".a-nas-snapshots" || strings.HasPrefix(item.Name(), ".a-nas-upload-") {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parentRel := filepath.Dir(relativePath)
		parentID := parentIDs[parentRel]
		entry, err := s.registerPath(ctx, space.ID, parentID, root, path)
		if err != nil {
			return err
		}
		inode, _ := inodeOf(info)
		seen[inode] = struct{}{}
		if info.IsDir() {
			parentIDs[relativePath] = entry.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT inode FROM entries WHERE space_id = ? AND trashed = 0", space.ID)
	if err != nil {
		return err
	}
	var missing []uint64
	for rows.Next() {
		var inode uint64
		if err := rows.Scan(&inode); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := seen[inode]; !ok {
			missing = append(missing, inode)
		}
	}
	_ = rows.Close()
	for _, inode := range missing {
		if _, err := s.store.db.ExecContext(ctx, "DELETE FROM entries WHERE space_id = ? AND inode = ? AND trashed = 0", space.ID, inode); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureCapacity(root string, incoming int64) error {
	if s.disableCapacityReserve {
		return nil
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(root, &stats); err != nil {
		return ErrVolumeUnavailable
	}
	total := uint64(stats.Blocks) * uint64(stats.Bsize)
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	reserve := total / 20
	const minimum = uint64(10 * 1024 * 1024 * 1024)
	if reserve < minimum {
		reserve = minimum
	}
	if uint64(max(incoming, 0)) >= available || available-uint64(max(incoming, 0)) < reserve {
		return ErrInsufficientSpace
	}
	return nil
}

func (s *Service) audit(ctx context.Context, actorID, action, resourceID, detail string) error {
	_, err := s.store.db.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)",
		s.randomID("audit"), actorID, action, "file", resourceID, formatTime(s.now().UTC()), detail)
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

func inodeOf(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem does not expose a stable inode")
	}
	return stat.Ino, nil
}

func copyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return ErrUnsupportedType
	}
	if info.Mode().IsRegular() {
		return copyRegularFile(source, destination, info.Mode().Perm())
	}
	if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
		return err
	}
	children, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := copyTree(filepath.Join(source, child.Name()), filepath.Join(destination, child.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = output.Close()
		if !committed {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	committed = true
	return nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func sortedKeys(values map[uint64]struct{}) []uint64 {
	keys := make([]uint64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
