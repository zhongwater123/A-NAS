package photos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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
// Catalog. Nothing searches the collection yet; the Catalog stays the
// record, and a lost collection is built again from it.

const (
	windowsIndex            = "windows"
	windowsCollectionPrefix = "video_windows_v1_"
	objectField             = "object"
	// windowSyncBatch is how many queued videos one round brings in line;
	// each may have hundreds of points.
	windowSyncBatch = 16
)

// windowsCollectionName names a new collection for an identity's windows.
func windowsCollectionName(identity, nonce string) string {
	sum := sha256.Sum256([]byte(identity))
	return windowsCollectionPrefix + hex.EncodeToString(sum[:4]) + "_" + nonce
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
// making it and queueing every video for it when the index has none. With
// no windows at all, there is no collection.
func (s *Service) windowsCollection(ctx context.Context, index VectorIndex, report func(VectorSyncEvent), verify bool) (windowsCollection, error) {
	current, err := s.currentWindowsCollection(ctx)
	if err != nil {
		return windowsCollection{}, err
	}
	var identity string
	err = s.db.QueryRowContext(ctx, "SELECT identity FROM video_windows ORDER BY rowid DESC LIMIT 1").Scan(&identity)
	if errors.Is(err, sql.ErrNoRows) {
		if current.name == "" {
			return windowsCollection{}, nil
		}
		if err := index.DeleteCollection(ctx, current.name); err != nil {
			return windowsCollection{}, err
		}
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
	var size int
	if err := s.db.QueryRowContext(ctx, "SELECT length(vector) FROM video_windows WHERE identity = ? LIMIT 1", identity).Scan(&size); err != nil {
		return windowsCollection{}, err
	}
	fresh := windowsCollection{name: windowsCollectionName(identity, s.randomHex()[:8]), identity: identity}
	// No graph: a few thousand points per video, scanned exactly.
	if err := index.CreateCollection(ctx, vectorindex.Collection{Name: fresh.name, Dimensions: size / 4,
		Keywords: []string{objectField, librariesField}}); err != nil {
		return windowsCollection{}, err
	}
	for _, name := range names {
		if strings.HasPrefix(name, windowsCollectionPrefix) {
			if err := index.DeleteCollection(ctx, name); err != nil {
				return windowsCollection{}, err
			}
		}
	}
	var queued int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO vector_index_state(name, collection, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET collection = excluded.collection, model = excluded.model, created_at = excluded.created_at`,
			windowsIndex, fresh.name, fresh.identity, formatTime(s.now())); err != nil {
			return err
		}
		// The WHERE keeps SQLite from reading ON CONFLICT as part of the
		// SELECT's join.
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
	s.windowsChecked.Store(&fresh)
	if report != nil {
		report(VectorSyncEvent{Kind: "created", Collection: fresh.name, Queued: int(queued)})
	}
	return fresh, nil
}

// windowPoints reads the points object should have in collection: one per
// window of its identity, scoped to the libraries with an untrashed asset of
// it; none when there is no such asset.
func (s *Service) windowPoints(ctx context.Context, object, identity string) ([]vectorindex.Point, error) {
	libraries, err := s.textColumn(ctx, "SELECT DISTINCT library_id FROM assets WHERE object_id = ? AND trashed_at IS NULL ORDER BY library_id", object)
	if err != nil || len(libraries) == 0 {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT idx, vector FROM video_windows WHERE object_id = ? AND identity = ? ORDER BY idx", object, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []vectorindex.Point
	for rows.Next() {
		var idx int
		var vector []byte
		if err := rows.Scan(&idx, &vector); err != nil {
			return nil, err
		}
		points = append(points, vectorindex.Point{Key: object + "#" + strconv.Itoa(idx), Vector: decodeVector(vector),
			Keywords: map[string][]string{objectField: {object}, librariesField: libraries}})
	}
	return points, rows.Err()
}

// SyncWindows brings a batch of queued videos' window points in line and
// reports whether there was anything to do.
func (s *Service) SyncWindows(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) (worked bool, err error) {
	var waiting bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM window_outbox)").Scan(&waiting); err != nil || !waiting {
		return false, err
	}
	collection, err := s.windowsCollection(ctx, index, report, false)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			s.windowsChecked.Store(nil)
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
			points, err := s.windowPoints(ctx, object, collection.identity)
			if err != nil {
				return false, err
			}
			refused, err := upsertPoints(ctx, index, collection.name, points)
			if err != nil {
				return false, err
			}
			upserted, rejected = upserted+len(points)-refused, rejected+refused
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
	return true, nil
}

// ReconcileWindows makes the windows collection again, from the Catalog,
// when the index lost it.
func (s *Service) ReconcileWindows(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) error {
	_, err := s.windowsCollection(ctx, index, report, true)
	return err
}

// queuedWindow is a video waiting in window_outbox.
type queuedWindow struct {
	object  string
	version int64
}

func (s *Service) queuedWindows(ctx context.Context) ([]queuedWindow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, version FROM window_outbox ORDER BY object_id LIMIT ?", windowSyncBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []queuedWindow
	for rows.Next() {
		var q queuedWindow
		if err := rows.Scan(&q.object, &q.version); err != nil {
			return nil, err
		}
		batch = append(batch, q)
	}
	return batch, rows.Err()
}

// textColumn reads one text column of a query's rows.
func (s *Service) textColumn(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
