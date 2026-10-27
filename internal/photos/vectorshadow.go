package photos

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// Shadow comparison (docs/architecture/vector-index.md#影子比对): after a
// search, the shadow worker asks the vector index the same question and
// compares its answer with the exact scan's. Search hands the query over
// without waiting and never touches the index itself; a busy worker drops
// the comparison. Whether search moves to the index is decided on what these
// comparisons show on the device.

// shadowLimit is how many photos the index is asked for, as many as search
// ranks by similarity.
const shadowLimit = maxSemanticResults

// shadowCuts are the ranks at which the two answers are compared.
var shadowCuts = []int{10, 100, 500}

// shadowTie is how close to the score at a cut a photo counts as within it:
// photos that score alike may come in either order.
const shadowTie = 1e-5

// ShadowComparison is how the index's answer to one search compared with the
// exact scan's, both ranking the content objects in the search's scope by
// similarity alone. The index scans exactly too, so any share below 1 means
// it is out of line with the Catalog, not an approximation
// (docs/research/qdrant-retrieval-features.md). It holds no query, name or
// photo.
type ShadowComparison struct {
	Collection string
	// Candidates counts the objects with a vector in the search's scope.
	Candidates int
	// Overlap maps each cut k to the share of the index's top k that the
	// exact scan ranks within its top k, ties at the cut included.
	Overlap map[int]float64
	// Index is how long the index took; Warm whether a connection to it was
	// open before, or the call had to wait for the instance to start.
	Index time.Duration
	Warm  bool
	// Exact is how long the exact ranking took, candidates read included.
	Exact time.Duration
}

type shadowQuery struct {
	vector []float32
	model  string
	scope  []string
}

// shadowSearch hands a search to the shadow worker if one runs and is free.
func (s *Service) shadowSearch(vector []float32, model string, scope []any) {
	if !s.shadowing.Load() {
		return
	}
	libraries := make([]string, len(scope))
	for i, library := range scope {
		libraries[i], _ = library.(string)
	}
	select {
	case s.shadow <- shadowQuery{vector: vector, model: model, scope: libraries}:
	default:
	}
}

// RunVectorShadow compares the searches handed to it with index until ctx
// ends. warm reports whether a connection to the index is open.
func (s *Service) RunVectorShadow(ctx context.Context, index VectorIndex, warm func() bool,
	report func(ShadowComparison), logError func(error)) {
	s.shadowing.Store(true)
	defer s.shadowing.Store(false)
	for {
		select {
		case <-ctx.Done():
			return
		case query := <-s.shadow:
			comparison, compared, err := s.compareWithIndex(ctx, index, warm, query)
			if err != nil && ctx.Err() == nil && logError != nil {
				logError(err)
			}
			if compared && report != nil {
				report(comparison)
			}
		}
	}
}

// compareWithIndex compares one search, unless the index is behind the
// Catalog or holds another model's vectors.
func (s *Service) compareWithIndex(ctx context.Context, index VectorIndex, warm func() bool, query shadowQuery) (ShadowComparison, bool, error) {
	var behind bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM vector_outbox)").Scan(&behind); err != nil || behind {
		return ShadowComparison{}, false, err
	}
	var collection imagesCollection
	err := s.db.QueryRowContext(ctx, "SELECT collection, model FROM vector_index_state WHERE name = ?", imagesIndex).
		Scan(&collection.name, &collection.model)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && collection.model != query.model) {
		return ShadowComparison{}, false, nil
	}
	if err != nil {
		return ShadowComparison{}, false, err
	}

	comparison := ShadowComparison{Collection: collection.name, Overlap: make(map[int]float64, len(shadowCuts))}
	if warm != nil {
		comparison.Warm = warm()
	}
	started := time.Now()
	hits, err := index.Search(ctx, collection.name, vectorindex.Query{Vector: query.vector,
		Scope: vectorindex.Scope{Field: librariesField, Values: query.scope}, Limit: shadowLimit, Exact: true})
	if err != nil {
		return ShadowComparison{}, false, err
	}
	comparison.Index = time.Since(started)

	started = time.Now()
	scope := make([]any, len(query.scope))
	for i, library := range query.scope {
		scope[i] = library
	}
	candidates, err := s.searchCandidates(ctx, scope)
	if err != nil {
		return ShadowComparison{}, false, err
	}
	type scored struct {
		object string
		score  float64
	}
	var exact []scored
	err = s.index.read(ctx, s, collection.model, func(vectors map[string][]float32) {
		seen := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			vector, ok := vectors[c.objectID]
			if !ok || c.video || seen[c.objectID] {
				continue
			}
			seen[c.objectID] = true
			exact = append(exact, scored{c.objectID, dot(vector, query.vector)})
		}
	})
	if err != nil {
		return ShadowComparison{}, false, err
	}
	slices.SortFunc(exact, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.object, b.object))
	})
	comparison.Exact = time.Since(started)
	comparison.Candidates = len(exact)

	scores := make(map[string]float64, len(exact))
	for _, e := range exact {
		scores[e.object] = e.score
	}
	for _, cut := range shadowCuts {
		want := min(cut, len(exact))
		if want == 0 {
			comparison.Overlap[cut] = 1
			continue
		}
		floor := exact[want-1].score - shadowTie
		found := 0
		for _, hit := range hits[:min(want, len(hits))] {
			if score, ok := scores[hit.Key]; ok && score >= floor {
				found++
			}
		}
		comparison.Overlap[cut] = float64(found) / float64(want)
	}
	return comparison, true, nil
}
