package files

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

type Snapshot struct {
	ID        string    `json:"id"`
	SpaceID   string    `json:"spaceId"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type SnapshotEntry struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshotId"`
	ParentID   string    `json:"parentId,omitempty"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
}

type SnapshotObject struct {
	Key       string    `json:"key"`
	ParentKey string    `json:"parentKey,omitempty"`
	Name      string    `json:"name"`
	Kind      EntryKind `json:"kind"`
	SizeBytes int64     `json:"sizeBytes"`
}

// SnapshotBackend accepts stable space and snapshot identities. Implementations
// own the mapping to Btrfs subvolumes or deterministic development directories.
type SnapshotBackend interface {
	Create(context.Context, string, string) ([]SnapshotObject, error)
	Open(context.Context, string, string) (io.ReadCloser, SnapshotObject, error)
	Delete(context.Context, string, string) error
}

func (s *Service) CreateSnapshot(ctx context.Context, actor accounts.User, spaceID, name string) (Snapshot, error) {
	space, _, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return Snapshot{}, err
	}
	if space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin {
		return Snapshot{}, ErrForbidden
	}
	if !validName(name) {
		return Snapshot{}, ErrInvalidName
	}
	var exists int
	if err := s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots WHERE space_id = ? AND name = ?", spaceID, name).Scan(&exists); err != nil {
		return Snapshot{}, err
	}
	if exists != 0 {
		return Snapshot{}, ErrConflict
	}
	snapshot := Snapshot{ID: s.randomID("snapshot"), SpaceID: spaceID, Name: name, CreatedBy: actor.ID, CreatedAt: s.now().UTC()}
	objects, err := s.snapshots.Create(ctx, spaceID, snapshot.ID)
	if err != nil {
		return Snapshot{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		_ = s.snapshots.Delete(ctx, spaceID, snapshot.ID)
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO snapshots(id, space_id, name, created_by, created_at) VALUES(?,?,?,?,?)",
		snapshot.ID, snapshot.SpaceID, snapshot.Name, snapshot.CreatedBy, formatTime(snapshot.CreatedAt)); err != nil {
		_ = s.snapshots.Delete(ctx, spaceID, snapshot.ID)
		return Snapshot{}, err
	}
	idsByKey := map[string]string{"": ""}
	for _, object := range objects {
		entryID := s.randomID("snapshot-entry")
		parentID, ok := idsByKey[object.ParentKey]
		if !ok {
			_ = s.snapshots.Delete(ctx, spaceID, snapshot.ID)
			return Snapshot{}, errors.New("snapshot backend returned an object before its parent")
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO snapshot_entries(id, snapshot_id, parent_id, name, kind, size_bytes, backend_key)
VALUES(?,?,?,?,?,?,?)`, entryID, snapshot.ID, parentID, object.Name, object.Kind, object.SizeBytes, object.Key); err != nil {
			_ = s.snapshots.Delete(ctx, spaceID, snapshot.ID)
			return Snapshot{}, err
		}
		idsByKey[object.Key] = entryID
	}
	if err := tx.Commit(); err != nil {
		_ = s.snapshots.Delete(ctx, spaceID, snapshot.ID)
		return Snapshot{}, err
	}
	_ = s.audit(ctx, actor.ID, "snapshot.created", snapshot.ID, snapshot.Name)
	return snapshot, nil
}

