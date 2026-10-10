package photos

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos/labels"
)

// AI labels (docs/architecture/photo-ai.md): a photo shows a label when the
// similarity of its vector to the label's text vector reaches the threshold
// calibrated for the model on public datasets. Scores are computed when read,
// so new thresholds apply at once and nothing needs recomputing. The AI
// runner fetches the label texts' vectors from the Worker and keeps them in
// the Catalog.

// AILabel is a label local AI sees in a photo.
type AILabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Score is the similarity, at least the label's threshold.
	Score float64 `json:"score"`
}

// maxShownLabels caps the labels one photo shows.
const maxShownLabels = 5

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

// labelsOf returns the labels a photo with this original shows, less the
// hidden ones, the clearest first: by how far each score passes its threshold.
func (s *Service) labelsOf(ctx context.Context, objectID string, hidden map[string]bool) ([]AILabel, error) {
	labelVectors, err := s.labelVectors(ctx)
	if err != nil || labelVectors == nil {
		return nil, err
	}
	var found []AILabel
	err = s.index.read(ctx, s, s.labels.Model, func(vectors map[string][]float32) {
		vector, ok := vectors[objectID]
		if !ok {
			return
		}
		for _, label := range s.labels.Shown() {
			if hidden[label.ID] {
				continue
			}
			if score := dot(vector, labelVectors[label.ID]); score >= s.labels.Thresholds[label.ID] {
				found = append(found, AILabel{ID: label.ID, Name: label.Name, Score: score})
			}
		}
	})
	slices.SortFunc(found, func(a, b AILabel) int {
		return cmp.Compare(b.Score-s.labels.Thresholds[b.ID], a.Score-s.labels.Thresholds[a.ID])
	})
	return found[:min(len(found), maxShownLabels)], err
}

// LabelCount is a label and how many photos within a search's scope show it.
type LabelCount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Category groups labels, such as animal, food or vehicle.
	Category string `json:"category"`
	Photos   int    `json:"photos"`
	// CoverID is the photo that shows the label most clearly.
	CoverID string `json:"coverId"`
}

// LabelCounts lists the labels that photos within a search's scope show, most
// photos first, by the rule of a search by label: a photo that hid a label
// does not count for it. ready is false while the label vectors are not in the
// Catalog; there are no counts then.
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
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(scope)), ",") + ")"
	rows, err := s.db.QueryContext(ctx, "SELECT id, object_id FROM assets WHERE trashed_at IS NULL AND library_id IN "+in+" ORDER BY id", scope...)
	if err != nil {
		return nil, false, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectID); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	hiding, err := s.strings(ctx, `SELECT c.asset_id || char(31) || c.label_id FROM ai_tag_corrections c
JOIN assets a ON a.id = c.asset_id WHERE a.library_id IN `+in, scope...)
	if err != nil {
		return nil, false, err
	}
	hidden := make(map[string]bool, len(hiding))
	for _, pair := range hiding {
		hidden[pair] = true
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
