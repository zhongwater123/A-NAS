package photos

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos/labels"
)

// AI clusters (docs/architecture/photo-ai.md): a photo belongs to a label's
// cluster when the similarity of its vector to the label's text vector
// reaches the threshold calibrated for the model on public datasets, and the
// photo has not been taken out of it. Clusters are only shown on their own
// (AI 聚合); search never uses them, and photo details do not list them.
// Scores are computed when read, so new thresholds apply at once and nothing
// needs recomputing. The AI runner fetches the label texts' vectors from the
// Worker and keeps them in the Catalog.

// labelIndex holds the shown labels' text vectors for the calibrated model
// once every one of them is in the Catalog.
type labelIndex struct {
	mu      sync.Mutex
	vectors map[string][]float32
	// retryAt delays fetching label texts after the Worker failed one.
	retryAt time.Time
}

// embedLabelText fetches the vector of one label text that the calibrated
// model still lacks and reports whether it did. With another model, or once
// all texts are present, it does nothing; a text the Worker fails on is tried
// again a minute later while photos go on.
func (s *Service) embedLabelText(ctx context.Context, embedder Embedder, info EmbedderInfo) (bool, error) {
	if info.Model != s.labels.Model {
		return false, nil
	}
	s.labelIndex.mu.Lock()
	waiting := s.labelIndex.vectors != nil || s.now().Before(s.labelIndex.retryAt)
	s.labelIndex.mu.Unlock()
	if waiting {
		return false, nil
	}
	have, err := s.queryVectors(ctx, info.Model)
	if err != nil {
		return false, err
	}
	for _, text := range labelTexts(s.labels) {
		if _, ok := have[text]; ok {
			continue
		}
		vector, err := embedder.EmbedQuery(ctx, text)
		if err != nil || !validVector(vector, info.Dimensions) {
			if errors.Is(err, ErrAIUnavailable) {
				s.ai.set(AIUnavailable, "", "")
			}
			s.labelIndex.mu.Lock()
			s.labelIndex.retryAt = s.now().Add(aiRetryDelay)
			s.labelIndex.mu.Unlock()
			return false, nil
		}
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO query_vectors(model, text, vector, created_at) VALUES(?, ?, ?, ?) ON CONFLICT(model, text) DO NOTHING",
			info.Model, text, encodeVector(vector), formatTime(s.now()))
		return err == nil, err
	}
	// All texts are present: build the labels now, so later rounds return early.
	_, err = s.labelVectors(ctx)
	return false, err
}

// labelVectors returns the shown labels' vectors, or nil while any text still
// lacks one. It needs no Worker: the vectors come from the Catalog.
func (s *Service) labelVectors(ctx context.Context) (map[string][]float32, error) {
	s.labelIndex.mu.Lock()
	defer s.labelIndex.mu.Unlock()
	if s.labelIndex.vectors != nil {
		return s.labelIndex.vectors, nil
	}
	have, err := s.queryVectors(ctx, s.labels.Model)
	if err != nil {
		return nil, err
	}
	vectors := make(map[string][]float32)
	for _, label := range s.labels.Shown() {
		var sum []float64
		for _, text := range s.labels.Texts(label) {
			vector, ok := have[text]
			if !ok {
				return nil, nil
			}
			if sum == nil {
				sum = make([]float64, len(vector))
			}
			for i, value := range vector {
				sum[i] += float64(value)
			}
		}
		vectors[label.ID] = normalized(sum)
	}
	s.labelIndex.vectors = vectors
	return vectors, nil
}

// LabelCount is a label and how many photos within a search's scope are in
// its cluster.
type LabelCount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Category groups labels, such as animal, food or vehicle.
	Category string `json:"category"`
	Photos   int    `json:"photos"`
	// CoverID is the photo that matches the label most clearly.
	CoverID string `json:"coverId"`
}

// LabelCounts lists the clusters of the photos within a search's scope, most
// photos first; a photo taken out of a cluster does not count for it. ready is
// false while the label vectors are not in the Catalog; there are no counts
// then.
func (s *Service) LabelCounts(ctx context.Context, p Principal, viewing string) (counts []LabelCount, ready bool, err error) {
	scope, err := s.searchScope(ctx, p, viewing)
	if err != nil {
		return nil, false, err
	}
	counts = []LabelCount{}
	labelVectors, err := s.labelVectors(ctx)
	if err != nil || labelVectors == nil {
		return counts, false, err
	}
	candidates, hidden, err := s.clusterCandidates(ctx, scope, "")
	if err != nil {
		return nil, false, err
	}

	type tally struct {
		photos int
		cover  string
		margin float64
	}
	tallies := make(map[string]*tally)
	err = s.index.read(ctx, s, s.labels.Model, func(vectors map[string][]float32) {
		for _, c := range candidates {
			vector, ok := vectors[c.objectID]
			if !ok {
				continue
			}
			for _, label := range s.labels.Shown() {
				margin := dot(vector, labelVectors[label.ID]) - s.labels.Thresholds[label.ID]
				if margin < 0 || hidden[c.id+"\x1f"+label.ID] {
					continue
				}
				t := tallies[label.ID]
				if t == nil {
					t = &tally{}
					tallies[label.ID] = t
				}
				t.photos++
				if t.cover == "" || margin > t.margin {
					t.cover, t.margin = c.id, margin
				}
			}
		}
	})
	if err != nil {
		return nil, false, err
	}
	for _, label := range s.labels.Shown() {
		if t := tallies[label.ID]; t != nil {
			counts = append(counts, LabelCount{ID: label.ID, Name: label.Name, Category: label.Category, Photos: t.photos, CoverID: t.cover})
		}
	}
	slices.SortStableFunc(counts, func(a, b LabelCount) int { return cmp.Compare(b.Photos, a.Photos) })
	return counts, true, nil
}

