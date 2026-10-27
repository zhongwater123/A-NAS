package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	// Change compares the entry with the space as it is now; empty when the
	// same path holds the same thing.
	Change SnapshotChange `json:"change,omitempty"`
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
	// Current marks the version of a file that the space holds now.
	Current bool `json:"current,omitempty"`
}

// SnapshotBackend accepts stable space and snapshot identities. Implementations
// own the mapping to Btrfs subvolumes or deterministic development directories.
// Create and Delete may be repeated: taking a snapshot that exists and
// deleting one that does not both succeed, so a caller that lost an answer
// retries or cleans up by the snapshot's ID.
type SnapshotBackend interface {
	Create(ctx context.Context, spaceID, snapshotID string) error
	// Objects calls visit for every file and folder a snapshot holds, each
	// folder before its contents, and stops at the first error. A snapshot
	// that does not exist fails with ErrNotFound.
	Objects(ctx context.Context, snapshotID string, visit func(SnapshotObject) error) error
	Open(ctx context.Context, snapshotID, key string) (io.ReadCloser, SnapshotObject, error)
	Delete(ctx context.Context, spaceID, snapshotID string) error
	// Prune deletes every snapshot that keep does not name and reports which
	// of keep exist.
	Prune(ctx context.Context, keep []string) (SnapshotPrune, error)
}

// SnapshotPrune is the outcome of a Prune: the kept snapshots that exist and
// how many snapshots nobody keeps were deleted.
type SnapshotPrune struct {
	Present []string `json:"present"`
	Removed int      `json:"removed"`
}

