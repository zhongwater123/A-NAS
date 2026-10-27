package photos

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// The vector index (docs/architecture/vector-index.md, ADR 0028) mirrors the
// Catalog's image vectors in the photo service's own Qdrant instance. The
// Catalog stays the only record: every change to a vector, or to which
// libraries hold an untrashed photo of an object, queues the object in
// vector_outbox in the same transaction (migration 14's triggers), and the
// sync loop below brings the index in line. Losing the index loses nothing;
// the loop builds a new one from the Catalog. In this first stage search
// still ranks with the exact scan (search.go); the index answers only shadow
// queries that measure how far it agrees.

// VectorIndex is the photo service's own vector index; *vectorindex.Client
// implements it.
type VectorIndex interface {
	Collections(ctx context.Context) ([]string, error)
	CreateCollection(ctx context.Context, collection vectorindex.Collection) error
	DeleteCollection(ctx context.Context, name string) error
	Upsert(ctx context.Context, collection string, points []vectorindex.Point) error
	Delete(ctx context.Context, collection string, keys []string) error
	Search(ctx context.Context, collection string, query vectorindex.Query) ([]vectorindex.Hit, error)
	Scan(ctx context.Context, collection string, fields []string, visit func(vectorindex.Entry) error) error
}

const (
	// imagesIndex names the index of image vectors in vector_index_state.
	imagesIndex = "images"
	// imagesCollectionPrefix starts the name of every collection of image
	// vectors; the rest of the name says whose vectors it holds.
	imagesCollectionPrefix = "images_v1_"
	// librariesField holds the libraries with an untrashed photo of the
	// object, which every search is scoped to.
	librariesField = "libraries"
	// vectorSyncBatch is how many queued objects one round brings in line.
	vectorSyncBatch = 256
	// vectorReconcileInterval is how often the whole index is compared with
	// the Catalog.
	vectorReconcileInterval = 24 * time.Hour
)

// imagesCollection is the collection of image vectors and the model whose
// vectors it holds.
type imagesCollection struct {
	name, model string
}

// imagesCollectionName names a new collection for a model's vectors: the
// model's hash says which vectors it holds and a random part which Catalog
// built it, so that a Catalog from another volume or an older snapshot never
// adopts it.
func imagesCollectionName(model, nonce string) string {
	sum := sha256.Sum256([]byte(model))
	return imagesCollectionPrefix + hex.EncodeToString(sum[:4]) + "_" + nonce
}

// VectorSyncEvent is what a sync round or a reconciliation did, for the
// service's log.
type VectorSyncEvent struct {
	// Kind is "created" for a new collection, which queues every vector,
	// "synced" for a round that brought objects in line, or "reconciled".
	Kind       string
	Collection string
	Upserted   int
	Deleted    int
	// Queued counts the objects a new collection or a reconciliation found
	// out of line and queued again.
	Queued int
}

