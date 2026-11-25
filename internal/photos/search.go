package photos

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos/objects"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Search finds photos by meaning and by name in the caller's private library
// and the shared library (docs/architecture/photo-ai.md). In Administrative
// Viewing Mode it also covers the one member library being viewed; ordinary
// searches never mix that library in. The AI Worker encodes the query; the
// vector index, asked within the caller's libraries, picks the photos that
// can rank, and the photo service scores those exactly against the Catalog's
// vectors (vectorsearch.go), so nothing outside the caller's libraries is
// ever ranked and the results are those of an exact scan.
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
//
// People are named by members, so their names are user data and take part in
// search (docs/architecture/photo-people.md#搜索): a query that contains the
// full name of a person in its scope keeps to that person's photos, and the
// rest of the query ranks them as usual; a name alone lists them newest first.

var ErrInvalidQuery = errors.New("invalid photo search query")

const (
	// MaxQueryRunes bounds a search query.
	MaxQueryRunes = 200
	// maxSemanticResults keeps the long tail of weak matches out of results.
	maxSemanticResults = 500
	// Results come in bands that never overlap, so that the score alone
	// orders them and resumes a page: photos whose name or user tags contain
	// the query, then the photos whose text does
	// (docs/architecture/photo-ocr.md#搜索), then the photos in which the
	// detector found what the query names, then the rest. A score within a
	// band lies in [-1, 1 + weight].
	nameBand     = 9
	textBand     = 6
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
	// name and user tag matches, the photos whose text holds the query, then
	// the photos the detector found the thing in or, without those, the
	// photos within closestGap of the best match. The results after them are
	// less related.
	Closest int
	// Names counts the results, over all pages, whose name or user tags hold
	// the query; they come first.
	Names int
	// Texts counts the results, over all pages, whose text holds the query
	// or whose document type the query names; they follow the name and user
	// tag matches.
	Texts int
	// Objects is set when the query is about things the detector finds
	// reliably.
	Objects *ObjectsFound
	// People are the names in the query whose photos the results keep to.
	People []string
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

// A candidate's tags are its user tags joined by unit separators; when is its
// capture time, or its import time without one. A video takes no part in
// AI and is matched by name and user tags only.
type candidate struct {
	id, objectID, libraryID, name, tags, when string
	video                                     bool
}

// inLocalAI is the rule of inLocalAI(objectID) on a candidate already read.
func (c candidate) inLocalAI() bool { return !c.video }

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
	candidates, err := s.searchCandidates(ctx, p, args)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Assets: []Asset{}}
	rest, names, of, err := s.peopleInQuery(ctx, query, args)
	if err != nil {
		return SearchPage{}, err
	}
	var hits []searchHit
	if len(names) > 0 {
		page.People = names
		candidates = slices.DeleteFunc(candidates, func(c candidate) bool {
			for _, photos := range of {
				if !photos[c.libraryID+"\x00"+c.objectID] {
					return true
				}
			}
			return false
		})
	}
	if len(names) > 0 && rest == "" {
		hits = newestHits(candidates)
		page.Semantic = true
		page.Closest = len(hits)
	} else {
		text := cmp.Or(rest, query)
		texts, err := s.textMatches(ctx, p, text, args)
		if err != nil {
			return SearchPage{}, err
		}
		queryVector, model := s.embedQuery(ctx, text)
		// A search kept to people's photos ranks a small part of the scope.
		var read vectorRead
		if hits, read, err = s.queryHits(ctx, text, queryVector, model, scopeIDs(args), candidates, texts, len(names) > 0,
			&page); err != nil {
			return SearchPage{}, err
		}
		// Only a first page without people compares like with like.
		if read.index && after == nil && len(names) == 0 {
			s.checkSearch(queryVector, model, scopeIDs(args), aiObjects(candidates, ""))
		}
	}
	page.Assets, page.Next, err = s.hitPage(ctx, p, args, hits, after, request.Limit)
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

