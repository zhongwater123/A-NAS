package photos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// The montage center's window vectors reach the photo service's vector
// index through window_outbox, as the image vectors do through
// vector_outbox, into a collection of their own (docs/architecture/vector-index.md).
// A video has a point per window, so an object's points are replaced
// together: deleted by the object field, then written again from the
// Catalog. A video waits in the outbox while its analysis is queued or
// running, so that its points are written once it is done, not again after
// every chunk. The Catalog stays the record, and a lost collection is built
// again from it.
//
// The montage center's text search reads the collection (windowsearch.go),
// through the alias windowsAlias: a new model's collection is filled while
// the alias still names the old one, and the alias moves to it once nothing
// waits in the outbox. The old collection stays until the model after, so
// that a model taken back takes its collection back too.

const (
	windowsIndex            = "windows"
	windowsAlias            = "video_windows_v1"
	windowsCollectionPrefix = windowsAlias + "_"
	objectField             = "object"
	// windowSyncBatch is how many queued videos one round brings in line;
	// each may have thousands of points.
	windowSyncBatch = 16
	// windowPointBatch is how many points one upsert carries: about 1.5 MB
	// of 768-dimension vectors, well under Qdrant's 32 MB request limit
	// however long the video.
	windowPointBatch = 500
)

// windowsCollectionName names a new collection for an identity's windows.
func windowsCollectionName(identity, nonce string) string {
	return windowsIdentityPrefix(identity) + nonce
}

// windowsIdentityPrefix starts the name of every collection of an
// identity's windows.
func windowsIdentityPrefix(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return windowsCollectionPrefix + hex.EncodeToString(sum[:4]) + "_"
}

type windowsCollection struct{ name, identity string }

func (s *Service) currentWindowsCollection(ctx context.Context) (windowsCollection, error) {
	var current windowsCollection
	err := s.db.QueryRowContext(ctx, "SELECT collection, model FROM vector_index_state WHERE name = ?", windowsIndex).
		Scan(&current.name, &current.identity)
	if errors.Is(err, sql.ErrNoRows) {
		return windowsCollection{}, nil
	}
	return current, err
}

