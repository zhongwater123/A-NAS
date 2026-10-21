package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// SnapshotChange compares a snapshot entry with what the space holds now at
// the same path.
type SnapshotChange string

const (
	// ChangeRemoved is a path the space no longer lists: deleted, moved or renamed.
	ChangeRemoved SnapshotChange = "removed"
	// ChangeModified is a file whose size or modification time differs now.
	ChangeModified SnapshotChange = "modified"
	// ChangeInside is a folder still in the space with something below it
	// removed or modified since the snapshot.
	ChangeInside SnapshotChange = "inside"
)

// RemovedEntry is a file or folder that snapshots hold but the space no
// longer lists at its path. Only the topmost removed item of a removed
// folder is reported; a folder carries the totals of the files inside it.
type RemovedEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	FileCount  int64     `json:"fileCount"`
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
	// The newest snapshot holding it, which restores and downloads read.
	SnapshotID   string         `json:"snapshotId"`
	SnapshotName string         `json:"snapshotName"`
	Origin       SnapshotOrigin `json:"origin"`
	EntryID      string         `json:"entryId"`
	LastSeenAt   time.Time      `json:"lastSeenAt"`
	// GoneBy is when the next snapshot was taken without it; zero when the
	// newest snapshot still holds it, so it went after that one.
	GoneBy time.Time `json:"goneBy,omitzero"`
	// InTrash reports that the recycle bin holds an item deleted from the
	// same path, which restores the latest version rather than a snapshot's.
	InTrash bool `json:"inTrash,omitempty"`
}

const (
	// A list of removed items reports at most removedEntriesLimit items and
	// reads snapshots, newest first, until removedEntriesRows entries were
	// compared with the space.
	removedEntriesLimit = 200
	removedEntriesRows  = 1_000_000
)

// descendantRange bounds the snapshot keys below a folder key: every key
// that starts with "key/" sorts between "key/" and "key0".
func descendantRange(key string) (string, string) { return key + "/", key + "0" }

// changeOf compares a snapshot entry with the live row at its path, if any.
// Something of another kind at the path does not stand for the entry, and a
// folder's own modification time is not compared.
func changeOf(entry SnapshotEntry, modifiedAt string, liveKind sql.NullString, liveSize sql.NullInt64, liveModified sql.NullString) SnapshotChange {
	switch {
	case !liveKind.Valid || EntryKind(liveKind.String) != entry.Kind:
		return ChangeRemoved
	case entry.Kind == EntryKindFile && (liveSize.Int64 != entry.SizeBytes || modifiedAt != "" && liveModified.String != modifiedAt):
		return ChangeModified
	}
	return ""
}

// liveMatch joins a snapshot entry e to the live row l at its path.
const liveMatch = "LEFT JOIN entries l ON l.space_id = ? AND l.relative_path = e.backend_key AND l.trashed = 0"

// annotateChanges compares a folder level of a snapshot with the space and
// marks folders that hold changes below them. The caller reconciled the space.
func (s *Service) annotateChanges(ctx context.Context, snapshot Snapshot, parentID string) ([]SnapshotEntry, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT e.id, e.snapshot_id, e.parent_id, e.name, e.kind, e.size_bytes, e.modified_at, e.backend_key, l.kind, l.size_bytes, l.modified_at
FROM snapshot_entries e `+liveMatch+`
WHERE e.snapshot_id = ? AND e.parent_id = ? ORDER BY e.kind, e.name COLLATE NOCASE`, snapshot.SpaceID, snapshot.ID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []SnapshotEntry
	var folders []int
	var keys []string
	for rows.Next() {
		var entry SnapshotEntry
		var modifiedAt, key string
		var liveKind, liveModified sql.NullString
		var liveSize sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.SnapshotID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &modifiedAt, &key,
			&liveKind, &liveSize, &liveModified); err != nil {
			return nil, err
		}
		entry.ModifiedAt = parseOptionalTime(modifiedAt)
		entry.Change = changeOf(entry, modifiedAt, liveKind, liveSize, liveModified)
		if entry.Kind == EntryKindDirectory && entry.Change == "" {
			folders = append(folders, len(entries))
			keys = append(keys, key)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for index, position := range folders {
		low, high := descendantRange(keys[index])
		var changed bool
		if err := s.store.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM snapshot_entries e `+liveMatch+`
WHERE e.snapshot_id = ? AND e.backend_key > ? AND e.backend_key < ?
    AND (l.id IS NULL OR l.kind <> e.kind OR (e.kind = 'file' AND (l.size_bytes <> e.size_bytes OR (e.modified_at <> '' AND l.modified_at <> e.modified_at)))))`,
			snapshot.SpaceID, snapshot.ID, low, high).Scan(&changed); err != nil {
			return nil, err
		}
		if changed {
			entries[position].Change = ChangeInside
		}
	}
	return entries, nil
}

