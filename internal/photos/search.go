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

	"github.com/zhongwater123/A-NAS/internal/photos/labels"
)

// Search finds photos by meaning and by name in the caller's private library
// and the shared library (docs/architecture/photo-ai.md). In Administrative
// Viewing Mode it also covers the one member library being viewed; ordinary
// searches never mix that library in. The AI Worker encodes the query;
// the photo service ranks the visible photos' image vectors against it with
// an exact scan, so nothing outside the caller's libraries is ever scored.
// Ranking alone shows unrelated photos after the real matches, and no single
// similarity cut-off separates them for every query
// (docs/research/photo-ai-label-calibration.md#搜索结果的取舍). So a query
// that names labels calibrated for the model keeps only the photos that show
// all of them; any other query keeps the photos close to its best match. A
// search by AI label returns the photos that show the label.

var ErrInvalidQuery = errors.New("invalid photo search query")

const (
	// MaxQueryRunes bounds a search query.
	MaxQueryRunes = 200
	// maxSemanticResults keeps the long tail of weak matches out of results.
	maxSemanticResults = 500
	// nameMatch ranks photos whose name contains the query above every
	// similarity, which lies within [-1, 1].
	nameMatch = 2
	// closestGap keeps the photos within this similarity of the best match
	// when no calibrated label applies. On COCO about 90% of what it keeps
	// is right when the library holds the thing; it cannot tell when the
	// library does not, so results of this kind are presented as the closest.
	closestGap = 0.04
	// maxClosestResults keeps such uncertain results to a few.
	maxClosestResults = 20
)

// What semantic results are (SearchPage.Match).
const (
	// MatchNames: local AI could not encode the query, so only names and user
	// tags matched.
	MatchNames = "names"
	// MatchLabels: the photos that show every calibrated label the query
	// names, as far as local AI can tell.
	MatchLabels = "labels"
	// MatchClosest: the query names nothing local AI recognises reliably;
	// these are the photos closest in meaning.
	MatchClosest = "closest"
)

// LabelRef names a label a search went by.
type LabelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SearchRequest asks for either a Query or a Label.
type SearchRequest struct {
	Query string
	// Label is the ID of a label that photos show (AILabel.ID).
	Label string
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
	// results then match names only. For a label it is false while label
	// vectors are not available, and there are no results.
	Semantic bool
	// Match says what the semantic results are, and Labels which labels
	// they show.
	Match  string
	Labels []LabelRef
}

type searchHit struct {
	id    string
	score float64
}

// A candidate's tags are its user tags joined by unit separators.
type candidate struct{ id, objectID, name, tags string }

// Search returns photos matching the query by name or meaning, or showing the
// label. Trashed photos are never included.
func (s *Service) Search(ctx context.Context, p Principal, request SearchRequest) (SearchPage, error) {
	query := strings.TrimSpace(request.Query)
	if request.Label != "" {
		if _, shown := s.labels.Thresholds[request.Label]; !shown || query != "" {
			return SearchPage{}, ErrInvalidQuery
		}
	} else if query == "" || utf8.RuneCountInString(query) > MaxQueryRunes {
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
	var hits []searchHit
	if request.Label != "" {
		hits, page.Semantic, err = s.labelHits(ctx, request.Label, candidates)
		page.Match = MatchNames
		if page.Semantic {
			page.Match, page.Labels = MatchLabels, []LabelRef{s.labelRef(request.Label)}
		}
	} else {
		hits, page.Match, page.Labels, err = s.queryHits(ctx, query, candidates)
		page.Semantic = page.Match != MatchNames
	}
	if err != nil {
		return SearchPage{}, err
	}
	sortHits(hits)
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

// queryHits scores candidates against a query: name or user tag matches
// first, then by meaning either the photos that show every calibrated label
// the query names or, when it names none, the photos near the best match.
func (s *Service) queryHits(ctx context.Context, query string, candidates []candidate) ([]searchHit, string, []LabelRef, error) {
	queryVector, model := s.embedQuery(ctx, query)
	folded := strings.ToLower(query)
	match := MatchNames
	var required []labels.Label
	var refs []LabelRef
	var labelVectors map[string][]float32
	hidden := map[string]bool{}
	if queryVector != nil {
		match = MatchClosest
		if model == s.labels.Model {
			for _, label := range s.labels.Mentioned(query) {
				if _, calibrated := s.labels.Thresholds[label.ID]; calibrated {
					required = append(required, label)
				}
			}
		}
		if len(required) > 0 {
			var err error
			if labelVectors, err = s.labelVectors(ctx); err != nil {
				return nil, "", nil, err
			}
			if labelVectors != nil {
				match = MatchLabels
				if hidden, err = s.photosHiding(ctx, required); err != nil {
					return nil, "", nil, err
				}
				for _, label := range required {
					refs = append(refs, LabelRef{ID: label.ID, Name: label.Name})
				}
			}
		}
	}
	shows := func(id string, vector []float32) bool {
		for _, label := range required {
			if hidden[id+"\x1f"+label.ID] || dot(vector, labelVectors[label.ID]) < s.labels.Thresholds[label.ID] {
				return false
			}
		}
		return true
	}
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
			case embedded && queryVector != nil && (match != MatchLabels || shows(c.id, vector)):
				similar = append(similar, searchHit{c.id, score})
			}
		}
	})
	sortHits(similar)
	if match == MatchClosest && len(similar) > 0 {
		floor := similar[0].score - closestGap
		kept := 0
		for kept < len(similar) && similar[kept].score >= floor {
			kept++
		}
		similar = similar[:min(kept, maxClosestResults)]
	}
	return append(named, similar[:min(len(similar), maxSemanticResults)]...), match, refs, err
}

// photosHiding returns the photos that hid any of the labels, as asset ID
// and label ID joined by a unit separator.
func (s *Service) photosHiding(ctx context.Context, required []labels.Label) (map[string]bool, error) {
	ids := make([]any, len(required))
	for i, label := range required {
		ids[i] = label.ID
	}
	pairs, err := s.strings(ctx, "SELECT asset_id || char(31) || label_id FROM ai_tag_corrections WHERE label_id IN ("+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
	hidden := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		hidden[pair] = true
	}
	return hidden, err
}

func (s *Service) labelRef(id string) LabelRef {
	for _, label := range s.labels.Labels {
		if label.ID == id {
			return LabelRef{ID: id, Name: label.Name}
		}
	}
	return LabelRef{ID: id, Name: id}
}

// labelHits returns the candidates that show the label and have not hidden
// it.
func (s *Service) labelHits(ctx context.Context, label string, candidates []candidate) ([]searchHit, bool, error) {
	labelVectors, err := s.labelVectors(ctx)
	if err != nil || labelVectors == nil {
		return nil, false, err
	}
	hiding, err := s.strings(ctx, "SELECT asset_id FROM ai_tag_corrections WHERE label_id = ?", label)
	if err != nil {
		return nil, false, err
	}
	hidden := make(map[string]bool, len(hiding))
	for _, id := range hiding {
		hidden[id] = true
	}
	threshold := s.labels.Thresholds[label]
	var hits []searchHit
	err = s.index.read(ctx, s, s.labels.Model, func(vectors map[string][]float32) {
		for _, c := range candidates {
			if vector, ok := vectors[c.objectID]; ok && !hidden[c.id] {
				if score := dot(vector, labelVectors[label]); score >= threshold {
					hits = append(hits, searchHit{c.id, score})
				}
			}
		}
	})
	return hits, true, err
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