// windowsCollection returns the collection of the newest windows' identity,
// the one sync writes, making it and queueing every video for it when the
// index has none. A new model's first stored chunk makes its windows the
// newest, as the image vectors' newest decides theirs. An earlier
// collection of that identity, still kept, is taken up again rather than
// made anew: the one the alias names, or the one it named before. With no
// windows at all, there is no collection.
func (s *Service) windowsCollection(ctx context.Context, index VectorIndex, report func(VectorSyncEvent), verify bool) (windowsCollection, error) {
	current, err := s.currentWindowsCollection(ctx)
	if err != nil {
		return windowsCollection{}, err
	}
	var identity string
	err = s.db.QueryRowContext(ctx, "SELECT identity FROM video_windows ORDER BY created_at DESC, rowid DESC LIMIT 1").Scan(&identity)
	if errors.Is(err, sql.ErrNoRows) {
		if current.name == "" {
			return windowsCollection{}, nil
		}
		// Every collection goes, and its alias with it.
		names, err := index.Collections(ctx)
		if err != nil {
			return windowsCollection{}, err
		}
		for _, name := range names {
			if strings.HasPrefix(name, windowsCollectionPrefix) {
				if err := index.DeleteCollection(ctx, name); err != nil {
					return windowsCollection{}, err
				}
			}
		}
		s.windowsChecked.Store(nil)
		s.windowsAliased.Store(nil)
		_, err = s.db.ExecContext(ctx, "DELETE FROM vector_index_state WHERE name = ?", windowsIndex)
		return windowsCollection{}, err
	}
	if err != nil {
		return windowsCollection{}, err
	}
	if checked := s.windowsChecked.Load(); !verify && checked != nil && *checked == current && current.identity == identity {
		return current, nil
	}
	names, err := index.Collections(ctx)
	if err != nil {
		return windowsCollection{}, err
	}
	if current.name != "" && current.identity == identity && slices.Contains(names, current.name) {
		s.windowsChecked.Store(&current)
		return current, nil
	}
	aliased, err := index.Alias(ctx, windowsAlias)
	if err != nil {
		return windowsCollection{}, err
	}
	target := windowsCollection{identity: identity}
	for _, name := range names {
		if strings.HasPrefix(name, windowsIdentityPrefix(identity)) && (target.name == "" || name == aliased) {
			target.name = name
		}
	}
	kind := "reused"
	if target.name == "" {
		kind = "created"
		var size int
		if err := s.db.QueryRowContext(ctx, "SELECT length(vector) FROM video_windows WHERE identity = ? LIMIT 1", identity).Scan(&size); err != nil {
			return windowsCollection{}, err
		}
		target.name = windowsCollectionName(identity, s.randomHex()[:8])
		// No graph: a few thousand points per video, scanned exactly.
		if err := index.CreateCollection(ctx, vectorindex.Collection{Name: target.name, Dimensions: size / 4,
			Keywords: []string{objectField, librariesField}}); err != nil {
			return windowsCollection{}, err
		}
	}
	// The collection the alias names is read until the new one is full.
	for _, name := range names {
		if strings.HasPrefix(name, windowsCollectionPrefix) && name != target.name && name != aliased {
			if err := index.DeleteCollection(ctx, name); err != nil {
				return windowsCollection{}, err
			}
		}
	}
	var queued int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO vector_index_state(name, collection, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET collection = excluded.collection, model = excluded.model, created_at = excluded.created_at`,
			windowsIndex, target.name, target.identity, formatTime(s.now())); err != nil {
			return err
		}
		// Every video is written again, into a collection taken up again
		// too: nothing kept it in line meanwhile. Points it still holds of
		// videos gone since are never searched, and the next reconciliation
		// deletes them. The WHERE keeps SQLite from reading ON CONFLICT as
		// part of the SELECT's join.
		result, err := tx.ExecContext(ctx, `INSERT INTO window_outbox(object_id) SELECT DISTINCT object_id FROM video_windows WHERE true
ON CONFLICT(object_id) DO UPDATE SET version = version + 1`)
		if err != nil {
			return err
		}
		queued, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return windowsCollection{}, err
	}
	s.windowsChecked.Store(&target)
	s.windowsAliased.Store(nil)
	if report != nil {
		report(VectorSyncEvent{Kind: kind, Collection: target.name, Queued: int(queued)})
	}
	return target, nil
}

// aliasWindows points the alias at the collection sync writes once nothing
// waits in the outbox: every video is written, and a search may read it. It
// asks the index only when it does not know the alias to name it already.
func (s *Service) aliasWindows(ctx context.Context, index VectorIndex, target windowsCollection, report func(VectorSyncEvent)) error {
	if target.name == "" {
		return nil
	}
	if aliased := s.windowsAliased.Load(); aliased != nil && *aliased == target.name {
		return nil
	}
	var waiting bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM window_outbox)").Scan(&waiting); err != nil || waiting {
		return err
	}
	aliased, err := index.Alias(ctx, windowsAlias)
	if err != nil {
		return err
	}
	if aliased != target.name {
		if err := index.SwitchAlias(ctx, windowsAlias, target.name); err != nil {
			return err
		}
		if report != nil {
			report(VectorSyncEvent{Kind: "switched", Collection: target.name})
		}
	}
	s.windowsAliased.Store(&target.name)
	return nil
}

// windowLibraries are the libraries with an untrashed asset of object,
// which scope its points; none when there is no such asset.
func (s *Service) windowLibraries(ctx context.Context, object string) ([]string, error) {
	return s.textColumn(ctx, "SELECT DISTINCT library_id FROM assets WHERE object_id = ? AND trashed_at IS NULL ORDER BY library_id", object)
}

// windowPoints reads up to limit of the points object should have in
// collection, from window index from on: one per window of its identity,
// scoped to libraries. next is where the next batch starts.
func (s *Service) windowPoints(ctx context.Context, object, identity string, libraries []string, from, limit int) (points []vectorindex.Point, next int, err error) {
	next = from
	err = s.eachRow(ctx, "SELECT idx, vector FROM video_windows WHERE object_id = ? AND identity = ? AND idx >= ? ORDER BY idx LIMIT ?",
		[]any{object, identity, from, limit}, func(rows *sql.Rows) error {
			var idx int
			var vector []byte
			if err := rows.Scan(&idx, &vector); err != nil {
				return err
			}
			points = append(points, vectorindex.Point{Key: windowKey(object, idx), Vector: decodeVector(vector),
				Keywords: map[string][]string{objectField: {object}, librariesField: libraries}})
			next = idx + 1
			return nil
		})
	return points, next, err
}

// windowKey is a window's point key in the collection.
func windowKey(object string, idx int) string { return object + "#" + strconv.Itoa(idx) }

// syncWindowPoints writes object's points, a batch of windowPointBatch at a
// time, and returns how many it wrote and how many the index refused.
func (s *Service) syncWindowPoints(ctx context.Context, index VectorIndex, collection windowsCollection, object string) (upserted, rejected int, err error) {
	libraries, err := s.windowLibraries(ctx, object)
	if err != nil || len(libraries) == 0 {
		return 0, 0, err
	}
	for from := 0; ; {
		points, next, err := s.windowPoints(ctx, object, collection.identity, libraries, from, windowPointBatch)
		if err != nil || len(points) == 0 {
			return upserted, rejected, err
		}
		refused, err := upsertPoints(ctx, index, collection.name, points)
		if err != nil {
			return upserted, rejected, err
		}
		upserted, rejected, from = upserted+len(points)-refused, rejected+refused, next
	}
}

// SyncWindows brings a batch of queued videos' window points in line and
// reports whether there was anything to do.
func (s *Service) SyncWindows(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) (worked bool, err error) {
	var waiting bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM window_outbox WHERE object_id NOT IN ("+analysedWindows+"))").
		Scan(&waiting); err != nil || !waiting {
		return false, err
	}
	// A new collection queues every video again, so the batch is read after.
	collection, err := s.windowsCollection(ctx, index, report, false)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			s.windowsChecked.Store(nil)
			s.windowsAliased.Store(nil)
		}
	}()
	batch, err := s.queuedWindows(ctx)
	if err != nil {
		return false, err
	}
	var upserted, deleted, rejected int
	if collection.name != "" {
		objects := make([]string, len(batch))
		for i, q := range batch {
			objects[i] = q.object
		}
		// An object's points go together, then come back from the Catalog.
		if err := index.DeleteMatching(ctx, collection.name, objectField, objects); err != nil {
			return false, err
		}
		deleted = len(objects)
		for _, object := range objects {
			wrote, refused, err := s.syncWindowPoints(ctx, index, collection, object)
			if err != nil {
				return false, err
			}
			upserted, rejected = upserted+wrote, rejected+refused
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, q := range batch {
			if _, err := tx.ExecContext(ctx, "DELETE FROM window_outbox WHERE object_id = ? AND version = ?", q.object, q.version); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if report != nil && collection.name != "" {
		report(VectorSyncEvent{Kind: "synced", Collection: collection.name, Upserted: upserted, Deleted: deleted, Rejected: rejected})
	}
	return true, s.aliasWindows(ctx, index, collection, report)
}

// analysedWindows is the SQL of the objects whose window analysis is
// queued or running, which wait in the outbox until it is done.
const analysedWindows = `SELECT object_id FROM jobs WHERE derivation = '` + windowsDerivation + `' AND state IN ('pending', 'running')`

// ReconcileWindows compares every point of the windows collection with
// what the Catalog says it should hold, as ReconcileVectors does the
// images', and queues each video whose points differ: it catches what the
// outbox cannot see, such as an instance that lost its storage or writes in
// a power cut. A collection the index lost is made again, with every video
// queued.
func (s *Service) ReconcileWindows(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) error {
	// The alias is looked at again too: the index may have lost it.
	s.windowsAliased.Store(nil)
	before, err := s.currentWindowsCollection(ctx)
	if err != nil {
		return err
	}
	current, err := s.windowsCollection(ctx, index, report, true)
	if err != nil || current.name == "" || current.name != before.name {
		// A new collection has everything queued already.
		return err
	}
	// What each video should have: as many points as windows, scoped to
	// its libraries. Videos still being analysed wait for their analysis.
	type wanted struct {
		count     int
		libraries []string
	}
	want := map[string]*wanted{}
	err = s.eachRow(ctx, `SELECT object_id, COUNT(*) FROM video_windows WHERE identity = ? AND object_id NOT IN (`+analysedWindows+`)
GROUP BY object_id`, []any{current.identity}, func(rows *sql.Rows) error {
		var object string
		var count int
		err := rows.Scan(&object, &count)
		want[object] = &wanted{count: count}
		return err
	})
	if err != nil {
		return err
	}
	err = s.eachRow(ctx, `SELECT DISTINCT object_id, library_id FROM assets WHERE trashed_at IS NULL
AND object_id IN (SELECT object_id FROM video_windows WHERE identity = ?) ORDER BY object_id, library_id`, []any{current.identity}, func(rows *sql.Rows) error {
		var object, library string
		if err := rows.Scan(&object, &library); err != nil {
			return err
		}
		if w := want[object]; w != nil {
			w.libraries = append(w.libraries, library)
		}
		return nil
	})
	if err != nil {
		return err
	}
	analysing, err := s.textColumn(ctx, analysedWindows)
	if err != nil {
		return err
	}
	analysed := map[string]bool{}
	for _, object := range analysing {
		analysed[object] = true
	}
	differ := map[string]bool{}
	seen := map[string]int{}
	err = index.Scan(ctx, current.name, []string{objectField, librariesField}, func(entry vectorindex.Entry) error {
		objects := entry.Keywords[objectField]
		if len(objects) != 1 {
			// Not a window's point: the video it names, if any, is written again.
			for _, object := range objects {
				differ[object] = true
			}
			return nil
		}
		object := objects[0]
		if analysed[object] {
			return nil
		}
		seen[object]++
		w := want[object]
		idx, err := strconv.Atoi(strings.TrimPrefix(entry.Key, object+"#"))
		have := slices.Clone(entry.Keywords[librariesField])
		slices.Sort(have)
		if w == nil || len(w.libraries) == 0 || err != nil || idx < 0 || idx >= w.count || !slices.Equal(have, w.libraries) {
			differ[object] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for object, w := range want {
		if len(w.libraries) > 0 && seen[object] != w.count {
			differ[object] = true
		}
	}
	queue := slices.Sorted(maps.Keys(differ))
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, object := range queue {
			if _, err := tx.ExecContext(ctx, `INSERT INTO window_outbox(object_id) VALUES(?)
ON CONFLICT(object_id) DO UPDATE SET version = version + 1`, object); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if report != nil {
		report(VectorSyncEvent{Kind: "reconciled", Collection: current.name, Queued: len(queue)})
	}
	return s.aliasWindows(ctx, index, current, report)
}

// queuedWindow is a video waiting in window_outbox.
type queuedWindow struct {
	object  string
	version int64
}

// queuedWindows reads the next videos to bring in line, leaving those whose
// analysis is still queued or running.
func (s *Service) queuedWindows(ctx context.Context) ([]queuedWindow, error) {
	var batch []queuedWindow
	err := s.eachRow(ctx, `SELECT object_id, version FROM window_outbox WHERE object_id NOT IN (`+analysedWindows+`)
ORDER BY object_id LIMIT ?`, []any{windowSyncBatch}, func(rows *sql.Rows) error {
		var q queuedWindow
		err := rows.Scan(&q.object, &q.version)
		batch = append(batch, q)
		return err
	})
	return batch, err
}

// textColumn reads one text column of a query's rows.
func (s *Service) textColumn(ctx context.Context, query string, args ...any) ([]string, error) {
	var out []string
	err := s.eachRow(ctx, query, args, func(rows *sql.Rows) error {
		var value string
		err := rows.Scan(&value)
		out = append(out, value)
		return err
	})
	return out, err
}
