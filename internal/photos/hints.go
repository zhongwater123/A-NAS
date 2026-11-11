package photos

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// hintBatch bounds the SQLite parameters of one hint query.
const hintBatch = 500

// addDuplicateHints fills two hints for available assets of the caller's own
// private library. AlsoKeptBy names other members whose private libraries
// hold the same original. The hint reveals the name only: no asset, path,
// metadata or count from the other library, and never to an administrator
// viewing someone else's library, for the shared library or to an assistant,
// which has no use for other members' names. SharedCopy names the shared
// library's asset with the same original, which every member sees anyway, so
// adding the photo to a family channel uses it instead of copying again.
func (s *Service) addDuplicateHints(ctx context.Context, q queryer, p Principal, records []assetRecord) error {
	if p.Assistant {
		return nil
	}
	positions := make(map[string][]int)
	var objects []string
	for i, record := range records {
		if record.Trash != nil || record.library.kind != LibraryKindPrivate || record.library.ownerUserID != p.UserID {
			continue
		}
		if _, seen := positions[record.objectID]; !seen {
			objects = append(objects, record.objectID)
		}
		positions[record.objectID] = append(positions[record.objectID], i)
	}
	for start := 0; start < len(objects); start += hintBatch {
		batch := objects[start:min(start+hintBatch, len(objects))]
		hints, err := duplicateHints(ctx, q, p.UserID, batch)
		if err != nil {
			return err
		}
		for _, hint := range hints {
			for _, i := range positions[hint.objectID] {
				records[i].AlsoKeptBy = append(records[i].AlsoKeptBy, hint.name)
			}
		}
		copies, err := sharedCopies(ctx, q, p, batch)
		if err != nil {
			return err
		}
		for _, copied := range copies {
			for _, i := range positions[copied.objectID] {
				if records[i].SharedCopy == "" && records[i].motionObjectID == copied.motionObjectID {
					records[i].SharedCopy = copied.id
				}
			}
		}
	}
	return nil
}

type duplicateHint struct{ objectID, name string }

// duplicateHints names, by name order, the other members whose private
// libraries hold one of objects.
func duplicateHints(ctx context.Context, q queryer, userID string, objects []string) ([]duplicateHint, error) {
	args := []any{userID}
	for _, id := range objects {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT d.object_id, l.owner_name
FROM assets d JOIN libraries l ON l.id = d.library_id
WHERE l.kind = 'private' AND l.owner_user_id <> ? AND l.owner_name <> '' AND d.trashed_at IS NULL
  AND d.object_id IN (`+sqlitedb.Placeholders(len(objects))+`)
ORDER BY l.owner_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hints []duplicateHint
	for rows.Next() {
		var hint duplicateHint
		if err := rows.Scan(&hint.objectID, &hint.name); err != nil {
			return nil, err
		}
		hints = append(hints, hint)
	}
	return hints, rows.Err()
}

type sharedCopy struct{ objectID, motionObjectID, id string }

// sharedCopies lists the shared library's assets that p sees and that hold
// one of objects, the earliest imported first: the asset its duplicate hint
// marks first.
func sharedCopies(ctx context.Context, q queryer, p Principal, objects []string) ([]sharedCopy, error) {
	args := make([]any, 0, len(objects))
	for _, id := range objects {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `
SELECT d.object_id, IFNULL(d.motion_object_id, ''), d.id
FROM assets d JOIN libraries l ON l.id = d.library_id
WHERE l.kind = 'shared' AND `+listedAssets(p, "d")+`
  AND d.object_id IN (`+sqlitedb.Placeholders(len(objects))+`)
ORDER BY d.imported_at, d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var copies []sharedCopy
	for rows.Next() {
		var copied sharedCopy
		if err := rows.Scan(&copied.objectID, &copied.motionObjectID, &copied.id); err != nil {
			return nil, err
		}
		copies = append(copies, copied)
	}
	return copies, rows.Err()
}
