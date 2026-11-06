package photos

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// Ranking by similarity reads from the vector index
// (docs/architecture/vector-index.md#读路径, ADR 0028): the index picks the
// content objects that can rank, and those are then scored exactly from the
// Catalog's vectors, as search always scored them, so the results are the
// same as an exact scan of the Catalog. The library's vectors are not kept in
// memory.
//
// The Catalog stays the record. Objects the outbox still queues may be out of
// date in the index, so they are scored from the Catalog beside its answer.
// When the index cannot answer for the query's model (it has no collection
// for it yet, much is still queued, or it fails), the Catalog is read in
// batches instead: the same results, slower.

const (
	// similarityMargin covers the difference between the index's scores,
	// summed in float32, and the Catalog's exact ones in float64: anything
	// within it of a cut is scored exactly before it is placed.
	similarityMargin = 1e-4
	// maxOutboxDelta bounds the queued objects scored from the Catalog beside
	// the index's answer; with more queued, as while a new collection fills,
	// the Catalog answers alone.
	maxOutboxDelta = 2000
	// vectorReadBatch is how many vectors one Catalog read takes, so that the
	// Catalog's one connection is free for other requests in between.
	vectorReadBatch = 500
	// vectorFallbackLogEvery bounds how often one reason for answering from
	// the Catalog is reported.
	vectorFallbackLogEvery = 10 * time.Minute
)

// indexOverfetch is how many more objects than it needs the first question
// to the index asks for, so that it usually needs no second one; tests set
// it to 0 to make the second one happen.
var indexOverfetch = 64

// similarityQuery asks for exact similarities to a vector of a model.
type similarityQuery struct {
	model  string
	vector []float32
	// scope are the libraries searched.
	scope []string
	// objects are the content objects that may be ranked: those of the
	// candidates that take part in local AI.
	objects []string
	// always are objects whose similarity is needed whatever their rank, such
	// as the photos that match by name.
	always []string
	// keep is how many objects the caller ranks by similarity plus bonus.
	keep int
	// bonus is what the caller adds to an object's similarity, from 0 to
	// maxBonus; nil adds nothing.
	bonus    func(object string) float64
	maxBonus float64
	// direct scores the objects straight from the Catalog: they are a small
	// part of the scope, as when a search keeps to a person's photos.
	direct bool
}

// vectorRead says how a ranking was answered.
type vectorRead struct {
	// index is set when the index chose the objects.
	index bool
	// warm says whether a connection to the index was open when it was asked:
	// otherwise took includes the instance's start.
	warm bool
	took time.Duration
}

// similarities returns the exact similarity to q.vector of every object of
// q.always that has a vector of q.model, and of every object of q.objects
// that can rank among the first q.keep by similarity plus bonus. An object
// left out has no vector, or cannot rank there.
func (s *Service) similarities(ctx context.Context, q similarityQuery) (map[string]float64, vectorRead, error) {
	if q.vector == nil || q.model == "" {
		return map[string]float64{}, vectorRead{}, nil
	}
	wanted := make(map[string]bool, len(q.objects)+len(q.always))
	for _, object := range q.objects {
		wanted[object] = true
	}
	for _, object := range q.always {
		wanted[object] = true
	}
	if s.vectors != nil && !q.direct {
		sims, read, ok, err := s.indexSimilarities(ctx, q)
		if err != nil && ctx.Err() != nil {
			return nil, vectorRead{}, err
		}
		if ok {
			return sims, read, nil
		}
	}
	sims, err := s.catalogSimilarities(ctx, q.model, q.vector, wanted)
	return sims, vectorRead{}, err
}