func (s *Service) ListSnapshots(ctx context.Context, actor accounts.User, spaceID string) ([]Snapshot, error) {
	if _, _, err := s.authorizedSpace(ctx, actor, spaceID, false); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx,
		"SELECT id, space_id, name, created_by, created_at FROM snapshots WHERE space_id = ? ORDER BY created_at DESC", spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []Snapshot
	for rows.Next() {
		var snapshot Snapshot
		var createdAt string
		if err := rows.Scan(&snapshot.ID, &snapshot.SpaceID, &snapshot.Name, &snapshot.CreatedBy, &createdAt); err != nil {
			return nil, err
		}
		snapshot.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Service) ListSnapshotEntries(ctx context.Context, actor accounts.User, snapshotID, parentID string) ([]SnapshotEntry, error) {
	_, _, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, snapshot_id, parent_id, name, kind, size_bytes
FROM snapshot_entries WHERE snapshot_id = ? AND parent_id = ? ORDER BY kind, name COLLATE NOCASE`, snapshotID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []SnapshotEntry
	for rows.Next() {
		var entry SnapshotEntry
		if err := rows.Scan(&entry.ID, &entry.SnapshotID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Service) RestoreSnapshotFile(ctx context.Context, actor accounts.User, snapshotID, snapshotEntryID, targetParentID, targetName string) (Entry, error) {
	snapshot, _, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return Entry{}, err
	}
	var entry SnapshotEntry
	var backendKey string
	err = s.store.db.QueryRowContext(ctx, `
SELECT id, snapshot_id, parent_id, name, kind, size_bytes, backend_key
FROM snapshot_entries WHERE id = ? AND snapshot_id = ?`, snapshotEntryID, snapshotID).Scan(
		&entry.ID, &entry.SnapshotID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &backendKey,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if entry.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	if targetName == "" {
		targetName = entry.Name
	}
	reader, object, err := s.snapshots.Open(ctx, snapshotID, backendKey)
	if err != nil {
		return Entry{}, err
	}
	defer reader.Close()
	if object.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	restored, err := s.Upload(ctx, actor, snapshot.SpaceID, targetParentID, targetName, reader)
	if err == nil {
		_ = s.audit(ctx, actor.ID, "snapshot.file_restored", restored.ID, snapshot.ID)
	}
	return restored, err
}

func (s *Service) DeleteSnapshot(ctx context.Context, actor accounts.User, snapshotID string) error {
	snapshot, space, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return err
	}
	if space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin {
		return ErrForbidden
	}
	if err := s.snapshots.Delete(ctx, snapshot.SpaceID, snapshot.ID); err != nil {
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id = ?", snapshotID); err != nil {
		return err
	}
	return s.audit(ctx, actor.ID, "snapshot.deleted", snapshot.ID, snapshot.Name)
}

func (s *Service) authorizeSnapshot(ctx context.Context, actor accounts.User, snapshotID string) (Snapshot, accounts.Space, error) {
	var snapshot Snapshot
	var createdAt string
	err := s.store.db.QueryRowContext(ctx,
		"SELECT id, space_id, name, created_by, created_at FROM snapshots WHERE id = ?", snapshotID,
	).Scan(&snapshot.ID, &snapshot.SpaceID, &snapshot.Name, &snapshot.CreatedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, accounts.Space{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, accounts.Space{}, err
	}
	snapshot.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	space, _, err := s.authorizedSpace(ctx, actor, snapshot.SpaceID, false)
	return snapshot, space, err
}

type directorySnapshotBackend struct {
	baseRoot string
	resolve  func(string) (string, error)
}

func newDirectorySnapshotBackend(baseRoot string, resolve func(string) (string, error)) *directorySnapshotBackend {
	return &directorySnapshotBackend{baseRoot: baseRoot, resolve: resolve}
}

func (b *directorySnapshotBackend) Create(_ context.Context, spaceID, snapshotID string) ([]SnapshotObject, error) {
	sourceRoot, err := b.resolve(spaceID)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(b.baseRoot, ".a-nas-snapshots")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	targetRoot := filepath.Join(base, safeSegment(snapshotID))
	temporary := targetRoot + ".incoming"
	if err := os.Mkdir(temporary, 0o750); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	var objects []SnapshotObject
	err = filepath.WalkDir(sourceRoot, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}
		if strings.HasPrefix(item.Name(), ".a-nas-") {
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
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(temporary, relative)
		kind := EntryKindFile
		if info.IsDir() {
			kind = EntryKindDirectory
			if err := os.Mkdir(destination, 0o750); err != nil {
				return err
			}
		} else if err := copyRegularFile(path, destination, 0o440); err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		parentKey := filepath.ToSlash(filepath.Dir(relative))
		if parentKey == "." {
			parentKey = ""
		}
		objects = append(objects, SnapshotObject{Key: key, ParentKey: parentKey, Name: info.Name(), Kind: kind, SizeBytes: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, targetRoot); err != nil {
		return nil, err
	}
	committed = true
	return objects, nil
}

func (b *directorySnapshotBackend) Open(_ context.Context, snapshotID, key string) (io.ReadCloser, SnapshotObject, error) {
	root := filepath.Join(b.baseRoot, ".a-nas-snapshots", safeSegment(snapshotID))
	path := filepath.Join(root, filepath.FromSlash(key))
	if !withinRoot(root, path) {
		return nil, SnapshotObject{}, ErrNotFound
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, SnapshotObject{}, ErrNotFound
	}
	if !info.Mode().IsRegular() {
		return nil, SnapshotObject{}, ErrUnsupportedType
	}
	reader, err := os.Open(path)
	if err != nil {
		return nil, SnapshotObject{}, err
	}
	return reader, SnapshotObject{Key: key, Name: info.Name(), Kind: EntryKindFile, SizeBytes: info.Size()}, nil
}

func (b *directorySnapshotBackend) Delete(_ context.Context, _ string, snapshotID string) error {
	root := filepath.Join(b.baseRoot, ".a-nas-snapshots", safeSegment(snapshotID))
	if !withinRoot(filepath.Join(b.baseRoot, ".a-nas-snapshots"), root) {
		return ErrNotFound
	}
	return os.RemoveAll(root)
}

// copyRegularFile copies one file for the development snapshot backend, which
// runs only without a Host Agent.
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
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
