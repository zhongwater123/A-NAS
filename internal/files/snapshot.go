package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// SnapshotOrigin tells manual snapshots, which only people delete, from
// scheduled ones, which the space's schedule expires.
type SnapshotOrigin string

const (
	SnapshotOriginManual    SnapshotOrigin = "manual"
	SnapshotOriginScheduled SnapshotOrigin = "scheduled"
)

type Snapshot struct {
	ID        string         `json:"id"`
	SpaceID   string         `json:"spaceId"`
	Name      string         `json:"name"`
	Origin    SnapshotOrigin `json:"origin"`
	CreatedBy string         `json:"createdBy"`
	CreatedAt time.Time      `json:"createdAt"`
	// FileCount and SizeBytes total the files the snapshot holds, not the
	// space it takes: Btrfs shares unchanged data with the live space.
	FileCount int64 `json:"fileCount"`
	SizeBytes int64 `json:"sizeBytes"`
}

type SnapshotEntry struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshotId"`
	ParentID   string    `json:"parentId,omitempty"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	// ModifiedAt is unknown for snapshots taken before it was recorded.
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
}

type SnapshotObject struct {
	Key        string    `json:"key"`
	ParentKey  string    `json:"parentKey,omitempty"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
}

// SnapshotContent streams one file out of a snapshot. Unlike Content it
// cannot seek, because the Host Agent streams it in production.
type SnapshotContent struct {
	Name       string
	SizeBytes  int64
	ModifiedAt time.Time
	Reader     io.ReadCloser
}

// SnapshotMatch is one path found by name in a space's snapshots with the
// distinct versions of it they hold, newest first.
type SnapshotMatch struct {
	Path string    `json:"path"`
	Name string    `json:"name"`
	Kind EntryKind `json:"kind"`
	// InSpace reports whether the catalog still lists the path in the space.
	InSpace  bool              `json:"inSpace"`
	Versions []SnapshotVersion `json:"versions"`
}

// SnapshotVersion is one version of a path. The newest snapshot holding it
// serves downloads and restores; Snapshots counts all that hold it.
type SnapshotVersion struct {
	SnapshotID   string         `json:"snapshotId"`
	SnapshotName string         `json:"snapshotName"`
	Origin       SnapshotOrigin `json:"origin"`
	CreatedAt    time.Time      `json:"createdAt"`
	EntryID      string         `json:"entryId"`
	SizeBytes    int64          `json:"sizeBytes"`
	ModifiedAt   time.Time      `json:"modifiedAt,omitzero"`
	Snapshots    int            `json:"snapshots"`
	OldestAt     time.Time      `json:"oldestAt"`
}

// SnapshotBackend accepts stable space and snapshot identities. Implementations
// own the mapping to Btrfs subvolumes or deterministic development directories.
type SnapshotBackend interface {
	Create(context.Context, string, string) ([]SnapshotObject, error)
	Open(context.Context, string, string) (io.ReadCloser, SnapshotObject, error)
	Delete(context.Context, string, string) error
}

const (
	// A search reports at most this many paths, read from at most
	// snapshotSearchRows snapshot entries.
	snapshotSearchPaths = 100
	snapshotSearchRows  = 5000
	snapshotColumns     = "id, space_id, name, origin, created_by, created_at, file_count, size_bytes"
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// storedSnapshotEntry is a snapshot entry with the backend key it is read by.
type storedSnapshotEntry struct {
	SnapshotEntry
	backendKey string
}

func (s *Service) CreateSnapshot(ctx context.Context, actor accounts.User, spaceID, name string) (Snapshot, error) {
	space, _, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return Snapshot{}, err
	}
	if !canManageSnapshots(actor, space) {
		return Snapshot{}, ErrForbidden
	}
	return s.createSnapshot(ctx, spaceID, actor.ID, name, SnapshotOriginManual)
}

// canManageSnapshots lets the owner of a personal space, and administrators
// for the shared space, create, delete, keep and schedule its snapshots.
// Administrative Viewing Mode stays read-only.
func canManageSnapshots(actor accounts.User, space accounts.Space) bool {
	if space.Kind == accounts.SpaceKindShared {
		return actor.Role == accounts.RoleAdmin
	}
	return space.OwnerUserID == actor.ID
}