// indexSimilarities answers q with the index; ok is false when the index
// cannot answer for the model, and the caller reads the Catalog instead.
func (s *Service) indexSimilarities(ctx context.Context, q similarityQuery) (sims map[string]float64, read vectorRead, ok bool, err error) {
	collection, queued, usable, err := s.readableCollection(ctx, q.model)
	if err != nil || !usable {
		return nil, vectorRead{}, false, err
	}
	allowed := make(map[string]bool, len(q.objects))
	for _, object := range q.objects {
		allowed[object] = true
	}
	bonus := q.bonus
	if bonus == nil {
		bonus = func(string) float64 { return 0 }
	}

	// What the outbox queues is scored from the Catalog: the index may hold an
	// older version of it, or nothing.
	var delta []string
	for object := range queued {
		if allowed[object] || slices.Contains(q.always, object) {
			delta = append(delta, object)
		}
	}
	exact := make(map[string]float64, len(delta)+q.keep)
	if err := s.scoreFromCatalog(ctx, q.model, q.vector, delta, exact); err != nil {
		return nil, vectorRead{}, false, err
	}

	read.warm = s.vectorsWarm()
	started := time.Now()
	ask := func(limit int, floor *float32) ([]vectorindex.Hit, error) {
		return s.vectors.Search(ctx, collection, vectorindex.Query{Vector: q.vector,
			Scope: vectorindex.Scope{Field: librariesField, Values: q.scope}, Limit: limit, Exact: true, Floor: floor})
	}
	// The objects the caller ranks may include always ones, which its
	// bands take out of the similarity ranking, so it needs that many more.
	want := q.keep + len(q.always)
	limit := want + len(delta) + indexOverfetch
	var hits []vectorindex.Hit
	for {
		if hits, err = ask(limit, nil); err != nil {
			s.reportVectorFallback("the vector index failed", err)
			return nil, vectorRead{}, false, err
		}
		if len(hits) < limit || countUsable(hits, allowed, queued) >= want {
			break
		}
		limit *= 4
	}

	// Approximate places: the index's float32 score plus the bonus, exact
	// for what was scored from the Catalog.
	type placed struct {
		object string
		score  float64
	}
	place := func(hits []vectorindex.Hit) []placed {
		ranked := make([]placed, 0, len(hits)+len(delta))
		for _, hit := range hits {
			if allowed[hit.Key] && !queued[hit.Key] {
				ranked = append(ranked, placed{hit.Key, float64(hit.Score) + bonus(hit.Key)})
			}
		}
		for _, object := range delta {
			if sim, ok := exact[object]; ok && allowed[object] {
				ranked = append(ranked, placed{object, sim + bonus(object)})
			}
		}
		slices.SortFunc(ranked, func(a, b placed) int { return cmp.Compare(b.score, a.score) })
		return ranked
	}
	cut := func(ranked []placed) float64 {
		if len(ranked) < want {
			return math.Inf(-1)
		}
		return ranked[want-1].score
	}
	ranked := place(hits)
	// A bonus can lift an object the index ranked lower above the cut: ask
	// again for everything that could reach it.
	if exhausted := len(hits) < limit; !exhausted {
		floor := float32(cut(ranked) - q.maxBonus - similarityMargin)
		if lowest := hits[len(hits)-1].Score; lowest >= floor {
			for limit = max(limit*2, 2*len(allowed)+64); ; limit *= 4 {
				if hits, err = ask(limit, &floor); err != nil {
					s.reportVectorFallback("the vector index failed", err)
					return nil, vectorRead{}, false, err
				}
				if len(hits) < limit {
					break
				}
			}
			ranked = place(hits)
		}
	}
	read.index, read.took = true, time.Since(started)

	// Exact scores for everything near enough to the cut, and for always.
	threshold := cut(ranked) - similarityMargin
	var pick []string
	for _, p := range ranked {
		if p.score < threshold {
			break
		}
		if _, done := exact[p.object]; !done {
			pick = append(pick, p.object)
		}
	}
	for _, object := range q.always {
		if _, done := exact[object]; !done && !slices.Contains(pick, object) {
			pick = append(pick, object)
		}
	}
	if err := s.scoreFromCatalog(ctx, q.model, q.vector, pick, exact); err != nil {
		return nil, vectorRead{}, false, err
	}
	sims = make(map[string]float64, len(pick)+len(delta))
	for _, p := range ranked {
		if p.score < threshold {
			break
		}
		if sim, ok := exact[p.object]; ok {
			sims[p.object] = sim
		}
	}
	for _, object := range q.always {
		if sim, ok := exact[object]; ok {
			sims[object] = sim
		}
	}
	return sims, read, true, nil
}