// LabelPhotos lists by page the cluster of a label within a search's scope,
// the clearest match first. A label without a threshold, or unknown, is not
// found; while the label vectors are not in the Catalog the cluster is empty.
func (s *Service) LabelPhotos(ctx context.Context, p Principal, label, viewing, cursor string, limit int) (Page, error) {
	if _, shown := s.labels.Thresholds[label]; !shown {
		return Page{}, ErrNotFound
	}
	after, err := decodeSearchCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	scope, err := s.searchScope(ctx, p, viewing)
	if err != nil {
		return Page{}, err
	}
	page := Page{Assets: []Asset{}}
	labelVectors, err := s.labelVectors(ctx)
	if err != nil || labelVectors == nil {
		return page, err
	}
	candidates, hidden, err := s.clusterCandidates(ctx, scope, label)
	if err != nil {
		return Page{}, err
	}
	threshold := s.labels.Thresholds[label]
	var hits []searchHit
	err = s.index.read(ctx, s, s.labels.Model, func(vectors map[string][]float32) {
		for _, c := range candidates {
			if vector, ok := vectors[c.objectID]; ok && !hidden[c.id+"\x1f"+label] {
				if score := dot(vector, labelVectors[label]); score >= threshold {
					hits = append(hits, searchHit{c.id, score})
				}
			}
		}
	})
	if err != nil {
		return Page{}, err
	}
	sortHits(hits)
	if after != nil {
		start, _ := slices.BinarySearchFunc(hits, *after, compareHits)
		if start < len(hits) && hits[start] == *after {
			start++
		}
		hits = hits[start:]
	}
	if limit = pageSize(limit); len(hits) > limit {
		last := hits[limit-1]
		page.Next = encodeCursor(strconv.FormatFloat(last.score, 'g', -1, 64), last.id)
		hits = hits[:limit]
	}
	if len(hits) == 0 {
		return page, nil
	}
	ids := make([]any, len(hits))
	position := make(map[string]int, len(hits))
	for i, hit := range hits {
		ids[i], position[hit.id] = hit.id, i
	}
	// A photo trashed since the candidates were read drops out here.
	records, err := s.queryAssets(ctx, s.db,
		"WHERE a.trashed_at IS NULL AND a.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
	if err != nil {
		return Page{}, err
	}
	slices.SortFunc(records, func(a, b assetRecord) int { return position[a.ID] - position[b.ID] })
	if err := s.addDuplicateHints(ctx, s.db, p, records); err != nil {
		return Page{}, err
	}
	for _, record := range records {
		page.Assets = append(page.Assets, record.Asset)
	}
	return page, nil
}

// clusterCandidates returns the untrashed photos of the libraries in scope
// and the ones taken out of a cluster, as asset ID and label ID joined by a
// unit separator; only those of label, when one is given.
func (s *Service) clusterCandidates(ctx context.Context, scope []any, label string) ([]candidate, map[string]bool, error) {
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(scope)), ",") + ")"
	rows, err := s.db.QueryContext(ctx, "SELECT id, object_id FROM assets WHERE trashed_at IS NULL AND library_id IN "+in+" ORDER BY id", scope...)
	if err != nil {
		return nil, nil, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectID); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	query := `SELECT c.asset_id || char(31) || c.label_id FROM ai_tag_corrections c
JOIN assets a ON a.id = c.asset_id WHERE a.library_id IN ` + in
	args := scope
	if label != "" {
		query += " AND c.label_id = ?"
		args = append(slices.Clone(scope), label)
	}
	hiding, err := s.strings(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	hidden := make(map[string]bool, len(hiding))
	for _, pair := range hiding {
		hidden[pair] = true
	}
	return candidates, hidden, nil
}

func (s *Service) queryVectors(ctx context.Context, model string) (map[string][]float32, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT text, vector FROM query_vectors WHERE model = ?", model)
	if err != nil {
		return nil, err
	}
	vectors := make(map[string][]float32)
	for rows.Next() {
		var text string
		var encoded []byte
		if err := rows.Scan(&text, &encoded); err != nil {
			_ = rows.Close()
			return nil, err
		}
		vectors[text] = decodeVector(encoded)
	}
	return vectors, errors.Join(rows.Err(), rows.Close())
}

func labelTexts(set labels.Set) []string {
	var texts []string
	for _, label := range set.Shown() {
		texts = append(texts, set.Texts(label)...)
	}
	return texts
}

func normalized(sum []float64) []float32 {
	var norm float64
	for _, value := range sum {
		norm += value * value
	}
	norm = math.Sqrt(norm)
	vector := make([]float32, len(sum))
	for i, value := range sum {
		if norm > 0 {
			vector[i] = float32(value / norm)
		}
	}
	return vector
}
