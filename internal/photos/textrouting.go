package photos

import (
	"context"
	"database/sql"
	"strings"
	"sync"
)

// Text routing (docs/architecture/photo-ocr.md#哪些照片读文字): reading a
// photo's text costs the NAS about two minutes, so a photo is queued for it
// (textDerivation) only when its vector comes close enough to one of the text
// route's queries: documents, receipts, cards, screenshots. The vectors are
// the ones search already keeps, so routing adds no model. New vectors are
// routed as they are stored; the ones stored earlier, before the queries had
// vectors or under another route, once per process. Queueing twice is
// harmless: a job already queued stays, and a photo already read is left.
const textDerivation = "ocr/v1"

// routeIndex holds the route's query vectors once every one is in the
// Catalog, and whether the stored vectors were routed in this process.
type routeIndex struct {
	mu      sync.Mutex
	vectors [][]float32
	routed  bool
}

// routeVectors returns the route's query vectors, or nil while any query
// still lacks one. It needs no Worker: the vectors come from the Catalog.
func (s *Service) routeVectors(ctx context.Context) ([][]float32, error) {
	s.routeIndex.mu.Lock()
	defer s.routeIndex.mu.Unlock()
	if s.routeIndex.vectors != nil {
		return s.routeIndex.vectors, nil
	}
	have, err := s.queryVectors(ctx, s.textRoute.Model)
	if err != nil {
		return nil, err
	}
	vectors := make([][]float32, 0, len(s.textRoute.Queries))
	for _, query := range s.textRoute.Queries {
		vector, ok := have[query]
		if !ok {
			return nil, nil
		}
		vectors = append(vectors, vector)
	}
	s.routeIndex.vectors = vectors
	return vectors, nil
}

// hasText reports whether a photo's vector routes it to be read.
func (s *Service) hasText(vector []float32, queries [][]float32) bool {
	for _, query := range queries {
		if dot(vector, query) >= s.textRoute.Threshold {
			return true
		}
	}
	return false
}

// textQueries returns the route's query vectors when a vector of model is
// routed: the route is calibrated for model and its queries are ready.
func (s *Service) textQueries(ctx context.Context, model string) ([][]float32, error) {
	if model != s.textRoute.Model {
		return nil, nil
	}
	return s.routeVectors(ctx)
}

// enqueueText queues the reading of an object's text, unless it was read.
func enqueueText(ctx context.Context, tx *sql.Tx, objectID, now string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT ?, ?, 'pending', ?, ?
WHERE NOT EXISTS (SELECT 1 FROM scans WHERE object_id = ? AND derivation = ?) AND `+inLocalAI("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, textDerivation, now, now, objectID, textDerivation, objectID)
	return err
}

// routeStoredVectors queues every stored vector of model that the route
// sends to be read, once per process, and reports whether it ran. Vectors
// stored before the route's queries had theirs were not routed then.
func (s *Service) routeStoredVectors(ctx context.Context, model string) (bool, error) {
	s.routeIndex.mu.Lock()
	routed := s.routeIndex.routed
	s.routeIndex.mu.Unlock()
	if routed {
		return false, nil
	}
	queries, err := s.textQueries(ctx, model)
	if err != nil || queries == nil {
		return false, err
	}
	var chosen []string
	if err := s.index.read(ctx, s, model, func(vectors map[string][]float32) {
		for objectID, vector := range vectors {
			if s.hasText(vector, queries) {
				chosen = append(chosen, objectID)
			}
		}
	}); err != nil {
		return false, err
	}
	now := formatTime(s.now())
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, objectID := range chosen {
			if err := enqueueText(ctx, tx, objectID, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	s.routeIndex.mu.Lock()
	s.routeIndex.routed = true
	s.routeIndex.mu.Unlock()
	return true, nil
}

// textPhotos counts the originals in libraries that the route sends to be
// read, queued or read, or returns nil while the route does not apply to
// model or its queries wait for the Worker. Like AIStatus it needs no Worker.
func (s *Service) textPhotos(ctx context.Context, model string, libraries []Library) (*int, error) {
	if model != "" && model != s.textRoute.Model {
		return nil, nil
	}
	queries, err := s.routeVectors(ctx)
	if err != nil || queries == nil {
		return nil, err
	}
	count := 0
	if len(libraries) == 0 {
		return &count, nil
	}
	args := make([]any, 0, len(libraries)+2)
	for _, lib := range libraries {
		args = append(args, lib.ID)
	}
	args = append(args, textDerivation, textDerivation)
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (SELECT DISTINCT object_id FROM assets
  WHERE trashed_at IS NULL AND library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(libraries)), ",")+`)
    AND `+inLocalAI("assets.object_id")+`) a
WHERE EXISTS (SELECT 1 FROM jobs WHERE object_id = a.object_id AND derivation = ?)
   OR EXISTS (SELECT 1 FROM scans WHERE object_id = a.object_id AND derivation = ?)`, args...).Scan(&count)
	if err != nil {
		return nil, err
	}
	return &count, nil
}
