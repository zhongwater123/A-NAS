package photos

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// Checking the read path (docs/architecture/vector-index.md#读路径): now that
// ranking reads the vector index, a sample of searches is asked again in the
// background, once of the index and once of the Catalog alone, and the two
// rankings are compared. Both score exactly, so any share below 1 means the
// index chose objects it should not have, or missed some: it is out of line
// with the Catalog. Search hands over the query without waiting; a busy
// worker, or one that checked too recently, drops it. The check reads every
// candidate's vector from the Catalog, so it runs rarely.

// checkLimit is how many photos each ranking holds, as many as search ranks
// by similarity.
const checkLimit = maxSemanticResults

// checkCuts are the ranks at which the two answers are compared.
var checkCuts = []int{10, 100, 500}

// checkTie is how close to the score at a cut a photo counts as within it:
// photos that score alike may come in either order.
const checkTie = 1e-5

// VectorCheck is how the index's answer to one search compared with the
// Catalog's, both ranking the content objects in the search's scope by
// similarity alone. It holds no query, name or photo.
type VectorCheck struct {
	Collection string
	// Candidates counts the objects with a vector in the search's scope.
	Candidates int
	// Overlap maps each cut k to the share of the index's top k that the
	// Catalog ranks within its top k, ties at the cut included.
	Overlap map[int]float64
	// Index is how long the index took to choose. Warm says whether a
	// connection to it was open: the instance stops a minute after the last
	// connection closes, so a check that is not Warm includes its start.
	Index time.Duration
	Warm  bool
	// Exact is how long scoring every candidate from the Catalog took.
	Exact time.Duration
}

// vectorCheckQuery is a search handed to the check worker.
type vectorCheckQuery struct {
	vector  []float32
	model   string
	scope   []string
	objects []string
}

// checkSearch hands a search to the check worker if it is running and free.
func (s *Service) checkSearch(vector []float32, model string, scope []string, objects []string) {
	if vector == nil || !s.checking.Load() {
		return
	}
	select {
	case s.check <- vectorCheckQuery{vector: vector, model: model, scope: scope, objects: objects}:
	default:
	}
}

// RunVectorCheck compares the searches handed to it until ctx ends, at most
// one every interval.
func (s *Service) RunVectorCheck(ctx context.Context, every time.Duration, report func(VectorCheck), logError func(error)) {
	s.checking.Store(true)
	defer s.checking.Store(false)
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case query := <-s.check:
			if !last.IsZero() && s.now().Sub(last) < every {
				continue
			}
			check, checked, err := s.checkWithCatalog(ctx, query)
			if err != nil {
				if ctx.Err() == nil && logError != nil {
					logError(err)
				}
				continue
			}
			if checked {
				last = s.now()
				if report != nil {
					report(check)
				}
			}
		}
	}
}

// checkWithCatalog compares one search, unless the index cannot answer it.
func (s *Service) checkWithCatalog(ctx context.Context, query vectorCheckQuery) (VectorCheck, bool, error) {
	collection, err := s.currentImagesCollection(ctx)
	if err != nil || collection.name == "" || collection.model != query.model {
		return VectorCheck{}, false, err
	}
	q := similarityQuery{model: query.model, vector: query.vector, scope: query.scope, objects: query.objects, keep: checkLimit}
	fromIndex, read, ok, err := s.indexSimilarities(ctx, q)
	if err != nil || !ok {
		return VectorCheck{}, false, err
	}
	wanted := make(map[string]bool, len(query.objects))
	for _, object := range query.objects {
		wanted[object] = true
	}
	started := time.Now()
	fromCatalog, err := s.catalogSimilarities(ctx, query.model, query.vector, wanted)
	if err != nil {
		return VectorCheck{}, false, err
	}
	check := VectorCheck{Collection: collection.name, Candidates: len(fromCatalog), Overlap: make(map[int]float64, len(checkCuts)),
		Index: read.took, Warm: read.warm, Exact: time.Since(started)}
	exact := ranked(fromCatalog)
	chosen := ranked(fromIndex)
	for _, cut := range checkCuts {
		want := min(cut, len(exact))
		if want == 0 {
			check.Overlap[cut] = 1
			continue
		}
		floor := exact[want-1].score - checkTie
		found := 0
		for _, hit := range chosen[:min(want, len(chosen))] {
			if score, ok := fromCatalog[hit.object]; ok && score >= floor {
				found++
			}
		}
		check.Overlap[cut] = float64(found) / float64(want)
	}
	return check, true, nil
}

type scoredObject struct {
	object string
	score  float64
}

// ranked orders scores best first, then by object.
func ranked(scores map[string]float64) []scoredObject {
	list := make([]scoredObject, 0, len(scores))
	for object, score := range scores {
		list = append(list, scoredObject{object, score})
	}
	slices.SortFunc(list, func(a, b scoredObject) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.object, b.object))
	})
	return list
}
