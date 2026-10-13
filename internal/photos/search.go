package photos

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos/objects"
)

// Search finds photos by meaning and by name in the caller's private library
// and the shared library (docs/architecture/photo-ai.md). In Administrative
// Viewing Mode it also covers the one member library being viewed; ordinary
// searches never mix that library in. The AI Worker encodes the query;
// the photo service ranks the visible photos' image vectors against it with
// an exact scan, so nothing outside the caller's libraries is ever scored.
// Ranking alone shows unrelated photos after the real matches, and no single
// similarity cut-off separates them for every query
// (docs/research/photo-ai-label-calibration.md#搜索结果的取舍). So nothing
// is cut off: the results come in two groups, the closest first and then
// the less related, and the caller is told where the first group ends.
//
// The object detector adds what whole-photo vectors miss
// (docs/architecture/photo-objects.md): a query's class words add their
// detector score to the similarity, weighted by how much of the query they
// make up, and a query about things the detector finds reliably puts the
// photos it found them in first, so that search can say when there are none.

var ErrInvalidQuery = errors.New("invalid photo search query")

const (
	// MaxQueryRunes bounds a search query.
	MaxQueryRunes = 200
	// maxSemanticResults keeps the long tail of weak matches out of results.
	maxSemanticResults = 500
	// Results come in bands that never overlap, so that the score alone
	// orders them and resumes a page: photos whose name or user tags contain
	// the query, then the photos in which the detector found what the query
	// names, then the rest. A score within a band lies in [-1, 1 + weight].
	nameBand     = 6
	detectedBand = 3
	// closestGap ends the closest group at this similarity below the best
	// match. On COCO about 90% of the group is right when the library holds
	// the thing; it cannot tell when the library does not, so the group is
	// presented as the closest, not as the matches.
	closestGap = 0.04
)

// SearchRequest asks for the photos that match a query.
type SearchRequest struct {
	Query string
	// Viewing names a member's private library the caller is viewing through
	// an unexpired grant, to search it as well.
	Viewing string
	// Library narrows the search to one library of that scope; empty
	// searches all of it.
	Library string
	Cursor  string
	Limit   int
}

// SearchPage is one page of results, best match first.
type SearchPage struct {
	Assets []Asset
	// Next continues the results; empty on the last page.
	Next string
	// Semantic is false when the AI Worker could not encode the query; the
	// results then match names and user tags only.
	Semantic bool
	// Closest counts the results, over all pages, in the closest group: the
	// name and user tag matches, then the photos the detector found the
	// thing in or, without those, the photos within closestGap of the best
	// match. The results after them are less related.
	Closest int
	// Objects is set when the query is about things the detector finds
	// reliably.
	Objects *ObjectsFound
}

// ObjectsFound is what the detector found for a query about things.
type ObjectsFound struct {
	// Words are the query's class words, such as 手机.
	Words []string
	// Detected counts the photos, over all pages, in which the detector
	// found them; they follow the name matches.
	Detected int
	// Undetected counts the photos in the search's scope still waiting for
	// the detector, so that "none found" is not said too early.
	Undetected int
}

type searchHit struct {
	id    string
	score float64
}

// A candidate's tags are its user tags joined by unit separators.
type candidate struct{ id, objectID, name, tags string }

// Search returns photos matching the query by name or meaning. Trashed photos
// are never included.
func (s *Service) Search(ctx context.Context, p Principal, request SearchRequest) (SearchPage, error) {
	query := strings.TrimSpace(request.Query)
	if query == "" || utf8.RuneCountInString(query) > MaxQueryRunes {
		return SearchPage{}, ErrInvalidQuery
	}
	after, err := decodeSearchCursor(request.Cursor)
	if err != nil {
		return SearchPage{}, err
	}
	args, err := s.searchScope(ctx, p, request.Viewing)
	if err != nil {
		return SearchPage{}, err
	}
	if request.Library != "" {
		// Only a library the search covers anyway; any other is not found.
		if !slices.Contains(args, any(request.Library)) {
			return SearchPage{}, ErrNotFound
		}
		args = []any{request.Library}
	}
	candidates, err := s.searchCandidates(ctx, args)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Assets: []Asset{}}
	hits, err := s.queryHits(ctx, query, candidates, &page)
	if err != nil {
		return SearchPage{}, err
	}
	page.Assets, page.Next, err = s.hitPage(ctx, p, hits, after, request.Limit)
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

// searchCandidates lists the untrashed photos of the libraries in scope with
// their user tags.
func (s *Service) searchCandidates(ctx context.Context, scope []any) ([]candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.object_id, a.name, IFNULL(group_concat(t.name, char(31)), '')
FROM assets a LEFT JOIN user_tags t ON t.asset_id = a.id
WHERE a.trashed_at IS NULL AND a.library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(scope)), ",")+`)
GROUP BY a.id`, scope...)
	if err != nil {
		return nil, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectID, &c.name, &c.tags); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, errors.Join(rows.Err(), rows.Close())
}

