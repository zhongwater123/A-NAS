package photos

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// The montage center's edit page finds shots by text (ADR 0039 decision 8):
// the photo library encodes each text with its own query encoder and ranks
// the window vectors of a grant's videos by similarity to it. A member waits
// for the answer, so it is an interactive request, as a photo search is.
//
// Only the grant's videos are searched, as its owner sees them now, and only
// those whose window analysis is done; a hit from the index outside them is
// ignored. As image ranking does (vectorsearch.go), the index only picks the
// windows that can rank, and those are scored exactly from the Catalog, so
// the answer is the same as an exact scan of the Catalog, which answers
// instead whenever the index cannot: it fails or rests after failing, or
// the collection its alias names is not the one of the text's model.

const (
	// MaxWindowSearchTexts bounds the texts one search asks about,
	// MaxWindowMatches the windows it answers for each, and
	// MaxWindowSearchHits the windows it answers in all, texts times k: the
	// answer stays well inside the grant socket's message limit.
	MaxWindowSearchTexts = 32
	MaxWindowMatches     = 200
	MaxWindowSearchHits  = 1600
)

// ErrWindowModelMismatch reports a search whose texts cannot be compared
// with the windows: a video of the grant has windows of another model than
// the text encoder's, as after the model changed and before its windows are
// made again. Searching the rest would leave that video out unseen.
var ErrWindowModelMismatch = errors.New("the windows were made with another model than the search's")

// WindowMatch is a window a text is like: the granted asset of its video,
// its index in the analysis, and its similarity to the text.
type WindowMatch struct {
	AssetID string  `json:"assetId"`
	Window  int     `json:"idx"`
	Score   float64 `json:"score"`
}

// windowHit is a window ranked for one text. Hits are ordered by score,
// then by object and window, so that the order is total.
type windowHit struct {
	object string
	idx    int
	score  float64
}

func compareWindowHits(a, b windowHit) int {
	if c := cmp.Compare(b.score, a.score); c != 0 {
		return c
	}
	if c := strings.Compare(a.object, b.object); c != 0 {
		return c
	}
	return cmp.Compare(a.idx, b.idx)
}

// searchedVideo is a granted video whose windows a search ranks: the first
// granted asset of its object, as the owner sees it, and how many windows
// it has.
type searchedVideo struct {
	asset string
	count int
}

// SearchWindows returns, for each of texts, the k windows of the grant's
// videos most like it, most alike first.
func (s *Service) SearchWindows(ctx context.Context, owner Principal, grantID string, texts []string, k int) ([][]WindowMatch, error) {
	if len(texts) == 0 || len(texts) > MaxWindowSearchTexts || k < 1 || k > MaxWindowMatches || len(texts)*k > MaxWindowSearchHits {
		return nil, ErrInvalidQuery
	}
	queries := make([]string, len(texts))
	for i, text := range texts {
		queries[i] = strings.TrimSpace(text)
		if queries[i] == "" || utf8.RuneCountInString(queries[i]) > MaxQueryRunes {
			return nil, ErrInvalidQuery
		}
	}
	videos, identities, err := s.searchedVideos(ctx, owner, grantID)
	if err != nil {
		return nil, err
	}
	matches := make([][]WindowMatch, len(queries))
	if len(identities) == 0 {
		// No video of the grant has windows yet.
		for i := range matches {
			matches[i] = []WindowMatch{}
		}
		return matches, nil
	}
	info, err := s.queryEncoder(ctx)
	if err != nil {
		return nil, err
	}
	identity := windowsIdentity(info.Model, info.Dimensions)
	for other := range identities {
		if other != identity {
			return nil, ErrWindowModelMismatch
		}
	}
	vectors := make([][]float32, len(queries))
	encoded := map[string][]float32{}
	for i, query := range queries {
		if vectors[i] = encoded[query]; vectors[i] != nil {
			continue
		}
		vector, err := s.encodeQuery(ctx, info, query)
		if err != nil {
			return nil, err
		}
		vectors[i], encoded[query] = vector, vector
	}
	ranked, err := s.rankWindows(ctx, identity, videos, vectors, k)
	if err != nil {
		return nil, err
	}
	for i, hits := range ranked {
		matches[i] = make([]WindowMatch, len(hits))
		for n, hit := range hits {
			matches[i][n] = WindowMatch{AssetID: videos[hit.object].asset, Window: hit.idx, Score: hit.score}
		}
	}
	return matches, nil
}

