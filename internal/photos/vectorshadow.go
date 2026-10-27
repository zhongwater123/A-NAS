package photos

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// Shadow comparison (docs/architecture/vector-index.md#影子比对): after a
// search, the shadow worker asks the vector index the same question and
// compares its answer with the exact scan's. Search hands over the query, the
// content objects it ranked and how long its own scan took, without waiting
// and without touching the index; a busy worker drops the comparison. The
// worker reads no candidates from the Catalog, whose one connection the next
// requests are waiting for. Whether search moves to the index is decided on
// what these comparisons show on the device.

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
	// Index is how long the index took. Warm says whether a connection to it
	// was open: the instance stops seconds after the last connection closes,
	// so a comparison that is not Warm includes the instance's start.
	Index time.Duration
	Warm  bool
	// Exact is how long ranking the same objects by their in-memory vectors
	// took, the dot products and the sort: the work the index's exact search
	// does, measured the same way.
	Exact time.Duration
}

// shadowQuery is a search handed to the shadow worker: its query vector and
// model, the libraries it covered and the content objects it ranked.
type shadowQuery struct {
	vector  []float32
	model   string
	scope   []string
	objects []string
}

// newShadowQuery prepares a search for comparison while a shadow worker runs;
// nil means the search is not compared.
func (s *Service) newShadowQuery(vector []float32, model string, scope []any) *shadowQuery {
	if vector == nil || !s.shadowing.Load() {
		return nil
	}
	libraries := make([]string, len(scope))
	for i, library := range scope {
		id, ok := library.(string)
		if !ok {
			return nil
		}
		libraries[i] = id
	}
	return &shadowQuery{vector: vector, model: model, scope: libraries}
}

// shadowSearch hands a search to the shadow worker if it is free.
func (s *Service) shadowSearch(query *shadowQuery) {
	if query == nil {
		return
	}
	select {
	case s.shadow <- *query:
	default:
	}
}

// RunVectorShadow compares the searches handed to it with index until ctx
// ends. warm reports, without waiting, whether a connection to the index is
// open. After a comparison fails it compares nothing for a while, longer
// after each failure in a row, so that searches against a broken index do not
// start its instance each time.
func (s *Service) RunVectorShadow(ctx context.Context, index VectorIndex, warm func() bool, idle time.Duration,
	report func(ShadowComparison), logError func(error)) {
	s.shadowing.Store(true)
	defer s.shadowing.Store(false)
	failures := 0
	var resume time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case query := <-s.shadow:
			if time.Now().Before(resume) {
				continue
			}
			comparison, compared, err := s.compareWithIndex(ctx, index, warm, query)
			if err != nil {
				failures++
				resume = time.Now().Add(vectorSyncBackoff(idle, failures))
				if ctx.Err() == nil && logError != nil {
					logError(err)
				}
				continue
			}
			failures = 0
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
	collection, err := s.currentImagesCollection(ctx)
	if err != nil || collection.name == "" || collection.model != query.model {
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

	type scored struct {
		object string
		score  float64
	}
	var exact []scored
	err = s.index.read(ctx, s, collection.model, func(vectors map[string][]float32) {
		started := time.Now()
		seen := make(map[string]bool, len(query.objects))
		for _, object := range query.objects {
			vector, ok := vectors[object]
			if !ok || seen[object] {
				continue
			}
			seen[object] = true
			exact = append(exact, scored{object, dot(vector, query.vector)})
		}
		slices.SortFunc(exact, func(a, b scored) int {
			return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.object, b.object))
		})
		comparison.Exact = time.Since(started)
	})
	if err != nil {
		return ShadowComparison{}, false, err
	}
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