// hitPage returns the page of ranked hits after the cursor and the cursor of
// the next page, with duplicate hints. A photo trashed since the candidates
// were read drops out.
func (s *Service) hitPage(ctx context.Context, p Principal, hits []searchHit, after *searchHit, limit int) ([]Asset, string, error) {
	if after != nil {
		start, _ := slices.BinarySearchFunc(hits, *after, compareHits)
		if start < len(hits) && hits[start] == *after {
			start++
		}
		hits = hits[start:]
	}
	limit = pageSize(limit)
	next := ""
	if len(hits) > limit {
		last := hits[limit-1]
		next = encodeCursor(strconv.FormatFloat(last.score, 'g', -1, 64), last.id)
		hits = hits[:limit]
	}
	assets := []Asset{}
	if len(hits) == 0 {
		return assets, next, nil
	}
	ids := make([]any, len(hits))
	for i, hit := range hits {
		ids[i] = hit.id
	}
	records, err := s.queryAssets(ctx, s.db,
		"WHERE a.trashed_at IS NULL AND a.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
	if err != nil {
		return nil, "", err
	}
	position := make(map[string]int, len(hits))
	for i, hit := range hits {
		position[hit.id] = i
	}
	slices.SortFunc(records, func(a, b assetRecord) int { return position[a.ID] - position[b.ID] })
	if err := s.addDuplicateHints(ctx, s.db, p, records); err != nil {
		return nil, "", err
	}
	for _, record := range records {
		assets = append(assets, record.Asset)
	}
	return assets, next, nil
}

// queryHits ranks the candidates for a query: name or user tag matches first,
// then every photo with a vector by similarity, at most maxSemanticResults
// of them. It records on the page whether the query was encoded and where the
// closest group ends.
func (s *Service) queryHits(ctx context.Context, query string, candidates []candidate, page *SearchPage) ([]searchHit, error) {
	queryVector, model := s.embedQuery(ctx, query)
	folded := strings.ToLower(query)
	calibration := s.objectCalibration
	mentions := s.matcher.Mentions(query)
	enabled := calibration.Enabled(mentions)
	share := objects.Coverage(query, enabled)
	tierScores := calibration.TierScores(query, mentions)
	tiered := tierScores != nil
	var named, detected, similar []searchHit
	var waiting []candidate
	var vectorErr error
	err := s.detected.read(ctx, s, calibration.Model, func(found map[string][]classScore) {
		if len(found) == 0 {
			// Nothing detected by the calibrated model yet: vectors alone.
			enabled, share, tierScores, tiered = nil, 0, nil, false
		}
		vectorErr = s.index.read(ctx, s, model, func(vectors map[string][]float32) {
			for _, c := range candidates {
				vector, embedded := vectors[c.objectID]
				score := 0.0
				if embedded && queryVector != nil {
					score = dot(vector, queryVector)
				}
				scores, wasDetected := found[c.objectID]
				if !wasDetected {
					waiting = append(waiting, c)
				}
				detector := mentionScore(scores, enabled)
				score += calibration.Weight * share * detector
				switch {
				case strings.Contains(strings.ToLower(c.name), folded) || tagMatches(c.tags, folded):
					named = append(named, searchHit{c.id, nameBand + score})
				case tiered && showsAll(scores, mentions, tierScores):
					detected = append(detected, searchHit{c.id, detectedBand + score})
				case embedded && queryVector != nil:
					similar = append(similar, searchHit{c.id, score})
				}
			}
		})
	})
	if err = errors.Join(err, vectorErr); err != nil {
		return nil, err
	}
	sortHits(named)
	sortHits(detected)
	sortHits(similar)
	similar = similar[:min(len(similar), maxSemanticResults)]
	closest := len(detected)
	if closest == 0 && len(similar) > 0 {
		floor := similar[0].score - closestGap
		for closest < len(similar) && similar[closest].score >= floor {
			closest++
		}
	}
	page.Semantic = queryVector != nil
	page.Closest = len(named) + closest
	if tiered {
		undetected, err := s.awaitingDetection(ctx, waiting)
		if err != nil {
			return nil, err
		}
		words := make([]string, len(mentions))
		for i, mention := range mentions {
			words[i] = mention.Word
		}
		page.Objects = &ObjectsFound{Words: words, Detected: len(detected), Undetected: undetected}
	}
	return append(append(named, detected...), similar...), nil
}

// mentionScore is the detector's score for a photo with these best scores per
// class. Every mentioned word must be found, so it is the lowest, over the
// words, of each word's best class.
func mentionScore(scores []classScore, mentions []objects.Mention) float64 {
	if len(mentions) == 0 {
		return 0
	}
	lowest := 1.0
	for _, mention := range mentions {
		lowest = min(lowest, bestScore(scores, mention.Classes))
	}
	return lowest
}

// showsAll reports whether a photo with these best scores per class shows
// every mentioned thing: each word's best class reaches that word's score.
func showsAll(scores []classScore, mentions []objects.Mention, tierScores []float64) bool {
	for i, mention := range mentions {
		if bestScore(scores, mention.Classes) < tierScores[i] {
			return false
		}
	}
	return true
}

// bestScore is the highest score of any of the classes.
func bestScore(scores []classScore, classes []int) float64 {
	best := float32(0)
	for _, class := range classes {
		if i, ok := slices.BinarySearchFunc(scores, class, func(s classScore, c int) int { return s.class - c }); ok {
			best = max(best, scores[i].score)
		}
	}
	return float64(best)
}

// awaitingDetection counts the candidates without a detection whose job has
// not failed for good: the detector will still look at them.
func (s *Service) awaitingDetection(ctx context.Context, candidates []candidate) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	failed, err := s.strings(ctx, "SELECT object_id FROM jobs WHERE derivation = ? AND state = 'failed'", detectionDerivation)
	if err != nil {
		return 0, err
	}
	gaveUp := make(map[string]bool, len(failed))
	for _, id := range failed {
		gaveUp[id] = true
	}
	count := 0
	for _, c := range candidates {
		if !gaveUp[c.objectID] {
			count++
		}
	}
	return count, nil
}