// searchedVideos reads the grant's videos as its owner sees them now: those
// whose window analysis is done, which a search ranks, and every identity
// of the windows the available ones have, done or not.
func (s *Service) searchedVideos(ctx context.Context, owner Principal, grantID string) (map[string]searchedVideo, map[string]bool, error) {
	if err := s.usableGrant(ctx, owner, grantID); err != nil {
		return nil, nil, err
	}
	assetIDs, err := s.textColumn(ctx, "SELECT asset_id FROM montage_grant_assets WHERE grant_id = ? ORDER BY asset_id", grantID)
	if err != nil {
		return nil, nil, err
	}
	videos := map[string]searchedVideo{}
	identities := map[string]bool{}
	for start := 0; start < len(assetIDs); start += MaxGrantAssets {
		looked, progress, err := s.lookupGrant(ctx, owner, grantID, assetIDs[start:min(len(assetIDs), start+MaxGrantAssets)])
		if err != nil {
			return nil, nil, err
		}
		for _, asset := range looked {
			if asset.Status != GrantAvailable {
				continue
			}
			p := progress[asset.ObjectID]
			for _, identity := range p.windowIdentities {
				identities[identity] = true
			}
			if _, seen := videos[asset.ObjectID]; seen {
				// The asset IDs come in order: the first one stands for its object.
				continue
			}
			if p.windows == AnalysisDone {
				videos[asset.ObjectID] = searchedVideo{asset: asset.AssetID, count: p.windowCount}
			}
		}
	}
	return videos, identities, nil
}

// rankWindows returns, for each vector, the k windows of identity of videos
// most similar to it, scored exactly.
func (s *Service) rankWindows(ctx context.Context, identity string, videos map[string]searchedVideo, vectors [][]float32, k int) ([][]windowHit, error) {
	if s.vectors != nil {
		ranked, ok, err := s.indexWindows(ctx, identity, videos, vectors, k)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		if ok {
			return ranked, nil
		}
	}
	tops := make([]windowTop, len(vectors))
	for i := range tops {
		tops[i] = windowTop{keep: k}
	}
	err := s.scoreVideoWindows(ctx, identity, videos, slices.Sorted(maps.Keys(videos)), vectors, tops)
	if err != nil {
		return nil, err
	}
	ranked := make([][]windowHit, len(tops))
	for i := range tops {
		ranked[i] = tops[i].ranked()
	}
	return ranked, nil
}

// windowTop keeps the windowTop.keep best hits it is given, and some more
// until it next prunes.
type windowTop struct {
	keep int
	hits []windowHit
}

func (t *windowTop) add(hit windowHit) {
	t.hits = append(t.hits, hit)
	if len(t.hits) >= 4*t.keep+256 {
		t.prune()
	}
}

func (t *windowTop) prune() {
	slices.SortFunc(t.hits, compareWindowHits)
	t.hits = t.hits[:min(len(t.hits), t.keep)]
}

func (t *windowTop) ranked() []windowHit {
	t.prune()
	return t.hits
}