func (s *Service) createSnapshot(ctx context.Context, spaceID, actorID, name string, origin SnapshotOrigin) (Snapshot, error) {
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
	snapshot := Snapshot{ID: s.randomID("snapshot"), SpaceID: spaceID, Name: name, Origin: origin, CreatedBy: actorID, CreatedAt: s.now().UTC()}
	objects, err := s.snapshots.Create(ctx, spaceID, snapshot.ID)
	if err != nil {
		return Snapshot{}, err
	}
	// The backend has taken the snapshot; remove it again unless the catalog records it.
	committed := false
	defer func() {
		if !committed {
			_ = s.snapshots.Delete(context.WithoutCancel(ctx), spaceID, snapshot.ID)
		}
	}()
	for _, object := range objects {
		if object.Kind == EntryKindFile {
			snapshot.FileCount++
			snapshot.SizeBytes += object.SizeBytes
		}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO snapshots("+snapshotColumns+") VALUES(?,?,?,?,?,?,?,?)",
		snapshot.ID, snapshot.SpaceID, snapshot.Name, snapshot.Origin, snapshot.CreatedBy, formatTime(snapshot.CreatedAt),
		snapshot.FileCount, snapshot.SizeBytes); err != nil {
		return Snapshot{}, err
	}
	insert, err := tx.PrepareContext(ctx, `
INSERT INTO snapshot_entries(id, snapshot_id, parent_id, name, kind, size_bytes, backend_key, modified_at)
VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return Snapshot{}, err
	}
	defer insert.Close()
	idsByKey := map[string]string{"": ""}
	for _, object := range objects {
		parentID, ok := idsByKey[object.ParentKey]
		if !ok {
			return Snapshot{}, errors.New("snapshot backend returned an object before its parent")
		}
		entryID := s.randomID("snapshot-entry")
		modifiedAt := ""
		if !object.ModifiedAt.IsZero() {
			modifiedAt = formatTime(object.ModifiedAt)
		}
		if _, err := insert.ExecContext(ctx, entryID, snapshot.ID, parentID, object.Name, object.Kind, object.SizeBytes, object.Key, modifiedAt); err != nil {
			return Snapshot{}, err
		}
		idsByKey[object.Key] = entryID
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	committed = true
	_ = s.audit(ctx, actorID, "snapshot.created", snapshot.SpaceID, snapshot.ID, snapshot.Name)
	return snapshot, nil
}

func (s *Service) ListSnapshots(ctx context.Context, actor accounts.User, spaceID string) ([]Snapshot, error) {
	if _, _, err := s.authorizedSpace(ctx, actor, spaceID, false); err != nil {
		return nil, err
	}
	return s.spaceSnapshots(ctx, spaceID, "")
}

// spaceSnapshots lists a space's snapshots newest first, all of them or only
// those of one origin.
func (s *Service) spaceSnapshots(ctx context.Context, spaceID string, origin SnapshotOrigin) ([]Snapshot, error) {
	rows, err := s.store.db.QueryContext(ctx,
		"SELECT "+snapshotColumns+" FROM snapshots WHERE space_id = ? AND (? = '' OR origin = ?) ORDER BY created_at DESC", spaceID, origin, origin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func scanSnapshot(row interface{ Scan(...any) error }) (Snapshot, error) {
	var snapshot Snapshot
	var createdAt string
	err := row.Scan(&snapshot.ID, &snapshot.SpaceID, &snapshot.Name, &snapshot.Origin, &snapshot.CreatedBy, &createdAt, &snapshot.FileCount, &snapshot.SizeBytes)
	snapshot.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return snapshot, err
}

func (s *Service) ListSnapshotEntries(ctx context.Context, actor accounts.User, snapshotID, parentID string) ([]SnapshotEntry, error) {
	if _, _, err := s.authorizeSnapshot(ctx, actor, snapshotID); err != nil {
		return nil, err
	}
	children, err := s.snapshotChildren(ctx, snapshotID, parentID)
	if err != nil {
		return nil, err
	}
	entries := make([]SnapshotEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, child.SnapshotEntry)
	}
	return entries, nil
}

// snapshotChildren reads one folder level completely before returning, so a
// caller can write to the single-connection catalog while it walks them.
func (s *Service) snapshotChildren(ctx context.Context, snapshotID, parentID string) ([]storedSnapshotEntry, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, snapshot_id, parent_id, name, kind, size_bytes, modified_at, backend_key
FROM snapshot_entries WHERE snapshot_id = ? AND parent_id = ? ORDER BY kind, name COLLATE NOCASE`, snapshotID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []storedSnapshotEntry
	for rows.Next() {
		entry, err := scanSnapshotEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Service) snapshotEntry(ctx context.Context, snapshotID, entryID string) (storedSnapshotEntry, error) {
	entry, err := scanSnapshotEntry(s.store.db.QueryRowContext(ctx, `
SELECT id, snapshot_id, parent_id, name, kind, size_bytes, modified_at, backend_key
FROM snapshot_entries WHERE id = ? AND snapshot_id = ?`, entryID, snapshotID))
	if errors.Is(err, sql.ErrNoRows) {
		return storedSnapshotEntry{}, ErrNotFound
	}
	return entry, err
}

func scanSnapshotEntry(row interface{ Scan(...any) error }) (storedSnapshotEntry, error) {
	var entry storedSnapshotEntry
	var modifiedAt string
	err := row.Scan(&entry.ID, &entry.SnapshotID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &modifiedAt, &entry.backendKey)
	entry.ModifiedAt = parseOptionalTime(modifiedAt)
	return entry, err
}

func parseOptionalTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// RestoreSnapshotEntry copies a file or a whole folder out of a snapshot into
// the snapshot's space as the user. It never replaces an existing entry; a
// folder restore that fails part way keeps what it already copied.
func (s *Service) RestoreSnapshotEntry(ctx context.Context, actor accounts.User, snapshotID, snapshotEntryID, targetParentID, targetName string) (Entry, error) {
	snapshot, _, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return Entry{}, err
	}
	entry, err := s.snapshotEntry(ctx, snapshotID, snapshotEntryID)
	if err != nil {
		return Entry{}, err
	}
	if targetName == "" {
		targetName = entry.Name
	}
	var restored Entry
	action := "snapshot.file_restored"
	if entry.Kind == EntryKindDirectory {
		action = "snapshot.folder_restored"
		restored, err = s.createDirectory(ctx, actor, snapshot.SpaceID, targetParentID, targetName, keeping(entry.Name))
		if err == nil {
			err = s.restoreSnapshotChildren(ctx, actor, snapshot, entry.ID, restored.ID)
		}
	} else {
		restored, err = s.restoreSnapshotFile(ctx, actor, snapshot, entry.backendKey, targetParentID, targetName, keeping(entry.Name))
	}
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, action, snapshot.SpaceID, restored.ID, snapshot.ID)
	return restored, nil
}

