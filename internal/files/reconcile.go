package files

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// scope is the part of a space one reconciliation pass covers (#167):
// listing a folder reconciles its children, a tree below a folder
// reconciles that subtree, and only the background, a comparison with
// snapshots or a folder that moved outside A-NAS needs the whole space.
type scope struct {
	// id and relative name the folder the pass starts at, relative to the
	// space root; both are empty for the root.
	id, relative string
	// shallow passes cover the folder's children without entering them.
	shallow bool
}

// whole reports whether the pass covers the whole space. Only such a pass
// knows that a row it did not find is gone rather than moved elsewhere.
func (sc scope) whole() bool { return sc.id == "" && !sc.shallow }

func (sc scope) path(root string) string {
	return filepath.Join(root, filepath.FromSlash(sc.relative))
}

func (sc scope) skip() ScanSkip {
	skip := reconcileSkip
	skip.Shallow = sc.shallow
	return skip
}

// unplacedPrefix holds rows whose files a partial pass no longer found
// where the rows say: they may have moved anywhere in the space. Listings
// leave them out, a pass that finds their inode puts them back under their
// IDs, and a pass over the whole space deletes the ones it does not find.
const unplacedPrefix = ".a-nas-stale/"

func unplacedPath(id string) string { return unplacedPrefix + id }

// placed is the SQL condition that leaves unplaced rows out.
var placed = "substr(relative_path, 1, " + strconv.Itoa(len(unplacedPrefix)) + ") <> '" + unplacedPrefix + "'"

// errMoved says that an entry's row exists but its path does not hold its
// file: it moved or went away outside A-NAS, and a pass over the whole
// space tells which.
var errMoved = fmt.Errorf("%w: the entry is not where the catalog says", ErrNotFound)

// wholePassFresh is how long a pass over a whole space spares the
// background loop, which runs every 5 minutes for each member's session,
// from passing over it again: a little under the loop's interval, so each
// round passes over a shared space once however many sessions reach it.
const wholePassFresh = 4 * time.Minute

// reconcileWhole reconciles the whole space. Passes over one space run one
// at a time, and a caller that waited for another pass is served by it when
// that pass began after the caller asked.
func (s *Service) reconcileWhole(ctx context.Context, space accounts.Space, root string) error {
	order := s.order(space.ID)
	order.mu.Lock()
	asked := order.wholeStarted
	order.mu.Unlock()
	select {
	case order.wholeSlot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-order.wholeSlot }()
	order.mu.Lock()
	if order.wholeWritten > asked {
		order.mu.Unlock()
		return nil
	}
	order.wholeStarted++
	pass, started := order.wholeStarted, s.now()
	order.mu.Unlock()
	written, err := s.reconcile(ctx, space.ID, root, scope{})
	if written {
		order.wholeDone(pass, started)
	}
	return err
}

// wholeDone records a pass over the whole space that wrote what it found.
func (o *spaceOrder) wholeDone(pass uint64, started time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if pass > o.wholeWritten {
		o.wholeWritten = pass
	}
	if started.After(o.wholeAt) {
		o.wholeAt = started
	}
}

// wholeFresh reports whether a pass over the whole space began less than
// wholePassFresh before now.
func (o *spaceOrder) wholeFresh(now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.wholeAt.IsZero() && now.Sub(o.wholeAt) < wholePassFresh
}

// reconcile brings the rows of a scope of a space in line with its files,
// which SMB and other clients change without A-NAS. A change through A-NAS
// that is in flight, or that happens during the scan, makes the scan stale,
// and it is discarded (see spaceOrder): the change keeps its own rows
// current, and the next pass catches up with the rest. written reports
// whether the scan was written.
func (s *Service) reconcile(ctx context.Context, spaceID, root string, sc scope) (written bool, err error) {
	order := s.order(spaceID)
	generation, ok := order.observe()
	if !ok {
		return false, nil
	}
	scanned, err := s.fs.Scan(ctx, sc.path(root), sc.skip())
	if err != nil {
		return false, err
	}
	if !order.beginWrite(generation) {
		return false, nil
	}
	changed, err := s.writeScan(ctx, spaceID, root, sc, scanned)
	order.endWrite(changed)
	return err == nil, err
}

// directoryScope returns the path of a catalogued folder relative to the
// space root, for a pass that starts there. A folder that is not where its
// row says, because it moved outside A-NAS, is looked for in a pass over
// the whole space.
func (s *Service) directoryScope(ctx context.Context, space accounts.Space, root, id string) (string, error) {
	entry, full, err := s.entryPath(ctx, root, space.ID, id)
	if errors.Is(err, errMoved) {
		if err := s.reconcileWhole(ctx, space, root); err != nil {
			return "", err
		}
		entry, full, err = s.entryPath(ctx, root, space.ID, id)
	}
	if err != nil {
		return "", err
	}
	if entry.Kind != EntryKindDirectory {
		return "", ErrNotFound
	}
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return "", ErrNotFound
	}
	return filepath.ToSlash(relative), nil
}

// knownRow is a catalogued entry as a pass compares it with its scan.
type knownRow struct {
	id, parentID, name, relativePath, modifiedAt string
	kind                                         EntryKind
	size                                         int64
}