// searchCandidates lists the photos listed to p in the libraries of scope,
// with their user tags.
func (s *Service) searchCandidates(ctx context.Context, p Principal, scope []any) ([]candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.object_id, a.library_id, a.name, IFNULL(group_concat(t.name, char(31)), ''),
  IFNULL(a.taken_at, a.imported_at), NOT `+notVideo("a.object_id")+`
FROM assets a LEFT JOIN user_tags t ON t.asset_id = a.id
WHERE `+listedAssets(p, "a")+` AND a.library_id IN (`+sqlitedb.Placeholders(len(scope))+`)
GROUP BY a.id`, scope...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectID, &c.libraryID, &c.name, &c.tags, &c.when, &c.video); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, errors.Join(rows.Err(), rows.Close())
}

// hitPage returns the page of ranked hits after the cursor and the cursor of
// the next page, with duplicate hints. Index hits grant nothing: a photo
// outside the libraries of scope, trashed since the candidates were read, or
// hidden from p by the asset rules drops out.
func (s *Service) hitPage(ctx context.Context, p Principal, scope []any, hits []searchHit, after *searchHit, limit int) ([]Asset, string, error) {
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
		"WHERE "+listedAssets(p, "a")+" AND a.library_id IN ("+sqlitedb.Placeholders(len(scope))+") AND a.id IN ("+sqlitedb.Placeholders(len(ids))+")",
		append(slices.Clone(scope), ids...)...)
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

// queryHits ranks the candidates for a query, whose vector of model is
// queryVector or nil: name or user tag matches first, then the photos texts
// finds the query in, then the photos the detector found the thing in, then
// every photo with a vector by similarity, at most maxSemanticResults of
// them. It records on the page whether the query was encoded and where the
// closest group ends. restricted says that the candidates are a small part of
// the libraries of scope.
func (s *Service) queryHits(ctx context.Context, query string, queryVector []float32, model string, scope []string,
	candidates []candidate, texts textHits, restricted bool, page *SearchPage) ([]searchHit, vectorRead, error) {
	folded := strings.ToLower(query)
	calibration := s.objectCalibration
	mentions := s.matcher.Mentions(query)
	enabled := calibration.Enabled(mentions)
	share := objects.Coverage(query, enabled)
	tierScores := calibration.TierScores(query, mentions)
	tiered := tierScores != nil

	// What the detector found decides the bands and adds to the similarity;
	// it is read first, so that the index is asked with nothing held.
	type judged struct {
		c                     candidate
		named, texted, inTier bool
	}
	judgedAll := make([]judged, 0, len(candidates))
	bonus := make(map[string]float64)
	// The photos of the bands above similarity are scored whatever their
	// rank.
	var always []string
	var waiting []candidate
	err := s.detected.read(ctx, s, calibration.Model, func(found map[string][]classScore) {
		if len(found) == 0 {
			// Nothing detected by the calibrated model yet: vectors alone.
			enabled, share, tierScores, tiered = nil, 0, nil, false
		}
		for _, c := range candidates {
			j := judged{c: c, named: strings.Contains(strings.ToLower(c.name), folded) || tagMatches(c.tags, folded)}
			if c.inLocalAI() {
				scores, wasDetected := found[c.objectID]
				if !wasDetected {
					waiting = append(waiting, c)
				}
				bonus[c.objectID] = calibration.Weight * share * mentionScore(scores, enabled)
				j.texted = !j.named && texts.holds(c)
				j.inTier = !j.named && !j.texted && tiered && showsAll(scores, mentions, tierScores)
				if j.named || j.texted || j.inTier {
					always = append(always, c.objectID)
				}
			}
			judgedAll = append(judgedAll, j)
		}
	})
	if err != nil {
		return nil, vectorRead{}, err
	}
	sims, read, err := s.similarities(ctx, similarityQuery{model: model, vector: queryVector, scope: scope,
		objects: aiObjects(candidates, ""), always: always, keep: maxSemanticResults,
		bonus: func(object string) float64 { return bonus[object] }, maxBonus: calibration.Weight * share, direct: restricted})
	if err != nil {
		return nil, vectorRead{}, err
	}

	var named, texted, detected, similar []searchHit
	for _, j := range judgedAll {
		c := j.c
		if !c.inLocalAI() {
			if j.named {
				named = append(named, searchHit{c.id, nameBand})
			}
			continue
		}
		sim, embedded := sims[c.objectID]
		score := bonus[c.objectID]
		if embedded {
			score += sim
		}
		switch {
		case j.named:
			named = append(named, searchHit{c.id, nameBand + score})
		case j.texted:
			texted = append(texted, searchHit{c.id, textBand + score})
		case j.inTier:
			detected = append(detected, searchHit{c.id, detectedBand + score})
		case embedded:
			similar = append(similar, searchHit{c.id, score})
		}
	}
	sortHits(named)
	sortHits(texted)
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
	page.Names, page.Texts = len(named), len(texted)
	page.Closest = len(named) + len(texted) + closest
	if tiered {
		undetected, err := s.awaitingDetection(ctx, waiting)
		if err != nil {
			return nil, vectorRead{}, err
		}
		words := make([]string, len(mentions))
		for i, mention := range mentions {
			words[i] = mention.Word
		}
		page.Objects = &ObjectsFound{Words: words, Detected: len(detected), Undetected: undetected}
	}
	return slices.Concat(named, texted, detected, similar), read, nil
}

// aiObjects lists, once each, the content objects of the candidates that
// take part in local AI, but exclude.
func aiObjects(candidates []candidate, exclude string) []string {
	seen := make(map[string]bool, len(candidates))
	objects := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.inLocalAI() && c.objectID != exclude && !seen[c.objectID] {
			seen[c.objectID] = true
			objects = append(objects, c.objectID)
		}
	}
	return objects
}

// scopeIDs is a scope's library IDs as strings.
func scopeIDs(scope []any) []string {
	ids := make([]string, 0, len(scope))
	for _, id := range scope {
		if s, ok := id.(string); ok {
			ids = append(ids, s)
		}
	}
	return ids
}

// peopleInQuery finds the full names of named people in the search's scope
// that the query contains, longest first so that a longer name is not taken
// for a shorter one inside it. It returns the query without them, the names,
// and for each name the photos of every person by that name, keyed by
// library and content object.
func (s *Service) peopleInQuery(ctx context.Context, query string, scope []any) (string, []string, []map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT pe.name, po.library_id || char(0) || o.object_id FROM people pe
JOIN person_occurrences po ON po.person_id = pe.id AND po.library_id = pe.library_id
JOIN occurrences o ON o.id = po.occurrence_id
WHERE pe.name <> '' AND pe.library_id IN (`+sqlitedb.Placeholders(len(scope))+`)
  AND pe.library_id IN (SELECT id FROM libraries WHERE people = 1)`, scope...)
	if err != nil {
		return "", nil, nil, err
	}
	defer rows.Close()
	photosOf := make(map[string]map[string]bool)
	for rows.Next() {
		var name, photo string
		if err := rows.Scan(&name, &photo); err != nil {
			return "", nil, nil, err
		}
		if photosOf[name] == nil {
			photosOf[name] = make(map[string]bool)
		}
		photosOf[name][photo] = true
	}
	if err := rows.Err(); err != nil {
		return "", nil, nil, err
	}
	known := make([]string, 0, len(photosOf))
	for name := range photosOf {
		known = append(known, name)
	}
	slices.SortFunc(known, func(a, b string) int {
		return cmp.Or(cmp.Compare(utf8.RuneCountInString(b), utf8.RuneCountInString(a)), strings.Compare(a, b))
	})
	rest := query
	var names []string
	var of []map[string]bool
	for _, name := range known {
		if strings.Contains(rest, name) {
			rest = strings.ReplaceAll(rest, name, " ")
			names = append(names, name)
			of = append(of, photosOf[name])
		}
	}
	return strings.Join(strings.Fields(rest), " "), names, of, nil
}

// newestHits ranks candidates by when they were taken, newest first, in the
// name band.
func newestHits(candidates []candidate) []searchHit {
	ordered := slices.Clone(candidates)
	slices.SortFunc(ordered, func(a, b candidate) int {
		return cmp.Or(strings.Compare(b.when, a.when), strings.Compare(b.id, a.id))
	})
	hits := make([]searchHit, len(ordered))
	for i, c := range ordered {
		hits[i] = searchHit{c.id, nameBand + 1 - float64(i)/float64(len(ordered)+1)}
	}
	return hits
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
	own, hasOwn, err := s.ownLibrary(ctx, p)
	if err != nil {
		return nil, err
	}
	scope := []any{s.sharedLibraryID}
	if hasOwn {
		scope = []any{own.id, s.sharedLibraryID}
	}
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
	s.ai.noticeModel(info.Model)
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
