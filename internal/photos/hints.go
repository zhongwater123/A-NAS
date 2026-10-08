package photos

import (
	"context"
	"strings"
)

// hintBatch bounds the SQLite parameters of one hint query.
const hintBatch = 500

// addDuplicateHints fills AlsoKeptBy for available assets of the caller's own
// private library with the names of other members whose private libraries
// hold the same original. The hint reveals the name only: no asset, path,
// metadata or count from the other library, and never to an administrator
// viewing someone else's library or for the shared library.
func (s *Service) addDuplicateHints(ctx context.Context, q queryer, p Principal, records []assetRecord) error {
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
		args := []any{p.UserID}
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := q.QueryContext(ctx, `
SELECT DISTINCT d.object_id, l.owner_name
FROM assets d JOIN libraries l ON l.id = d.library_id
WHERE l.kind = 'private' AND l.owner_user_id <> ? AND l.owner_name <> '' AND d.trashed_at IS NULL
  AND d.object_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)
ORDER BY l.owner_name`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var objectID, name string
			if err := rows.Scan(&objectID, &name); err != nil {
				_ = rows.Close()
				return err
			}
			for _, i := range positions[objectID] {
				records[i].AlsoKeptBy = append(records[i].AlsoKeptBy, name)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