// countUsable counts the hits the caller may rank and that are not queued.
func countUsable(hits []vectorindex.Hit, allowed, queued map[string]bool) int {
	count := 0
	for _, hit := range hits {
		if allowed[hit.Key] && !queued[hit.Key] {
			count++
		}
	}
	return count
}

// readableCollection returns the collection holding model's vectors and the
// objects the outbox queues, or usable false when the index cannot answer
// for model: it has no collection for it, or too much is queued.
func (s *Service) readableCollection(ctx context.Context, model string) (string, map[string]bool, bool, error) {
	current, err := s.currentImagesCollection(ctx)
	if err != nil {
		return "", nil, false, err
	}
	if current.name == "" || current.model != model {
		s.reportVectorFallback("the vector index has no collection for the model", nil)
		return "", nil, false, nil
	}
	objects, err := s.strings(ctx, "SELECT object_id FROM vector_outbox LIMIT ?", maxOutboxDelta+1)
	if err != nil {
		return "", nil, false, err
	}
	if len(objects) > maxOutboxDelta {
		s.reportVectorFallback("too many photos wait for the vector index", nil)
		return "", nil, false, nil
	}
	queued := make(map[string]bool, len(objects))
	for _, object := range objects {
		queued[object] = true
	}
	return current.name, queued, true, nil
}

// scoreFromCatalog adds to scores the exact similarity to vector of each of
// objects that has a vector of model in the Catalog.
func (s *Service) scoreFromCatalog(ctx context.Context, model string, vector []float32, objects []string, scores map[string]float64) error {
	return s.catalogVectors(ctx, model, objects, func(object string, stored []float32) {
		scores[object] = dot(stored, vector)
	})
}

// catalogSimilarities scores every object of wanted that has a vector of
// model in the Catalog, as the index's answer would be scored: exactly.
func (s *Service) catalogSimilarities(ctx context.Context, model string, vector []float32, wanted map[string]bool) (map[string]float64, error) {
	scores := make(map[string]float64, len(wanted))
	if len(wanted) <= 4*vectorReadBatch {
		objects := make([]string, 0, len(wanted))
		for object := range wanted {
			objects = append(objects, object)
		}
		return scores, s.scoreFromCatalog(ctx, model, vector, objects, scores)
	}
	err := s.eachVector(ctx, model, func(object string, stored []float32) {
		if wanted[object] {
			scores[object] = dot(stored, vector)
		}
	})
	return scores, err
}

// catalogVectors calls visit with the Catalog's vector of model for each of
// objects that has one, a batch at a time.
func (s *Service) catalogVectors(ctx context.Context, model string, objects []string, visit func(object string, vector []float32)) error {
	for start := 0; start < len(objects); start += vectorReadBatch {
		batch := objects[start:min(len(objects), start+vectorReadBatch)]
		args := make([]any, 0, len(batch)+2)
		args = append(args, embeddingDerivation, model)
		for _, object := range batch {
			args = append(args, object)
		}
		found, err := s.readVectors(ctx, "SELECT object_id, vector FROM embeddings WHERE derivation = ? AND model = ? AND object_id IN ("+
			sqlitedb.Placeholders(len(batch))+")", args...)
		if err != nil {
			return err
		}
		for _, v := range found {
			visit(v.object, v.vector)
		}
	}
	return nil
}