func (s *Service) restoreSnapshotChildren(ctx context.Context, actor accounts.User, snapshot Snapshot, parentEntryID, targetParentID string) error {
	children, err := s.snapshotChildren(ctx, snapshot.ID, parentEntryID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if child.Kind == EntryKindDirectory {
			// The folder was just created; its contents keep their names.
			created, err := s.createDirectory(ctx, actor, snapshot.SpaceID, targetParentID, child.Name, nameKept)
			if err != nil {
				return err
			}
			if err := s.restoreSnapshotChildren(ctx, actor, snapshot, child.ID, created.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.restoreSnapshotFile(ctx, actor, snapshot, child.backendKey, targetParentID, child.Name, nameKept); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreSnapshotFile(ctx context.Context, actor accounts.User, snapshot Snapshot, backendKey, targetParentID, targetName string, naming naming) (Entry, error) {
	reader, object, err := s.snapshots.Open(ctx, snapshot.ID, backendKey)
	if err != nil {
		return Entry{}, err
	}
	defer reader.Close()
	if object.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	return s.upload(ctx, actor, snapshot.SpaceID, targetParentID, targetName, reader, naming)
}

// OpenSnapshotContent opens one file in a snapshot for download.
func (s *Service) OpenSnapshotContent(ctx context.Context, actor accounts.User, snapshotID, snapshotEntryID string) (SnapshotContent, error) {
	if _, _, err := s.authorizeSnapshot(ctx, actor, snapshotID); err != nil {
		return SnapshotContent{}, err
	}
	entry, err := s.snapshotEntry(ctx, snapshotID, snapshotEntryID)
	if err != nil {
		return SnapshotContent{}, err
	}
	if entry.Kind != EntryKindFile {
		return SnapshotContent{}, ErrUnsupportedType
	}
	reader, object, err := s.snapshots.Open(ctx, snapshotID, entry.backendKey)
	if err != nil {
		return SnapshotContent{}, err
	}
	if object.Kind != EntryKindFile {
		_ = reader.Close()
		return SnapshotContent{}, ErrUnsupportedType
	}
	return SnapshotContent{Name: entry.Name, SizeBytes: object.SizeBytes, ModifiedAt: entry.ModifiedAt, Reader: reader}, nil
}

// SearchSnapshots finds files and folders whose name contains query in any
// snapshot of the space. truncated reports that more paths matched than
// were returned.
func (s *Service) SearchSnapshots(ctx context.Context, actor accounts.User, spaceID, query string) (matches []SnapshotMatch, truncated bool, err error) {
	if _, _, err := s.authorizedSpace(ctx, actor, spaceID, false); err != nil {
		return nil, false, err
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 100 {
		return nil, false, ErrInvalidQuery
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT e.id, e.name, e.kind, e.size_bytes, e.modified_at, e.backend_key, s.id, s.name, s.origin, s.created_at
FROM snapshot_entries e JOIN snapshots s ON s.id = e.snapshot_id
WHERE s.space_id = ? AND e.name LIKE ? ESCAPE '\'
ORDER BY e.backend_key, s.created_at DESC
LIMIT ?`, spaceID, "%"+likeEscaper.Replace(query)+"%", snapshotSearchRows+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	byPath := map[string]int{}
	byVersion := map[string]int{}
	read := 0
	for rows.Next() {
		if read++; read > snapshotSearchRows {
			truncated = true
			break
		}
		var version SnapshotVersion
		var name, path, modifiedAt, createdAt string
		var kind EntryKind
		if err := rows.Scan(&version.EntryID, &name, &kind, &version.SizeBytes, &modifiedAt, &path,
			&version.SnapshotID, &version.SnapshotName, &version.Origin, &createdAt); err != nil {
			return nil, false, err
		}
		version.ModifiedAt = parseOptionalTime(modifiedAt)
		version.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		position, ok := byPath[path]
		if !ok {
			if len(matches) == snapshotSearchPaths {
				truncated = true
				continue
			}
			position = len(matches)
			byPath[path] = position
			matches = append(matches, SnapshotMatch{Path: path, Name: name, Kind: kind})
		}
		match := &matches[position]
		// Rows of a path arrive newest first, so the first row of a version
		// names its newest snapshot and the last one its oldest.
		key := path + "\x00" + snapshotVersionKey(kind, version, modifiedAt)
		if index, ok := byVersion[key]; ok {
			match.Versions[index].Snapshots++
			match.Versions[index].OldestAt = version.CreatedAt
			continue
		}
		byVersion[key] = len(match.Versions)
		version.Snapshots, version.OldestAt = 1, version.CreatedAt
		match.Versions = append(match.Versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	_ = rows.Close()
	for index := range matches {
		var present int
		if err := s.store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM entries WHERE space_id = ? AND relative_path = ? AND trashed = 0", spaceID, matches[index].Path,
		).Scan(&present); err != nil {
			return nil, false, err
		}
		matches[index].InSpace = present > 0
	}
	// Exact names first; otherwise the order of their paths.
	folded := strings.ToLower(query)
	sort.SliceStable(matches, func(left, right int) bool {
		return strings.ToLower(matches[left].Name) == folded && strings.ToLower(matches[right].Name) != folded
	})
	return matches, truncated, nil
}

// snapshotVersionKey groups the snapshots holding the same version of a path:
// a folder is one version, a file is its size and modification time. Files
// recorded without a modification time are not grouped.
func snapshotVersionKey(kind EntryKind, version SnapshotVersion, modifiedAt string) string {
	switch {
	case kind == EntryKindDirectory:
		return "directory"
	case modifiedAt == "":
		return "snapshot:" + version.SnapshotID
	default:
		return fmt.Sprintf("%d|%s", version.SizeBytes, modifiedAt)
	}
}

func (s *Service) DeleteSnapshot(ctx context.Context, actor accounts.User, snapshotID string) error {
	snapshot, space, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return err
	}
	if !canManageSnapshots(actor, space) {
		return ErrForbidden
	}
	if err := s.deleteSnapshot(ctx, snapshot); err != nil {
		return err
	}
	return s.audit(ctx, actor.ID, "snapshot.deleted", snapshot.SpaceID, snapshot.ID, snapshot.Name)
}

func (s *Service) deleteSnapshot(ctx context.Context, snapshot Snapshot) error {
	if err := s.snapshots.Delete(ctx, snapshot.SpaceID, snapshot.ID); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id = ?", snapshot.ID)
	return err
}

// KeepSnapshot turns a scheduled snapshot into a manual one, so the space's
// schedule no longer expires it.
func (s *Service) KeepSnapshot(ctx context.Context, actor accounts.User, snapshotID string) (Snapshot, error) {
	snapshot, space, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if !canManageSnapshots(actor, space) {
		return Snapshot{}, ErrForbidden
	}
	if snapshot.Origin == SnapshotOriginManual {
		return snapshot, nil
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE snapshots SET origin = ? WHERE id = ?", SnapshotOriginManual, snapshot.ID); err != nil {
		return Snapshot{}, err
	}
	snapshot.Origin = SnapshotOriginManual
	_ = s.audit(ctx, actor.ID, "snapshot.kept", snapshot.SpaceID, snapshot.ID, snapshot.Name)
	return snapshot, nil
}

func (s *Service) authorizeSnapshot(ctx context.Context, actor accounts.User, snapshotID string) (Snapshot, accounts.Space, error) {
	snapshot, err := scanSnapshot(s.store.db.QueryRowContext(ctx, "SELECT "+snapshotColumns+" FROM snapshots WHERE id = ?", snapshotID))
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, accounts.Space{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, accounts.Space{}, err
	}
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
		objects = append(objects, SnapshotObject{Key: key, ParentKey: parentKey, Name: info.Name(), Kind: kind, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC()})
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