const (
	// A search reports at most this many paths, read from at most
	// snapshotSearchRows snapshot entries.
	snapshotSearchPaths = 100
	snapshotSearchRows  = 5000
	snapshotColumns     = "id, space_id, name, origin, created_by, created_at, file_count, size_bytes"
	// snapshotDiscardTimeout bounds removing a snapshot whose taking failed,
	// which also runs while the service stops.
	snapshotDiscardTimeout = 10 * time.Second
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
	// Until the catalog records it, the snapshot is this call's to remove:
	// sweeps leave it alone, and any failure, a closed tab or a stopping
	// service included, deletes it again. What a crash leaves, the next
	// sweep removes.
	s.holdSnapshot(snapshot.ID, true)
	committed := false
	defer func() {
		if !committed {
			s.discardSnapshot(ctx, spaceID, snapshot.ID)
		}
		s.holdSnapshot(snapshot.ID, false)
	}()
	if err := s.snapshots.Create(ctx, spaceID, snapshot.ID); err != nil {
		return Snapshot{}, err
	}
	// The whole listing is read before the catalog is written, so the
	// single-connection catalog stays free while the backend walks the snapshot.
	var objects []SnapshotObject
	folders := map[string]bool{"": true}
	if err := s.snapshots.Objects(ctx, snapshot.ID, func(object SnapshotObject) error {
		if !folders[object.ParentKey] {
			return errors.New("snapshot backend returned an object before its parent")
		}
		switch object.Kind {
		case EntryKindDirectory:
			folders[object.Key] = true
		case EntryKindFile:
			snapshot.FileCount++
			snapshot.SizeBytes += object.SizeBytes
		default:
			return errors.New("snapshot backend returned an object of an unknown kind")
		}
		objects = append(objects, object)
		return nil
	}); err != nil {
		return Snapshot{}, err
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
	defer func() { _ = insert.Close() }()
	// Only folders are parents, so only their entry IDs are remembered.
	folderIDs := map[string]string{"": ""}
	for _, object := range objects {
		entryID := s.randomID("snapshot-entry")
		modifiedAt := ""
		if !object.ModifiedAt.IsZero() {
			modifiedAt = formatTime(object.ModifiedAt)
		}
		if _, err := insert.ExecContext(ctx, entryID, snapshot.ID, folderIDs[object.ParentKey], object.Name, object.Kind, object.SizeBytes, object.Key, modifiedAt); err != nil {
			return Snapshot{}, err
		}
		if object.Kind == EntryKindDirectory {
			folderIDs[object.Key] = entryID
		}
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

// ListSnapshotEntries lists one folder level of a snapshot, each entry
// compared with the space as it is now. Opening the snapshot's top level
// reconciles the space, so changes made over SMB are told too.
func (s *Service) ListSnapshotEntries(ctx context.Context, actor accounts.User, snapshotID, parentID string) ([]SnapshotEntry, error) {
	snapshot, space, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return nil, err
	}
	if parentID == "" {
		if err := s.reconcileWhole(ctx, space, s.spaceRoot(space)); err != nil {
			return nil, err
		}
	}
	return s.annotateChanges(ctx, snapshot, parentID)
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
// the snapshot's space as the user. A folder restore that fails part way
// keeps what it already copied. An existing entry is never overwritten: a
// taken name fails with ErrConflict, unless replace asks for a file to take
// the place of the file of that name, which then goes to the recycle bin.
func (s *Service) RestoreSnapshotEntry(ctx context.Context, actor accounts.User, snapshotID, snapshotEntryID, targetParentID, targetName string, replace bool) (Entry, error) {
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
	if replace && entry.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	var restored Entry
	action := "snapshot.file_restored"
	if replace {
		action = "snapshot.file_replaced"
		restored, err = s.replaceWithSnapshotFile(ctx, actor, snapshot, entry, targetParentID, targetName)
	} else if entry.Kind == EntryKindDirectory {
		action = "snapshot.folder_restored"
		restored, err = s.createDirectory(ctx, actor, snapshot.SpaceID, targetParentID, targetName, keeping(entry.Name))
		if err == nil {
			err = s.restoreSnapshotChildren(ctx, actor, snapshot, entry.ID, restored.ID)
		}
	} else {
		restored, err = s.restoreSnapshotFile(ctx, actor, snapshot, entry, targetParentID, targetName, keeping(entry.Name))
	}
	if err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, action, snapshot.SpaceID, restored.ID, snapshot.ID)
	return restored, nil
}

// RestoreSnapshot copies everything a snapshot holds into a new folder of its
// space. Like a folder restore, a failure part way keeps what was copied.
func (s *Service) RestoreSnapshot(ctx context.Context, actor accounts.User, snapshotID, targetParentID, targetName string) (Entry, error) {
	snapshot, _, err := s.authorizeSnapshot(ctx, actor, snapshotID)
	if err != nil {
		return Entry{}, err
	}
	restored, err := s.createDirectory(ctx, actor, snapshot.SpaceID, targetParentID, targetName, nameChosen)
	if err != nil {
		return Entry{}, err
	}
	if err := s.restoreSnapshotChildren(ctx, actor, snapshot, "", restored.ID); err != nil {
		return Entry{}, err
	}
	_ = s.audit(ctx, actor.ID, "snapshot.restored", snapshot.SpaceID, restored.ID, snapshot.ID)
	return restored, nil
}

// replaceWithSnapshotFile writes a snapshot file in full beside the file it
// replaces, then moves that file to the recycle bin and puts the restored
// one in its place, as an upload that replaces does.
func (s *Service) replaceWithSnapshotFile(ctx context.Context, actor accounts.User, snapshot Snapshot, entry storedSnapshotEntry, targetParentID, targetName string) (Entry, error) {
	reader, object, err := s.snapshots.Open(ctx, snapshot.ID, entry.backendKey)
	if err != nil {
		return Entry{}, err
	}
	defer reader.Close()
	if object.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	upload, err := s.BeginUpload(ctx, actor, UploadRequest{
		SpaceID: snapshot.SpaceID, ParentID: targetParentID, Name: targetName, Size: object.SizeBytes, Conflict: ConflictReplace,
	})
	if err != nil {
		return Entry{}, err
	}
	upload.modTime = entry.ModifiedAt
	committed := false
	defer func() {
		if !committed {
			_ = s.AbortUpload(context.WithoutCancel(ctx), actor, upload)
		}
	}()
	if _, err := s.WriteUpload(ctx, upload, 0, reader); err != nil {
		return Entry{}, err
	}
	result, err := s.CommitUpload(ctx, actor, upload)
	if err != nil {
		return Entry{}, err
	}
	committed = true
	return result.Entry, nil
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
		if _, err := s.restoreSnapshotFile(ctx, actor, snapshot, child, targetParentID, child.Name, nameKept); err != nil {
			return err
		}
	}
	return nil
}

// restoreSnapshotFile copies a snapshot file out with the modification time
// it was saved with.
func (s *Service) restoreSnapshotFile(ctx context.Context, actor accounts.User, snapshot Snapshot, entry storedSnapshotEntry, targetParentID, targetName string, naming naming) (Entry, error) {
	reader, object, err := s.snapshots.Open(ctx, snapshot.ID, entry.backendKey)
	if err != nil {
		return Entry{}, err
	}
	defer reader.Close()
	if object.Kind != EntryKindFile {
		return Entry{}, ErrUnsupportedType
	}
	return s.upload(ctx, actor, snapshot.SpaceID, targetParentID, targetName, reader, naming, entry.ModifiedAt)
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
	versions := newVersionGroups()
	read := 0
	for rows.Next() {
		if read++; read > snapshotSearchRows {
			truncated = true
			break
		}
		row, err := scanVersionRow(rows)
		if err != nil {
			return nil, false, err
		}
		position, ok := byPath[row.path]
		if !ok {
			if len(matches) == snapshotSearchPaths {
				truncated = true
				continue
			}
			position = len(matches)
			byPath[row.path] = position
			matches = append(matches, SnapshotMatch{Path: row.path, Name: row.name, Kind: row.kind})
		}
		versions.add(&matches[position], row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	_ = rows.Close()
	for index := range matches {
		match := &matches[index]
		var liveKind EntryKind
		var liveSize int64
		var liveModified string
		err := s.store.db.QueryRowContext(ctx,
			"SELECT kind, size_bytes, modified_at FROM entries WHERE space_id = ? AND relative_path = ? AND trashed = 0", spaceID, match.Path,
		).Scan(&liveKind, &liveSize, &liveModified)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		match.InSpace = true
		for version := range match.Versions {
			current := &match.Versions[version]
			current.Current = match.Kind == EntryKindFile && liveKind == EntryKindFile && current.SizeBytes == liveSize &&
				formatOptionalTime(current.ModifiedAt) == liveModified
		}
	}
	// Exact names first; otherwise the order of their paths.
	folded := strings.ToLower(query)
	sort.SliceStable(matches, func(left, right int) bool {
		return strings.ToLower(matches[left].Name) == folded && strings.ToLower(matches[right].Name) != folded
	})
	return matches, truncated, nil
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

// deleteSnapshot removes a snapshot and then its record. A snapshot the
// backend no longer has counts as removed, so a record whose removal was
// interrupted can still go.
func (s *Service) deleteSnapshot(ctx context.Context, snapshot Snapshot) error {
	if err := s.snapshots.Delete(ctx, snapshot.SpaceID, snapshot.ID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id = ?", snapshot.ID)
	return err
}

// discardSnapshot deletes a snapshot the catalog never recorded, even after
// ctx ended. One the backend cannot delete now is left to the next sweep.
func (s *Service) discardSnapshot(ctx context.Context, spaceID, snapshotID string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotDiscardTimeout)
	defer cancel()
	_ = s.snapshots.Delete(cleanup, spaceID, snapshotID)
}

// holdSnapshot marks a snapshot as being taken, which keeps sweeps from
// removing it before the catalog records it.
func (s *Service) holdSnapshot(snapshotID string, held bool) {
	s.snapshotsMu.Lock()
	defer s.snapshotsMu.Unlock()
	if held {
		s.takingSnapshots[snapshotID] = struct{}{}
	} else {
		delete(s.takingSnapshots, snapshotID)
	}
}

// SnapshotSweep reports what a sweep removed.
type SnapshotSweep struct {
	// Snapshots counts snapshots on the data volume that the catalog did not
	// record, such as those a crash left while one was being taken.
	Snapshots int
	// Records names the catalog records whose snapshot was gone.
	Records []Snapshot
}

// SweepSnapshots makes the data volume and the catalog agree: it deletes the
// snapshots the catalog does not record and the records whose snapshot is
// gone. It runs only while the intended data volume is mounted.
func (s *Service) SweepSnapshots(ctx context.Context) (SnapshotSweep, error) {
	if !s.allowUnverifiedVolume {
		if s.volumeGuard == nil || s.volumeGuard.Check(ctx, s.volumeRoot, false) != nil {
			return SnapshotSweep{}, ErrVolumeUnavailable
		}
	}
	// Held for the whole sweep: a snapshot taken meanwhile waits to be held
	// until the sweep is done, so it is never mistaken for one nobody keeps.
	s.snapshotsMu.Lock()
	defer s.snapshotsMu.Unlock()
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+snapshotColumns+" FROM snapshots ORDER BY created_at")
	if err != nil {
		return SnapshotSweep{}, err
	}
	defer rows.Close()
	var recorded []Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return SnapshotSweep{}, err
		}
		recorded = append(recorded, snapshot)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return SnapshotSweep{}, err
	}
	keep := make([]string, 0, len(recorded)+len(s.takingSnapshots))
	for _, snapshot := range recorded {
		keep = append(keep, snapshot.ID)
	}
	for id := range s.takingSnapshots {
		keep = append(keep, id)
	}
	pruned, err := s.snapshots.Prune(ctx, keep)
	if err != nil {
		return SnapshotSweep{}, err
	}
	sweep := SnapshotSweep{Snapshots: pruned.Removed}
	present := make(map[string]bool, len(pruned.Present))
	for _, id := range pruned.Present {
		present[id] = true
	}
	for _, snapshot := range recorded {
		if present[snapshot.ID] {
			continue
		}
		if _, err := s.store.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id = ?", snapshot.ID); err != nil {
			return sweep, err
		}
		_ = s.audit(ctx, systemActor, "snapshot.missing", snapshot.SpaceID, snapshot.ID, snapshot.Name)
		sweep.Records = append(sweep.Records, snapshot)
	}
	return sweep, nil
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

// unconfiguredSnapshots stands in when NewService was given no snapshot
// backend: every snapshot operation fails as unavailable.
type unconfiguredSnapshots struct{}

var errNoSnapshotBackend = fmt.Errorf("%w: no snapshot backend is configured", ErrVolumeUnavailable)

func (unconfiguredSnapshots) Create(context.Context, string, string) error {
	return errNoSnapshotBackend
}
func (unconfiguredSnapshots) Objects(context.Context, string, func(SnapshotObject) error) error {
	return errNoSnapshotBackend
}
func (unconfiguredSnapshots) Open(context.Context, string, string) (io.ReadCloser, SnapshotObject, error) {
	return nil, SnapshotObject{}, errNoSnapshotBackend
}
func (unconfiguredSnapshots) Delete(context.Context, string, string) error {
	return errNoSnapshotBackend
}
func (unconfiguredSnapshots) Prune(context.Context, []string) (SnapshotPrune, error) {
	return SnapshotPrune{}, errNoSnapshotBackend
}

type directorySnapshotBackend struct {
	baseRoot string
	resolve  func(string) (string, error)
}

func newDirectorySnapshotBackend(baseRoot string, resolve func(string) (string, error)) *directorySnapshotBackend {
	return &directorySnapshotBackend{baseRoot: baseRoot, resolve: resolve}
}

func (b *directorySnapshotBackend) base() string {
	return filepath.Join(b.baseRoot, ".a-nas-snapshots")
}

func (b *directorySnapshotBackend) root(snapshotID string) string {
	return filepath.Join(b.base(), safeSegment(snapshotID))
}

// Create copies the space into the snapshot's directory with the times its
// files and folders had, as a Btrfs snapshot keeps them.
func (b *directorySnapshotBackend) Create(ctx context.Context, spaceID, snapshotID string) error {
	sourceRoot, err := b.resolve(spaceID)
	if err != nil {
		return err
	}
	targetRoot := b.root(snapshotID)
	if _, err := os.Lstat(targetRoot); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(b.base(), 0o700); err != nil {
		return err
	}
	temporary := targetRoot + ".incoming"
	if err := os.Mkdir(temporary, 0o750); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	var folders []SnapshotObject
	if err := WalkSnapshot(ctx, sourceRoot, func(object SnapshotObject) error {
		destination := filepath.Join(temporary, filepath.FromSlash(object.Key))
		if object.Kind == EntryKindDirectory {
			folders = append(folders, object)
			return os.Mkdir(destination, 0o750)
		}
		if err := copyRegularFile(filepath.Join(sourceRoot, filepath.FromSlash(object.Key)), destination, 0o440); err != nil {
			return err
		}
		return os.Chtimes(destination, object.ModifiedAt, object.ModifiedAt)
	}); err != nil {
		return err
	}
	// Deepest first, as filling a folder changes its time.
	for _, folder := range slices.Backward(folders) {

		if err := os.Chtimes(filepath.Join(temporary, filepath.FromSlash(folder.Key)), folder.ModifiedAt, folder.ModifiedAt); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, targetRoot); err != nil {
		return err
	}
	committed = true
	return nil
}

func (b *directorySnapshotBackend) Objects(ctx context.Context, snapshotID string, visit func(SnapshotObject) error) error {
	root := b.root(snapshotID)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return WalkSnapshot(ctx, root, visit)
}

func (b *directorySnapshotBackend) Open(_ context.Context, snapshotID, key string) (io.ReadCloser, SnapshotObject, error) {
	root := b.root(snapshotID)
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
	root := b.root(snapshotID)
	if !withinRoot(b.base(), root) {
		return ErrNotFound
	}
	return os.RemoveAll(root)
}

// Prune also removes the copies that a stopped Create left unfinished.
func (b *directorySnapshotBackend) Prune(_ context.Context, keep []string) (SnapshotPrune, error) {
	wanted := make(map[string]string, len(keep))
	for _, id := range keep {
		wanted[safeSegment(id)] = id
	}
	pruned := SnapshotPrune{Present: []string{}}
	items, err := os.ReadDir(b.base())
	if errors.Is(err, os.ErrNotExist) {
		return pruned, nil
	}
	if err != nil {
		return SnapshotPrune{}, err
	}
	for _, item := range items {
		if id, ok := wanted[item.Name()]; ok {
			pruned.Present = append(pruned.Present, id)
			continue
		}
		if _, ok := wanted[strings.TrimSuffix(item.Name(), ".incoming")]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(b.base(), item.Name())); err != nil {
			return SnapshotPrune{}, err
		}
		pruned.Removed++
	}
	return pruned, nil
}

// WalkSnapshot calls visit for the files and folders below root in
// pre-order, so each folder comes before its contents. It leaves out A-NAS
// bookkeeping, symlinks and special files, and stops when ctx ends.
func WalkSnapshot(ctx context.Context, root string, visit func(SnapshotObject) error) error {
	return filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
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
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parentKey := filepath.ToSlash(filepath.Dir(relative))
		if parentKey == "." {
			parentKey = ""
		}
		kind := EntryKindFile
		if info.IsDir() {
			kind = EntryKindDirectory
		}
		return visit(SnapshotObject{
			Key: filepath.ToSlash(relative), ParentKey: parentKey, Name: info.Name(), Kind: kind, SizeBytes: info.Size(),
			ModifiedAt: info.ModTime().UTC(),
		})
	})
}

// copyRegularFile copies one file for the development snapshot backend, which
// runs only without a Host Agent.
func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
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