// RunVectorSync keeps index in line with the Catalog until ctx ends, a batch
// at a time while gate allows background work, and compares the whole index
// with the Catalog once it has caught up and daily after that.
func (s *Service) RunVectorSync(ctx context.Context, index VectorIndex, gate Gate, idle time.Duration,
	report func(VectorSyncEvent), logError func(error)) {
	var reconciled time.Time
	for {
		wait := idle
		if open, reason := gate.Open(ctx); !open {
			if pausedByPressure(reason) {
				wait = min(wait, pressureRecheck)
			}
		} else {
			worked, err := s.SyncVectors(ctx, index, report)
			switch {
			case err != nil:
				if ctx.Err() == nil && logError != nil {
					logError(err)
				}
			case worked:
				continue
			case s.now().Sub(reconciled) >= vectorReconcileInterval:
				if err := s.ReconcileVectors(ctx, index, report); err != nil {
					if ctx.Err() == nil && logError != nil {
						logError(err)
					}
				} else {
					reconciled = s.now()
					continue
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// SyncVectors brings one batch of queued objects in line, first making a
// collection when there is none for the current model, and reports whether
// there was anything to do. With nothing queued it leaves the index alone,
// so that the instance can stop while the library is unchanged; a lost
// index is then found by ReconcileVectors.
func (s *Service) SyncVectors(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) (bool, error) {
	var waiting bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM vector_outbox)").Scan(&waiting); err != nil || !waiting {
		return false, err
	}
	collection, created, err := s.imagesCollection(ctx, index, report)
	if err != nil || collection.name == "" {
		return created, err
	}
	type queued struct {
		object  string
		version int64
	}
	var batch []queued
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, version FROM vector_outbox ORDER BY object_id LIMIT ?", vectorSyncBatch)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var q queued
		if err := rows.Scan(&q.object, &q.version); err != nil {
			_ = rows.Close()
			return false, err
		}
		batch = append(batch, q)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(batch) == 0 {
		return created, err
	}
	objects := make([]string, len(batch))
	for i, q := range batch {
		objects[i] = q.object
	}
	// The Catalog is read and released before the index is called: it has
	// one connection, and a cold instance takes seconds to answer.
	points, err := s.vectorPoints(ctx, objects, collection.model)
	if err != nil {
		return false, err
	}
	var upserts []vectorindex.Point
	var deletes []string
	for _, object := range objects {
		if point, ok := points[object]; ok {
			upserts = append(upserts, point)
		} else {
			deletes = append(deletes, object)
		}
	}
	if err := index.Upsert(ctx, collection.name, upserts); err != nil {
		return false, err
	}
	if err := index.Delete(ctx, collection.name, deletes); err != nil {
		return false, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, q := range batch {
			if _, err := tx.ExecContext(ctx, "DELETE FROM vector_outbox WHERE object_id = ? AND version = ?", q.object, q.version); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if report != nil {
		report(VectorSyncEvent{Kind: "synced", Collection: collection.name, Upserted: len(upserts), Deleted: len(deletes)})
	}
	return true, nil
}

// vectorPoints reads what the index should hold for objects: the point of
// each that has a vector of model and an untrashed photo in some library.
// The others should have no point.
func (s *Service) vectorPoints(ctx context.Context, objects []string, model string) (map[string]vectorindex.Point, error) {
	args := []any{embeddingDerivation, model}
	for _, object := range objects {
		args = append(args, object)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.object_id, e.vector, a.library_id FROM embeddings e
JOIN assets a ON a.object_id = e.object_id AND a.trashed_at IS NULL
WHERE e.derivation = ? AND e.model = ? AND `+notVideo("e.object_id")+` AND e.object_id IN (`+placeholders(len(objects))+`)
ORDER BY e.object_id, a.library_id`, args...)
	if err != nil {
		return nil, err
	}
	points := make(map[string]vectorindex.Point)
	for rows.Next() {
		var object, library string
		var vector []byte
		if err := rows.Scan(&object, &vector, &library); err != nil {
			_ = rows.Close()
			return nil, err
		}
		point, ok := points[object]
		if !ok {
			point = vectorindex.Point{Key: object, Vector: decodeVector(vector), Keywords: map[string][]string{librariesField: nil}}
		}
		if libraries := point.Keywords[librariesField]; !slices.Contains(libraries, library) {
			point.Keywords[librariesField] = append(libraries, library)
		}
		points[object] = point
	}
	return points, errors.Join(rows.Err(), rows.Close())
}

// imagesCollection returns the collection that holds the current model's
// vectors, making it, and queueing every vector for it, when the index has
// none: on the first sync, after the instance lost its storage, when another
// Catalog made the one there, and when the model changes. The model is that
// of the newest vector; with none there is nothing to index.
func (s *Service) imagesCollection(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) (imagesCollection, bool, error) {
	var current imagesCollection
	err := s.db.QueryRowContext(ctx, "SELECT collection, model FROM vector_index_state WHERE name = ?", imagesIndex).
		Scan(&current.name, &current.model)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return imagesCollection{}, false, err
	}
	var model string
	var size int
	err = s.db.QueryRowContext(ctx, "SELECT model, length(vector) FROM embeddings WHERE derivation = ? ORDER BY created_at DESC, object_id LIMIT 1",
		embeddingDerivation).Scan(&model, &size)
	if errors.Is(err, sql.ErrNoRows) {
		// No vectors, so nothing to index: an earlier collection held only
		// photos deleted since, and what is queued needs no point.
		if current.name != "" {
			names, err := index.Collections(ctx)
			if err != nil {
				return imagesCollection{}, false, err
			}
			if slices.Contains(names, current.name) {
				if err := index.DeleteCollection(ctx, current.name); err != nil {
					return imagesCollection{}, false, err
				}
			}
		}
		return imagesCollection{}, false, s.ForgetVectorIndex(ctx)
	}
	if err != nil {
		return imagesCollection{}, false, err
	}
	names, err := index.Collections(ctx)
	if err != nil {
		return imagesCollection{}, false, err
	}
	if current.name != "" && current.model == model && slices.Contains(names, current.name) {
		return current, false, nil
	}

	fresh := imagesCollection{name: imagesCollectionName(model, s.randomHex()[:8]), model: model}
	// No graph: up to about 100,000 photos an exact scan in the index is as
	// fast as the in-process one (docs/research/qdrant-retrieval-features.md),
	// so building one would only cost the device processor and memory.
	if err := index.CreateCollection(ctx, vectorindex.Collection{Name: fresh.name, Dimensions: size / 4,
		Keywords: []string{librariesField}}); err != nil {
		return imagesCollection{}, false, err
	}
	// The instance is this service's alone, so any other collection of image
	// vectors is an earlier one of its own.
	for _, name := range names {
		if strings.HasPrefix(name, imagesCollectionPrefix) {
			if err := index.DeleteCollection(ctx, name); err != nil {
				return imagesCollection{}, false, err
			}
		}
	}
	var queued int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO vector_index_state(name, collection, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET collection = excluded.collection, model = excluded.model, created_at = excluded.created_at`,
			imagesIndex, fresh.name, fresh.model, formatTime(s.now())); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO vector_outbox(object_id) SELECT object_id FROM embeddings WHERE derivation = ?
ON CONFLICT(object_id) DO UPDATE SET version = version + 1`, embeddingDerivation)
		if err != nil {
			return err
		}
		queued, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return imagesCollection{}, false, err
	}
	if report != nil {
		report(VectorSyncEvent{Kind: "created", Collection: fresh.name, Queued: int(queued)})
	}
	return fresh, true, nil
}

// ReconcileVectors compares every point of the index with what the Catalog
// says it should hold and queues each object that differs. It catches what
// the outbox cannot see: an instance that lost its storage, as when a release
// brings another Qdrant, a Catalog restored from an older snapshot, or writes
// the instance lost in a power cut.
func (s *Service) ReconcileVectors(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) error {
	current, created, err := s.imagesCollection(ctx, index, report)
	if err != nil || created || current.name == "" {
		// A new collection has everything queued already.
		return err
	}
	want := make(map[string][]string)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT e.object_id, a.library_id FROM embeddings e
JOIN assets a ON a.object_id = e.object_id AND a.trashed_at IS NULL
WHERE e.derivation = ? AND e.model = ? AND `+notVideo("e.object_id")+`
ORDER BY e.object_id, a.library_id`, embeddingDerivation, current.model)
	if err != nil {
		return err
	}
	for rows.Next() {
		var object, library string
		if err := rows.Scan(&object, &library); err != nil {
			_ = rows.Close()
			return err
		}
		want[object] = append(want[object], library)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var differ []string
	seen := make(map[string]bool, len(want))
	err = index.Scan(ctx, current.name, []string{librariesField}, func(entry vectorindex.Entry) error {
		seen[entry.Key] = true
		have := slices.Clone(entry.Keywords[librariesField])
		slices.Sort(have)
		if !slices.Equal(have, want[entry.Key]) {
			differ = append(differ, entry.Key)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for object := range want {
		if !seen[object] {
			differ = append(differ, object)
		}
	}
	slices.SortFunc(differ, cmp.Compare[string])
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, object := range differ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO vector_outbox(object_id) VALUES(?)
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
		report(VectorSyncEvent{Kind: "reconciled", Collection: current.name, Queued: len(differ)})
	}
	return nil
}

// ForgetVectorIndex drops what the Catalog keeps for an index, when the
// service runs without one: the queue would only grow, and an index turned on
// later is built anew.
func (s *Service) ForgetVectorIndex(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM vector_outbox"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM vector_index_state")
		return err
	})
}