// searchScope returns the IDs of the libraries a search covers.
func (s *Service) searchScope(ctx context.Context, p Principal, viewing string) ([]any, error) {
	if !p.valid() {
		return nil, ErrForbidden
	}
	own, err := s.ensurePrivateLibrary(ctx, p)
	if err != nil {
		return nil, err
	}
	scope := []any{own.id, s.sharedLibraryID}
	if viewing == "" {
		return scope, nil
	}
	lib, err := s.visibleLibrary(ctx, s.db, p, viewing)
	if err != nil {
		return nil, err
	}
	if viewingOnly(p, lib) {
		scope = append(scope, lib.id)
	}
	return scope, nil
}

// embedQuery asks the Worker for the query's vector and the model it belongs
// to; any failure leaves search to names alone.
func (s *Service) embedQuery(ctx context.Context, query string) ([]float32, string) {
	if s.embedder == nil {
		return nil, ""
	}
	info, err := s.embedder.Info(ctx)
	if err != nil {
		return nil, ""
	}
	s.noticeModel(info.Model)
	vector, err := s.embedder.EmbedQuery(ctx, query)
	if err != nil || !validVector(vector, info.Dimensions) {
		return nil, ""
	}
	return vector, info.Model
}

// Hits are ordered by score, then by ID, both descending; the order is total,
// so a cursor resumes exactly after the last hit of a page.
func compareHits(a, b searchHit) int {
	if c := cmp.Compare(b.score, a.score); c != 0 {
		return c
	}
	return strings.Compare(b.id, a.id)
}

func sortHits(hits []searchHit) { slices.SortFunc(hits, compareHits) }

func decodeSearchCursor(cursor string) (*searchHit, error) {
	if cursor == "" {
		return nil, nil
	}
	key, id, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	score, err := strconv.ParseFloat(key, 64)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	return &searchHit{id: id, score: score}, nil
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range min(len(a), len(b)) {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// vectorIndex keeps one model's image vectors in memory, keyed by content
// object, so that a search does not read every vector from the Catalog;
// 20,000 photos take about 60 MB. It loads on the first search and then
// follows new vectors and purges.
type vectorIndex struct {
	mu      sync.RWMutex
	model   string
	vectors map[string][]float32
}

// read calls use with the vectors of model, loading them first when needed.
// An empty model means no vectors.
func (x *vectorIndex) read(ctx context.Context, s *Service, model string, use func(map[string][]float32)) error {
	if model == "" {
		use(nil)
		return nil
	}
	x.mu.RLock()
	if x.vectors != nil && x.model == model {
		defer x.mu.RUnlock()
		use(x.vectors)
		return nil
	}
	x.mu.RUnlock()

	x.mu.Lock()
	defer x.mu.Unlock()
	if x.vectors == nil || x.model != model {
		vectors, err := s.loadVectors(ctx, model)
		if err != nil {
			return err
		}
		x.model, x.vectors = model, vectors
	}
	use(x.vectors)
	return nil
}

// put adds a vector stored for model; vectors of a model not loaded wait for
// the next load.
func (x *vectorIndex) put(objectID, model string, vector []float32) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.vectors != nil && x.model == model {
		x.vectors[objectID] = vector
	}
}

func (x *vectorIndex) forget(objectIDs []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, id := range objectIDs {
		delete(x.vectors, id)
	}
}

func (s *Service) loadVectors(ctx context.Context, model string) (map[string][]float32, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, vector FROM embeddings WHERE derivation = ? AND model = ?",
		embeddingDerivation, model)
	if err != nil {
		return nil, err
	}
	vectors := make(map[string][]float32)
	for rows.Next() {
		var objectID string
		var encoded []byte
		if err := rows.Scan(&objectID, &encoded); err != nil {
			_ = rows.Close()
			return nil, err
		}
		vectors[objectID] = decodeVector(encoded)
	}
	return vectors, errors.Join(rows.Err(), rows.Close())
}
