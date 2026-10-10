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

var ErrInvalidQuery = errors.New("invalid photo search query")

const (
	// MaxQueryRunes bounds a search query.
	MaxQueryRunes = 200
	// maxSemanticResults keeps the long tail of weak matches out of results.
	maxSemanticResults = 500
	// nameMatch ranks photos whose name contains the query above every
	// similarity, which lies within [-1, 1].
	nameMatch = 2
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
	// name and user tag matches and the photos within closestGap of the best
	// match. The results after them are less related.
	Closest int
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
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.object_id, a.name, IFNULL(group_concat(t.name, char(31)), '')
FROM assets a LEFT JOIN user_tags t ON t.asset_id = a.id
WHERE a.trashed_at IS NULL AND a.library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
GROUP BY a.id`, args...)
	if err != nil {
		return SearchPage{}, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectID, &c.name, &c.tags); err != nil {
			_ = rows.Close()
			return SearchPage{}, err
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return SearchPage{}, err
	}

	page := SearchPage{Assets: []Asset{}}
	hits, err := s.queryHits(ctx, query, candidates, &page)
	if err != nil {
		return SearchPage{}, err
	}
	if after != nil {
		start, _ := slices.BinarySearchFunc(hits, *after, compareHits)
		if start < len(hits) && hits[start] == *after {
			start++
		}
		hits = hits[start:]
	}
	limit := pageSize(request.Limit)
	if len(hits) > limit {
		last := hits[limit-1]
		page.Next = encodeCursor(strconv.FormatFloat(last.score, 'g', -1, 64), last.id)
		hits = hits[:limit]
	}
	if len(hits) == 0 {
		return page, nil
	}
	ids := make([]any, len(hits))
	for i, hit := range hits {
		ids[i] = hit.id
	}
	// A photo trashed since the candidates were read drops out here.
	records, err := s.queryAssets(ctx, s.db,
		"WHERE a.trashed_at IS NULL AND a.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
	if err != nil {
		return SearchPage{}, err
	}
	position := make(map[string]int, len(hits))
	for i, hit := range hits {
		position[hit.id] = i
	}
	slices.SortFunc(records, func(a, b assetRecord) int { return position[a.ID] - position[b.ID] })
	if err := s.addDuplicateHints(ctx, s.db, p, records); err != nil {
		return SearchPage{}, err
	}
	for _, record := range records {
		page.Assets = append(page.Assets, record.Asset)
	}
	return page, nil
}

// queryHits ranks the candidates for a query: name or user tag matches first,
// then every photo with a vector by similarity, at most maxSemanticResults
// of them. It records on the page whether the query was encoded and where the
// closest group ends.
func (s *Service) queryHits(ctx context.Context, query string, candidates []candidate, page *SearchPage) ([]searchHit, error) {
	queryVector, model := s.embedQuery(ctx, query)
	folded := strings.ToLower(query)
	var named, similar []searchHit
	err := s.index.read(ctx, s, model, func(vectors map[string][]float32) {
		for _, c := range candidates {
			vector, embedded := vectors[c.objectID]
			score := 0.0
			if embedded && queryVector != nil {
				score = dot(vector, queryVector)
			}
			switch {
			case strings.Contains(strings.ToLower(c.name), folded) || tagMatches(c.tags, folded):
				named = append(named, searchHit{c.id, nameMatch + score})
			case embedded && queryVector != nil:
				similar = append(similar, searchHit{c.id, score})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	sortHits(named)
	sortHits(similar)
	similar = similar[:min(len(similar), maxSemanticResults)]
	closest := 0
	if len(similar) > 0 {
		floor := similar[0].score - closestGap
		for closest < len(similar) && similar[closest].score >= floor {
			closest++
		}
	}
	page.Semantic = queryVector != nil
	page.Closest = len(named) + closest
	return append(named, similar...), nil
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