// EntryVersions lists the versions of a file or folder that the snapshots of
// its space hold at its path, newest first. Versions equal to the file as it
// is now are marked current.
func (s *Service) EntryVersions(ctx context.Context, actor accounts.User, entryID string) (SnapshotMatch, error) {
	var spaceID, path, name, liveModified string
	var kind EntryKind
	var liveSize int64
	err := s.store.db.QueryRowContext(ctx,
		"SELECT space_id, relative_path, name, kind, size_bytes, modified_at FROM entries WHERE id = ? AND trashed = 0", entryID,
	).Scan(&spaceID, &path, &name, &kind, &liveSize, &liveModified)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotMatch{}, ErrNotFound
	}
	if err != nil {
		return SnapshotMatch{}, err
	}
	if _, _, err := s.authorizedSpace(ctx, actor, spaceID, false); err != nil {
		return SnapshotMatch{}, err
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT e.id, e.name, e.kind, e.size_bytes, e.modified_at, e.backend_key, s.id, s.name, s.origin, s.created_at
FROM snapshots s JOIN snapshot_entries e ON e.snapshot_id = s.id AND e.backend_key = ?
WHERE s.space_id = ? ORDER BY s.created_at DESC`, path, spaceID)
	if err != nil {
		return SnapshotMatch{}, err
	}
	defer rows.Close()
	versions := newVersionGroups()
	match := SnapshotMatch{Path: path, Name: name, Kind: kind, InSpace: true, Versions: []SnapshotVersion{}}
	for rows.Next() {
		row, err := scanVersionRow(rows)
		if err != nil {
			return SnapshotMatch{}, err
		}
		if row.kind != kind {
			// The path held something of another kind then; it is not a version of this one.
			continue
		}
		versions.add(&match, row)
	}
	if err := rows.Err(); err != nil {
		return SnapshotMatch{}, err
	}
	for index := range match.Versions {
		version := &match.Versions[index]
		version.Current = kind == EntryKindFile && version.SizeBytes == liveSize && formatOptionalTime(version.ModifiedAt) == liveModified
	}
	return match, nil
}

// RemovedEntries lists what the snapshots of a space hold that the space no
// longer lists, most recently removed first. truncated reports that the list
// stopped at its limit or before the oldest snapshot was compared.
func (s *Service) RemovedEntries(ctx context.Context, actor accounts.User, spaceID string) (items []RemovedEntry, truncated bool, err error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return nil, false, err
	}
	// SMB changes reach the catalog only when the space is reconciled.
	if err := s.reconcileWhole(ctx, space, root); err != nil {
		return nil, false, err
	}
	snapshots, err := s.spaceSnapshots(ctx, spaceID, "")
	if err != nil {
		return nil, false, err
	}
	items = []RemovedEntry{}
	seen := map[string]bool{}
	compared := int64(0)
	for index, snapshot := range snapshots {
		if compared >= removedEntriesRows || len(items) >= removedEntriesLimit {
			return items, true, nil
		}
		// file_count leaves out folders; it is close enough as a budget.
		compared += snapshot.FileCount
		found, err := s.removedIn(ctx, snapshot)
		if err != nil {
			return nil, false, err
		}
		for _, item := range found {
			if seen[item.Path] {
				continue
			}
			seen[item.Path] = true
			if index > 0 {
				item.GoneBy = snapshots[index-1].CreatedAt
			}
			if len(items) == removedEntriesLimit {
				return items, true, nil
			}
			items = append(items, item)
		}
	}
	return items, false, nil
}

// removedIn finds the topmost entries of one snapshot that the space no
// longer lists: removed themselves, while their folder is still there.
func (s *Service) removedIn(ctx context.Context, snapshot Snapshot) ([]RemovedEntry, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT e.id, e.name, e.kind, e.size_bytes, e.modified_at, e.backend_key
FROM snapshot_entries e LEFT JOIN snapshot_entries p ON p.id = e.parent_id
WHERE e.snapshot_id = ?
    AND NOT EXISTS (SELECT 1 FROM entries l WHERE l.space_id = ? AND l.relative_path = e.backend_key AND l.trashed = 0 AND l.kind = e.kind)
    AND (e.parent_id = '' OR EXISTS (SELECT 1 FROM entries l
        WHERE l.space_id = ? AND l.relative_path = p.backend_key AND l.trashed = 0 AND l.kind = 'directory'))
ORDER BY e.backend_key`, snapshot.ID, snapshot.SpaceID, snapshot.SpaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RemovedEntry
	for rows.Next() {
		item := RemovedEntry{SnapshotID: snapshot.ID, SnapshotName: snapshot.Name, Origin: snapshot.Origin, LastSeenAt: snapshot.CreatedAt}
		var modifiedAt string
		if err := rows.Scan(&item.EntryID, &item.Name, &item.Kind, &item.SizeBytes, &modifiedAt, &item.Path); err != nil {
			return nil, err
		}
		item.ModifiedAt = parseOptionalTime(modifiedAt)
		if item.Kind == EntryKindFile {
			item.FileCount = 1
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for index := range items {
		item := &items[index]
		folder, name := path.Split(item.Path)
		if err := s.store.db.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM trash WHERE space_id = ? AND original_path = ? AND original_name = ?)",
			snapshot.SpaceID, strings.TrimSuffix(folder, "/"), name,
		).Scan(&item.InTrash); err != nil {
			return nil, err
		}
		if item.Kind != EntryKindDirectory {
			continue
		}
		low, high := descendantRange(item.Path)
		if err := s.store.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM snapshot_entries
WHERE snapshot_id = ? AND kind = 'file' AND backend_key > ? AND backend_key < ?`, snapshot.ID, low, high,
		).Scan(&items[index].FileCount, &items[index].SizeBytes); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// versionRow is one snapshot entry read for version grouping.
type versionRow struct {
	version    SnapshotVersion
	name, path string
	kind       EntryKind
	modifiedAt string
}

func scanVersionRow(rows *sql.Rows) (versionRow, error) {
	var row versionRow
	var createdAt string
	if err := rows.Scan(&row.version.EntryID, &row.name, &row.kind, &row.version.SizeBytes, &row.modifiedAt, &row.path,
		&row.version.SnapshotID, &row.version.SnapshotName, &row.version.Origin, &createdAt); err != nil {
		return versionRow{}, err
	}
	row.version.ModifiedAt = parseOptionalTime(row.modifiedAt)
	row.version.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return row, nil
}

// versionGroups merges the snapshots holding the same version of a path.
type versionGroups map[string]int

func newVersionGroups() versionGroups { return versionGroups{} }

// add counts row towards its version. Rows of a path must arrive newest
// first, so the first row of a version names its newest snapshot and the
// last one its oldest.
func (g versionGroups) add(match *SnapshotMatch, row versionRow) {
	key := row.path + "\x00" + snapshotVersionKey(row.kind, row.version, row.modifiedAt)
	if index, ok := g[key]; ok {
		match.Versions[index].Snapshots++
		match.Versions[index].OldestAt = row.version.CreatedAt
		return
	}
	g[key] = len(match.Versions)
	version := row.version
	version.Snapshots, version.OldestAt = 1, version.CreatedAt
	match.Versions = append(match.Versions, version)
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

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}