// unchanged reports whether the row already says what the scan found.
func (r knownRow) unchanged(parentID, relativePath string, info FileInfo) bool {
	return r.parentID == parentID && r.relativePath == relativePath && r.name == info.Name &&
		string(r.kind) == info.Kind && r.size == info.Size && r.modifiedAt == formatTime(info.ModTime)
}

// scopeRows reads the rows a pass over sc covers, by inode.
func scopeRows(ctx context.Context, q queryer, spaceID string, sc scope) (map[uint64]knownRow, error) {
	query := `SELECT id, parent_id, name, kind, relative_path, inode, size_bytes, modified_at
FROM entries WHERE space_id = ? AND trashed = 0`
	arguments := []any{spaceID}
	switch {
	case sc.shallow:
		query += " AND parent_id = ?"
		arguments = append(arguments, sc.id)
	case !sc.whole():
		from, to := below(sc.relative)
		query += " AND relative_path >= ? AND relative_path < ?"
		arguments = append(arguments, from, to)
	}
	rows, err := q.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := make(map[uint64]knownRow)
	for rows.Next() {
		var row knownRow
		var inode uint64
		if err := rows.Scan(&row.id, &row.parentID, &row.name, &row.kind, &row.relativePath, &inode, &row.size, &row.modifiedAt); err != nil {
			return nil, err
		}
		known[inode] = row
	}
	return known, rows.Err()
}

// below returns the bounds of the paths below a folder's, for a range on
// the UNIQUE(space_id, relative_path) index: '0' follows '/'.
func below(relative string) (from, to string) {
	return relative + "/", relative + "0"
}

// dropMissing handles the rows a pass did not find, and reports whether it
// wrote any. A pass over the whole space deletes them. A partial pass
// unplaces them, since the file may have moved elsewhere in the space,
// where a later pass keeps its ID; a shallow pass moves the rows below an
// unplaced folder along with it, since it does not cover them.
func dropMissing(ctx context.Context, q queryer, spaceID string, sc scope, known map[uint64]knownRow, seen map[uint64]struct{}) (bool, error) {
	var missing []knownRow
	for inode, row := range known {
		if _, ok := seen[inode]; !ok {
			missing = append(missing, row)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].relativePath < missing[j].relativePath })
	wrote := false
	for _, row := range missing {
		switch {
		case sc.whole():
			if _, err := q.ExecContext(ctx, "DELETE FROM entries WHERE id = ?", row.id); err != nil {
				return wrote, err
			}
		case strings.HasPrefix(row.relativePath, unplacedPrefix):
			continue
		default:
			if _, err := q.ExecContext(ctx, "UPDATE entries SET relative_path = ? WHERE id = ?", unplacedPath(row.id), row.id); err != nil {
				return wrote, err
			}
			if sc.shallow && row.kind == EntryKindDirectory {
				if err := movePrefix(ctx, q, spaceID, row.relativePath, unplacedPath(row.id)); err != nil {
					return wrote, err
				}
			}
		}
		wrote = true
	}
	return wrote, nil
}

// moveBelow moves the rows below a folder whose row moved from one path to
// another, so that its contents follow a rename made outside A-NAS without
// waiting for a pass over each of them. Rows already below the new path
// belong to what was there before and are unplaced first. The rows go by
// way of the folder's unplaced path, which only they use, so no step of
// the rewrite holds a path twice.
func moveBelow(ctx context.Context, q queryer, spaceID, id, from, to string) error {
	parked := unplacedPath(id)
	if from != parked {
		if err := unplaceBelow(ctx, q, spaceID, parked); err != nil {
			return err
		}
		if err := movePrefix(ctx, q, spaceID, from, parked); err != nil {
			return err
		}
	}
	if to == parked {
		return nil
	}
	if err := unplaceBelow(ctx, q, spaceID, to); err != nil {
		return err
	}
	return movePrefix(ctx, q, spaceID, parked, to)
}

// movePrefix rewrites the paths below one folder path to below another.
func movePrefix(ctx context.Context, q queryer, spaceID, from, to string) error {
	start, end := below(from)
	_, err := q.ExecContext(ctx, `UPDATE entries SET relative_path = ? || substr(relative_path, length(?) + 1)
WHERE space_id = ? AND trashed = 0 AND relative_path >= ? AND relative_path < ?`,
		to, from, spaceID, start, end)
	return err
}

// unplaceBelow unplaces every row below a folder path, recycle-bin rows
// that a stale scan of an earlier version wrote there included (#164).
func unplaceBelow(ctx context.Context, q queryer, spaceID, folder string) error {
	start, end := below(folder)
	_, err := q.ExecContext(ctx, `UPDATE entries SET relative_path = '`+unplacedPrefix+`' || id
WHERE space_id = ? AND relative_path >= ? AND relative_path < ?`, spaceID, start, end)
	return err
}

// scopedEntries makes the paths of a scan below sc relative to the space
// root.
func scopedEntries(sc scope, scanned []ScanEntry) []ScanEntry {
	if sc.relative == "" {
		return scanned
	}
	entries := make([]ScanEntry, len(scanned))
	for index, item := range scanned {
		item.Path = path.Join(sc.relative, item.Path)
		entries[index] = item
	}
	return entries
}