// eachVector calls visit with every vector of model the Catalog holds, in
// order of content object, reading a batch at a time.
func (s *Service) eachVector(ctx context.Context, model string, visit func(object string, vector []float32)) error {
	after := ""
	for {
		found, err := s.readVectors(ctx, `SELECT object_id, vector FROM embeddings WHERE derivation = ? AND model = ? AND object_id > ?
ORDER BY object_id LIMIT ?`, embeddingDerivation, model, after, vectorReadBatch)
		if err != nil {
			return err
		}
		for _, v := range found {
			visit(v.object, v.vector)
		}
		if len(found) < vectorReadBatch {
			return nil
		}
		after = found[len(found)-1].object
	}
}

type storedVector struct {
	object string
	vector []float32
}

// readVectors runs a query of content objects and their vectors and releases
// the connection before returning them.
func (s *Service) readVectors(ctx context.Context, query string, args ...any) ([]storedVector, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []storedVector
	for rows.Next() {
		var v storedVector
		var encoded []byte
		if err := rows.Scan(&v.object, &encoded); err != nil {
			return nil, err
		}
		v.vector = decodeVector(encoded)
		found = append(found, v)
	}
	return found, errors.Join(rows.Err(), rows.Close())
}

// thresholdQuery asks for the objects whose similarity to vector reaches
// floor.
type thresholdQuery struct {
	vector []float32
	floor  float64
}

// above returns, for each query, the objects of objects whose similarity to
// its vector reaches its floor, with that similarity. Whether an object
// reaches the floor is decided exactly, and so is the similarity of the most
// similar ones; with exactAll every similarity is exact. Otherwise the
// others are the index's, within similarityMargin of exact.
func (s *Service) above(ctx context.Context, model string, scope []string, objects []string, queries []thresholdQuery,
	exactAll bool) ([]map[string]float64, error) {
	found := make([]map[string]float64, len(queries))
	if len(queries) == 0 {
		return found, nil
	}
	if s.vectors != nil {
		if sims, ok, err := s.indexAbove(ctx, model, scope, objects, queries, exactAll); err != nil && ctx.Err() != nil {
			return nil, err
		} else if ok {
			return sims, nil
		}
	}
	wanted := make(map[string]bool, len(objects))
	for _, object := range objects {
		wanted[object] = true
	}
	for i := range found {
		found[i] = make(map[string]float64)
	}
	visit := func(object string, vector []float32) {
		for i, query := range queries {
			if sim := dot(vector, query.vector); sim >= query.floor {
				found[i][object] = sim
			}
		}
	}
	if len(wanted) <= 4*vectorReadBatch {
		return found, s.catalogVectors(ctx, model, objects, visit)
	}
	return found, s.eachVector(ctx, model, func(object string, vector []float32) {
		if wanted[object] {
			visit(object, vector)
		}
	})
}

