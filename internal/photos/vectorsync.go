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
	// vectorSyncMaxBackoff bounds how long sync waits after failures in a
	// row; the wait doubles from the idle interval up to it, so an index that
	// keeps failing is not kept running by retries.
	vectorSyncMaxBackoff = 30 * time.Minute
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
	// Rejected counts the objects the index refused; they leave the queue
	// so that they do not hold up the rest, and the next reconciliation
	// tries them again.
	Rejected int
	// Queued counts the objects a new collection or a reconciliation found
	// out of line and queued again.
	Queued int
}

// vectorSyncBackoff is how long sync waits after failures in a row.
func vectorSyncBackoff(idle time.Duration, failures int) time.Duration {
	wait := idle
	for range failures - 1 {
		if wait >= vectorSyncMaxBackoff/2 {
			return vectorSyncMaxBackoff
		}
		wait *= 2
	}
	return min(wait, vectorSyncMaxBackoff)
}

// RunVectorSync keeps index in line with the Catalog until ctx ends, a batch
// at a time while gate allows background work, and compares the whole index
// with the Catalog once it has caught up and daily after that.
func (s *Service) RunVectorSync(ctx context.Context, index VectorIndex, gate Gate, idle time.Duration,
	report func(VectorSyncEvent), logError func(error)) {
	var reconciled time.Time
	failures := 0
	failed := func(err error) {
		failures++
		if ctx.Err() == nil && logError != nil {
			logError(err)
		}
	}
	for {
		wait := idle
		if open, reason := gate.Open(ctx, 0); !open {
			if pausedByPressure(reason) {
				wait = min(wait, pressureRecheck)
			}
		} else {
			worked, err := s.SyncVectors(ctx, index, report)
			switch {
			case err != nil:
				failed(err)
			case worked:
				failures = 0
				continue
			case s.now().Sub(reconciled) >= vectorReconcileInterval:
				if err := s.ReconcileVectors(ctx, index, report); err != nil {
					failed(err)
				} else {
					failures, reconciled = 0, s.now()
					continue
				}
			}
			if failures > 0 {
				wait = vectorSyncBackoff(idle, failures)
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
func (s *Service) SyncVectors(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) (worked bool, err error) {
	var waiting bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM vector_outbox)").Scan(&waiting); err != nil || !waiting {
		return false, err
	}
	collection, created, err := s.imagesCollection(ctx, index, report, false)
	if err != nil || collection.name == "" {
		return created, err
	}
	// Whatever fails below is checked against the instance again next time:
	// it may have lost the collection.
	defer func() {
		if err != nil {
			s.vectorChecked.Store(nil)
		}
	}()
	type queued struct {
		object  string
		version int64
	}
	var batch []queued
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, version FROM vector_outbox ORDER BY object_id LIMIT ?", vectorSyncBatch)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var q queued
		if err := rows.Scan(&q.object, &q.version); err != nil {
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
	rejected, err := upsertPoints(ctx, index, collection.name, upserts)
	if err != nil {
		return false, err
	}
	if err = index.Delete(ctx, collection.name, deletes); err != nil {
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
		report(VectorSyncEvent{Kind: "synced", Collection: collection.name, Upserted: len(upserts) - rejected,
			Deleted: len(deletes), Rejected: rejected})
	}
	return true, nil
}

// upsertPoints stores points and counts those the index refused. When it
// refuses a batch, each point is tried alone, so that one it will never take
// does not hold up the others.
func upsertPoints(ctx context.Context, index VectorIndex, collection string, points []vectorindex.Point) (int, error) {
	err := index.Upsert(ctx, collection, points)
	if !errors.Is(err, vectorindex.ErrRejected) {
		return 0, err
	}
	rejected := 0
	for _, point := range points {
		err := index.Upsert(ctx, collection, []vectorindex.Point{point})
		switch {
		case errors.Is(err, vectorindex.ErrRejected):
			rejected++
		case err != nil:
			return 0, err
		}
	}
	return rejected, nil
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
WHERE e.derivation = ? AND e.model = ? AND `+inLocalAI("e.object_id")+` AND e.object_id IN (`+placeholders(len(objects))+`)
ORDER BY e.object_id, a.library_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make(map[string]vectorindex.Point)
	for rows.Next() {
		var object, library string
		var vector []byte
		if err := rows.Scan(&object, &vector, &library); err != nil {
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

// currentImagesCollection is the collection the Catalog records for image
// vectors, empty when there is none.
func (s *Service) currentImagesCollection(ctx context.Context) (imagesCollection, error) {
	var current imagesCollection
	err := s.db.QueryRowContext(ctx, "SELECT collection, model FROM vector_index_state WHERE name = ?", imagesIndex).
		Scan(&current.name, &current.model)
	if errors.Is(err, sql.ErrNoRows) {
		return imagesCollection{}, nil
	}
	return current, err
}

// imagesCollection returns the collection that holds the current model's
// vectors, making it, and queueing every vector for it, when the index has
// none: on the first sync, after the instance lost its storage, when another
// Catalog made the one there, and when the model changes. The model is that
// of the newest vector; with none there is nothing to index.
//
// Asking the instance whether the collection is still there takes a call, so
// a collection seen there is trusted until a sync fails; verify asks anyway.
func (s *Service) imagesCollection(ctx context.Context, index VectorIndex, report func(VectorSyncEvent), verify bool) (imagesCollection, bool, error) {
	current, err := s.currentImagesCollection(ctx)
	if err != nil {
		return imagesCollection{}, false, err
	}
	var model string
	err = s.db.QueryRowContext(ctx, "SELECT model FROM embeddings WHERE derivation = ? ORDER BY created_at DESC, object_id LIMIT 1",
		embeddingDerivation).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return imagesCollection{}, false, s.dropImagesCollection(ctx, index, current)
	}
	if err != nil {
		return imagesCollection{}, false, err
	}
	if checked := s.vectorChecked.Load(); !verify && checked != nil && *checked == current && current.model == model {
		return current, false, nil
	}
	names, err := index.Collections(ctx)
	if err != nil {
		return imagesCollection{}, false, err
	}
	if current.name != "" && current.model == model && slices.Contains(names, current.name) {
		s.vectorChecked.Store(&current)
		return current, false, nil
	}

	// The size every vector of the model has, but for a damaged row: the
	// most common one, not the newest row's, which may be the damaged one.
	var size int
	if err := s.db.QueryRowContext(ctx, `SELECT length(vector) FROM embeddings WHERE derivation = ? AND model = ?
GROUP BY length(vector) ORDER BY COUNT(*) DESC, length(vector) LIMIT 1`, embeddingDerivation, model).Scan(&size); err != nil {
		return imagesCollection{}, false, err
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
	s.vectorChecked.Store(&fresh)
	if report != nil {
		report(VectorSyncEvent{Kind: "created", Collection: fresh.name, Queued: int(queued)})
	}
	return fresh, true, nil
}

// dropImagesCollection forgets the index when the Catalog holds no vectors:
// an earlier collection held only photos deleted since, and what is queued
// needs no point. A vector stored meanwhile keeps its queue and the record,
// so that the next round makes a collection for it.
func (s *Service) dropImagesCollection(ctx context.Context, index VectorIndex, current imagesCollection) error {
	s.vectorChecked.Store(nil)
	if current.name != "" {
		names, err := index.Collections(ctx)
		if err != nil {
			return err
		}
		if slices.Contains(names, current.name) {
			if err := index.DeleteCollection(ctx, current.name); err != nil {
				return err
			}
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"vector_outbox", "vector_index_state"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE NOT EXISTS (SELECT 1 FROM embeddings WHERE derivation = ?)",
				embeddingDerivation); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReconcileVectors compares every point of the index with what the Catalog
// says it should hold and queues each object that differs. It catches what
// the outbox cannot see: an instance that lost its storage, as when a release
// brings another Qdrant, a Catalog restored from an older snapshot, or writes
// the instance lost in a power cut.
func (s *Service) ReconcileVectors(ctx context.Context, index VectorIndex, report func(VectorSyncEvent)) error {
	current, created, err := s.imagesCollection(ctx, index, report, true)
	if err != nil || created || current.name == "" {
		// A new collection has everything queued already.
		return err
	}
	want := make(map[string][]string)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT e.object_id, a.library_id FROM embeddings e
JOIN assets a ON a.object_id = e.object_id AND a.trashed_at IS NULL
WHERE e.derivation = ? AND e.model = ? AND `+inLocalAI("e.object_id")+`
ORDER BY e.object_id, a.library_id`, embeddingDerivation, current.model)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var object, library string
		if err := rows.Scan(&object, &library); err != nil {
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
	s.vectorChecked.Store(nil)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM vector_outbox"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM vector_index_state")
		return err
	})
}
