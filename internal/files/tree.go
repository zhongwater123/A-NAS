package files

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TreeEntry is an entry below a listed directory.
type TreeEntry struct {
	Entry
	// Path is slash-separated and relative to the listed directory.
	Path string `json:"path"`
}

// Tree lists everything below a directory of a space, or below the space root
// when directoryID is empty, after reconciling that part of the space with
// the file system. The media center and music scan their libraries with it.
func (s *Service) Tree(ctx context.Context, actor accounts.User, spaceID, directoryID string) ([]TreeEntry, error) {
	space, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return nil, err
	}
	query := `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at, relative_path
FROM entries WHERE space_id = ? AND trashed = 0`
	arguments := []any{spaceID}
	prefix := ""
	if directoryID == "" {
		if err := s.reconcileWhole(ctx, space, root); err != nil {
			return nil, err
		}
	} else {
		relative, err := s.directoryScope(ctx, space, root, directoryID)
		if err != nil {
			return nil, err
		}
		if _, err := s.reconcile(ctx, spaceID, root, scope{id: directoryID, relative: relative}); err != nil {
			return nil, err
		}
		from, to := below(relative)
		query += " AND relative_path >= ? AND relative_path < ?"
		arguments = append(arguments, from, to)
		prefix = from
	}
	rows, err := s.store.db.QueryContext(ctx, query+" ORDER BY relative_path", arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []TreeEntry
	for rows.Next() {
		var entry TreeEntry
		var modifiedAt, relativePath string
		if err := rows.Scan(&entry.ID, &entry.SpaceID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &modifiedAt, &relativePath); err != nil {
			return nil, err
		}
		if strings.HasPrefix(relativePath, unplacedPrefix) {
			continue
		}
		entry.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modifiedAt)
		entry.Path = relativePath[len(prefix):]
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// Lookup returns a catalogued entry the actor may read and its
// slash-separated path in its space, without touching the file system.
func (s *Service) Lookup(ctx context.Context, actor accounts.User, entryID string) (Entry, string, error) {
	var entry Entry
	var modifiedAt, relativePath string
	err := s.store.db.QueryRowContext(ctx, `
SELECT id, space_id, parent_id, name, kind, size_bytes, modified_at, relative_path
FROM entries WHERE id = ? AND trashed = 0`, entryID).Scan(
		&entry.ID, &entry.SpaceID, &entry.ParentID, &entry.Name, &entry.Kind, &entry.SizeBytes, &modifiedAt, &relativePath)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, "", ErrNotFound
	}
	if err != nil {
		return Entry{}, "", err
	}
	if s.policy == nil {
		return Entry{}, "", ErrForbidden
	}
	allowed, err := s.policy.CanAccessSpace(ctx, actor, entry.SpaceID, false)
	if err != nil {
		return Entry{}, "", err
	}
	if !allowed || strings.HasPrefix(relativePath, unplacedPrefix) {
		return Entry{}, "", ErrNotFound
	}
	entry.ModifiedAt, _ = time.Parse(time.RFC3339Nano, modifiedAt)
	return entry, relativePath, nil
}