// indexAbove answers above with the index; ok is false when it cannot answer
// for the model.
func (s *Service) indexAbove(ctx context.Context, model string, scope []string, objects []string, queries []thresholdQuery,
	exactAll bool) ([]map[string]float64, bool, error) {
	collection, queued, usable, err := s.readableCollection(ctx, model)
	if err != nil || !usable {
		return nil, false, err
	}
	allowed := make(map[string]bool, len(objects))
	for _, object := range objects {
		allowed[object] = true
	}
	requests := make([]vectorindex.Query, len(queries))
	floors := make([]float32, len(queries))
	for i, query := range queries {
		floors[i] = float32(query.floor - similarityMargin)
		requests[i] = vectorindex.Query{Vector: query.vector, Scope: vectorindex.Scope{Field: librariesField, Values: scope},
			Limit: len(allowed) + 64, Exact: true, Floor: &floors[i]}
	}
	answers, err := s.vectors.SearchBatch(ctx, collection, requests)
	for err == nil {
		// A query whose answer filled its limit may have more above its floor.
		var again []int
		for i, hits := range answers {
			if len(hits) == requests[i].Limit {
				again = append(again, i)
			}
		}
		if len(again) == 0 {
			break
		}
		retry := make([]vectorindex.Query, len(again))
		for n, i := range again {
			requests[i].Limit *= 4
			retry[n] = requests[i]
		}
		var more [][]vectorindex.Hit
		if more, err = s.vectors.SearchBatch(ctx, collection, retry); err == nil {
			for n, i := range again {
				answers[i] = more[n]
			}
		}
	}
	if err != nil {
		s.reportVectorFallback("the vector index failed", err)
		return nil, false, err
	}

	found := make([]map[string]float64, len(queries))
	// Exact scores are needed for what the outbox queues, for what lies near
	// a floor, and for the most similar of each query.
	need := make(map[string]bool)
	for object := range queued {
		if allowed[object] {
			need[object] = true
		}
	}
	for i, hits := range answers {
		found[i] = make(map[string]float64, len(hits))
		var best float64
		for _, hit := range hits {
			if allowed[hit.Key] && !queued[hit.Key] {
				found[i][hit.Key] = float64(hit.Score)
				best = max(best, float64(hit.Score))
			}
		}
		for object, sim := range found[i] {
			if exactAll || sim < queries[i].floor+similarityMargin || sim >= best-2*similarityMargin {
				need[object] = true
			}
		}
	}
	vectors := make(map[string][]float32, len(need))
	objectsNeeded := make([]string, 0, len(need))
	for object := range need {
		objectsNeeded = append(objectsNeeded, object)
	}
	if err := s.catalogVectors(ctx, model, objectsNeeded, func(object string, vector []float32) { vectors[object] = vector }); err != nil {
		return nil, false, err
	}
	for i, query := range queries {
		for object, vector := range vectors {
			_, listed := found[i][object]
			if !listed && !queued[object] {
				continue
			}
			if sim := dot(vector, query.vector); sim >= query.floor {
				found[i][object] = sim
			} else {
				delete(found[i], object)
			}
		}
		// Queued objects the index answered for, and those without a vector
		// in the Catalog any more, are left out.
		for object := range found[i] {
			if _, scored := vectors[object]; !scored && (queued[object] || need[object]) {
				delete(found[i], object)
			}
		}
	}
	return found, true, nil
}

// vectorsWarm reports whether a connection to the index is open.
func (s *Service) vectorsWarm() bool {
	warm, ok := s.vectors.(interface{ Warm() bool })
	return ok && warm.Warm()
}

// prepareVectorsEvery is how often PrepareVectors reaches the index: within
// the client's idle timeout, so that the connection stays open while the
// library is in use.
const prepareVectorsEvery = 20 * time.Second

// PrepareVectors starts the vector index ahead of a search, without waiting:
// the photo service calls it for every request of a signed-in person, so the
// instance runs while someone uses the library. It does nothing more often
// than every prepareVectorsEvery, nor without local AI.
func (s *Service) PrepareVectors() {
	preparer, ok := s.vectors.(interface{ Prepare(context.Context) error })
	if !ok || s.embedder == nil {
		return
	}
	now := time.Now().UnixNano()
	last := s.vectorsPrepared.Load()
	if now-last < int64(prepareVectorsEvery) || !s.vectorsPrepared.CompareAndSwap(last, now) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.closed, time.Minute)
		defer cancel()
		_ = preparer.Prepare(ctx)
	}()
}

// vectorFallbacks remembers when each reason to read the Catalog instead of
// the index was last reported.
type vectorFallbacks struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// reportVectorFallback tells Options.OnVectorFallback why a ranking read the
// Catalog instead of the index, once per reason in vectorFallbackLogEvery.
func (s *Service) reportVectorFallback(reason string, err error) {
	if s.onVectorFallback == nil {
		return
	}
	s.fallbacks.mu.Lock()
	now := s.now()
	if last, ok := s.fallbacks.last[reason]; ok && now.Sub(last) < vectorFallbackLogEvery {
		s.fallbacks.mu.Unlock()
		return
	}
	if s.fallbacks.last == nil {
		s.fallbacks.last = make(map[string]time.Time)
	}
	s.fallbacks.last[reason] = now
	s.fallbacks.mu.Unlock()
	s.onVectorFallback(reason, err)
}