// scoreVideoWindows scores every window of identity of objects against each
// vector, exactly, into tops, reading a video at a time so that the
// Catalog's one connection is free between them.
func (s *Service) scoreVideoWindows(ctx context.Context, identity string, videos map[string]searchedVideo, objects []string,
	vectors [][]float32, tops []windowTop) error {
	for _, object := range objects {
		err := s.eachRow(ctx, "SELECT idx, vector FROM video_windows WHERE object_id = ? AND identity = ? AND idx < ? ORDER BY idx",
			[]any{object, identity, videos[object].count}, func(rows *sql.Rows) error {
				var idx int
				var encoded []byte
				if err := rows.Scan(&idx, &encoded); err != nil {
					return err
				}
				stored := decodeVector(encoded)
				for i, vector := range vectors {
					tops[i].add(windowHit{object: object, idx: idx, score: dot(stored, vector)})
				}
				return nil
			})
		if err != nil {
			return err
		}
	}
	return nil
}

// indexWindows answers rankWindows with the index; ok is false when the
// index cannot answer for identity, and the caller reads the Catalog.
func (s *Service) indexWindows(ctx context.Context, identity string, videos map[string]searchedVideo, vectors [][]float32,
	k int) (ranked [][]windowHit, ok bool, err error) {
	if s.indexResting() {
		s.reportVectorFallback("the vector index failed recently", nil)
		return nil, false, nil
	}
	// Sync keeps the collection it writes in line with the Catalog; the
	// alias names it once every video is written.
	target, err := s.currentWindowsCollection(ctx)
	if err != nil {
		return nil, false, err
	}
	if target.name == "" || target.identity != identity {
		s.reportVectorFallback("the vector index has no windows collection for the model", nil)
		return nil, false, nil
	}
	aliased, err := s.vectors.Alias(ctx, windowsAlias)
	if err != nil {
		s.indexFailed(ctx, err)
		return nil, false, err
	}
	if aliased != target.name {
		s.reportVectorFallback("the vector index is still filling the windows collection", nil)
		return nil, false, nil
	}
	waiting, err := s.textColumn(ctx, "SELECT object_id FROM window_outbox LIMIT ?", maxOutboxDelta+1)
	if err != nil {
		return nil, false, err
	}
	if len(waiting) > maxOutboxDelta {
		s.reportVectorFallback("too many videos wait for the vector index", nil)
		return nil, false, nil
	}

	// What the outbox queues is scored from the Catalog: the index may hold
	// an older version of it, or nothing.
	queued := map[string]bool{}
	for _, object := range waiting {
		if _, searched := videos[object]; searched {
			queued[object] = true
		}
	}
	exact := make([]windowTop, len(vectors))
	for i := range exact {
		exact[i] = windowTop{keep: k}
	}
	if err := s.scoreVideoWindows(ctx, identity, videos, slices.Sorted(maps.Keys(queued)), vectors, exact); err != nil {
		return nil, false, err
	}
	var indexed []string
	for object := range videos {
		if !queued[object] {
			indexed = append(indexed, object)
		}
	}
	slices.Sort(indexed)

	// usable is a hit on a window of a searched video, as the Catalog has
	// it: any other point grants nothing.
	usable := func(hit vectorindex.Hit) (windowHit, bool) {
		object, number, found := strings.Cut(hit.Key, "#")
		idx, err := strconv.Atoi(number)
		video, searched := videos[object]
		if !found || err != nil || !searched || queued[object] || idx < 0 || idx >= video.count {
			return windowHit{}, false
		}
		return windowHit{object: object, idx: idx, score: float64(hit.Score)}, true
	}
	ask := func(queries []vectorindex.Query) ([][]vectorindex.Hit, error) {
		found, err := s.vectors.SearchBatch(ctx, target.name, queries)
		if err != nil {
			s.indexFailed(ctx, err)
		}
		return found, err
	}
	query := func(i, limit int, floor *float32) vectorindex.Query {
		return vectorindex.Query{Vector: vectors[i], Scope: vectorindex.Scope{Field: objectField, Values: indexed},
			Limit: limit, Exact: true, Floor: floor}
	}

	// First the k best, and some more; deeper while too few of them count.
	limits := make([]int, len(vectors))
	requests := make([]vectorindex.Query, len(vectors))
	for i := range vectors {
		limits[i] = k + indexOverfetch
		requests[i] = query(i, limits[i], nil)
	}
	answers, err := ask(requests)
	if err != nil {
		return nil, false, err
	}
	placed := make([][]windowHit, len(vectors))
	for i := range vectors {
		for {
			placed[i] = placed[i][:0]
			for _, hit := range answers[i] {
				if h, ok := usable(hit); ok {
					placed[i] = append(placed[i], h)
				}
			}
			if len(answers[i]) < limits[i] || len(placed[i])+len(exact[i].hits) >= k {
				break
			}
			limits[i] *= 4
			more, err := ask([]vectorindex.Query{query(i, limits[i], nil)})
			if err != nil {
				return nil, false, err
			}
			answers[i] = more[0]
		}
	}

	// The index sums in float32: whatever lies within similarityMargin of
	// the k-th place may rank there exactly, so all of it is asked for and
	// scored from the Catalog.
	cuts := make([]float64, len(vectors))
	for i := range vectors {
		all := append(slices.Clone(placed[i]), exact[i].hits...)
		slices.SortFunc(all, compareWindowHits)
		if len(all) < k {
			cuts[i] = math.Inf(-1)
			continue
		}
		cuts[i] = all[k-1].score - similarityMargin
		hits := answers[i]
		if len(hits) < limits[i] || float64(hits[len(hits)-1].Score) < cuts[i] {
			continue
		}
		floor := float32(cuts[i])
		for limit := limits[i] * 4; ; limit *= 4 {
			more, err := ask([]vectorindex.Query{query(i, limit, &floor)})
			if err != nil {
				return nil, false, err
			}
			if hits = more[0]; len(hits) < limit {
				break
			}
		}
		placed[i] = placed[i][:0]
		for _, hit := range hits {
			if h, ok := usable(hit); ok {
				placed[i] = append(placed[i], h)
			}
		}
	}
	s.indexAnswered()

	// Exact scores for every window near enough to a cut.
	needed := map[string]map[int]bool{}
	for i := range vectors {
		for _, hit := range placed[i] {
			if hit.score >= cuts[i] {
				if needed[hit.object] == nil {
					needed[hit.object] = map[int]bool{}
				}
				needed[hit.object][hit.idx] = true
			}
		}
	}
	stored, err := s.windowVectors(ctx, identity, needed)
	if err != nil {
		return nil, false, err
	}
	ranked = make([][]windowHit, len(vectors))
	for i, vector := range vectors {
		top := exact[i]
		for _, hit := range placed[i] {
			if window, ok := stored[windowKey(hit.object, hit.idx)]; ok && hit.score >= cuts[i] {
				top.add(windowHit{object: hit.object, idx: hit.idx, score: dot(window, vector)})
			}
		}
		ranked[i] = top.ranked()
	}
	return ranked, true, nil
}

// windowVectors reads the Catalog's vectors of identity of the windows
// needed names, a video at a time, keyed as their points are.
func (s *Service) windowVectors(ctx context.Context, identity string, needed map[string]map[int]bool) (map[string][]float32, error) {
	stored := map[string][]float32{}
	for _, object := range slices.Sorted(maps.Keys(needed)) {
		indexes := slices.Sorted(maps.Keys(needed[object]))
		for start := 0; start < len(indexes); start += vectorReadBatch {
			batch := indexes[start:min(len(indexes), start+vectorReadBatch)]
			args := []any{object, identity}
			for _, idx := range batch {
				args = append(args, idx)
			}
			err := s.eachRow(ctx, "SELECT idx, vector FROM video_windows WHERE object_id = ? AND identity = ? AND idx IN ("+
				sqlitedb.Placeholders(len(batch))+")", args, func(rows *sql.Rows) error {
				var idx int
				var encoded []byte
				if err := rows.Scan(&idx, &encoded); err != nil {
					return err
				}
				stored[windowKey(object, idx)] = decodeVector(encoded)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return stored, nil
}
