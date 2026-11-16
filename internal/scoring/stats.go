package scoring

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// RaterStats describes one rater's work in a batch, for administrators.
type RaterStats struct {
	Rater string
	// Scores, Skips and Flags count current answers to photos handed out
	// the first time; Repeats the consistency checks.
	Scores, Skips, Flags, Repeats int
	// MedianViewMilliseconds is over Scores, each a whole photo with every
	// dimension of the batch.
	MedianViewMilliseconds int
	// FastShare is the share of Scores given within a second.
	FastShare float64
	LastAt    time.Time
	// Dimensions has one entry per dimension of the batch, in its order.
	Dimensions []DimensionStats
}

// DimensionStats describes a rater's scores on one dimension.
type DimensionStats struct {
	Dimension string
	// Mean, StandardDeviation and RepeatDifference are in points, 0 to 5.
	Mean, StandardDeviation float64
	// Agreement is the Pearson correlation of the rater's scores with the
	// other raters' mean on the same photos; nil below minAgreementPhotos.
	Agreement *float64
	// AgreementPhotos is how many photos it is computed on.
	AgreementPhotos int
	// RepeatDifference is the mean absolute difference between a repeat
	// and the rater's current score of that photo; nil without repeats.
	RepeatDifference *float64
	// LongestRun is the longest run of the same score in a row.
	LongestRun int
}

const minAgreementPhotos = 20

type answer struct {
	id       int64
	item     int64
	rater    string
	kind     string
	repeatOf int64
	viewMs   int
	created  string
	// scores holds every dimension a score answered, overall included.
	scores map[string]int
}

// Stats summarizes the raters of a batch.
func (s *Store) Stats(ctx context.Context, batchID string) ([]RaterStats, error) {
	batch, err := s.Batch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	var answers []answer
	index := map[int64]int{}
	err = each(ctx, s.db, `SELECT id, item_id, rater, kind, score, coalesce(repeat_of, 0), view_ms, created_at
		FROM ratings WHERE batch_id = ? AND superseded_at IS NULL ORDER BY id`, []any{batchID}, func(rows *sql.Rows) error {
		a := answer{scores: map[string]int{}}
		var overall sql.NullInt64
		if err := rows.Scan(&a.id, &a.item, &a.rater, &a.kind, &overall, &a.repeatOf, &a.viewMs, &a.created); err != nil {
			return err
		}
		if overall.Valid {
			a.scores[Overall] = int(overall.Int64)
		}
		index[a.id] = len(answers)
		answers = append(answers, a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(ctx, s.db, `SELECT s.rating_id, s.dimension, s.score FROM rating_scores s JOIN ratings r ON r.id = s.rating_id
		WHERE r.batch_id = ? AND r.superseded_at IS NULL`, []any{batchID}, func(rows *sql.Rows) error {
		var id int64
		var dimension string
		var value int
		if err := rows.Scan(&id, &dimension, &value); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			answers[i].scores[dimension] = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summarize(answers, batch.Dimensions), nil
}

// summarize computes RaterStats from a batch's current answers in order. A
// repeat is a rating of the same photo, so it is compared with the rater's
// current score of that photo, which may have been changed since.
func summarize(answers []answer, dimensions []string) []RaterStats {
	stats := map[string]*RaterStats{}
	views := map[string][]int{}
	var order []string
	for _, a := range answers {
		st := stats[a.rater]
		if st == nil {
			st = &RaterStats{Rater: a.rater}
			stats[a.rater] = st
			order = append(order, a.rater)
		}
		if at, err := time.Parse(sqlitedb.TimeLayout, a.created); err == nil && at.After(st.LastAt) {
			st.LastAt = at
		}
		switch {
		case a.repeatOf != 0:
			st.Repeats++
		case a.kind == "skip":
			st.Skips++
		case a.kind == "flag":
			st.Flags++
		default:
			st.Scores++
			views[a.rater] = append(views[a.rater], a.viewMs)
			if a.viewMs < 1000 {
				st.FastShare++
			}
		}
	}
	result := make([]RaterStats, 0, len(order))
	for _, rater := range order {
		st := stats[rater]
		if st.Scores > 0 {
			st.FastShare /= float64(st.Scores)
			st.MedianViewMilliseconds = median(views[rater])
		}
		for _, dimension := range dimensions {
			st.Dimensions = append(st.Dimensions, summarizeDimension(answers, rater, dimension))
		}
		result = append(result, *st)
	}
	return result
}

// summarizeDimension computes one rater's statistics on one dimension.
func summarizeDimension(answers []answer, rater, dimension string) DimensionStats {
	type key struct {
		item  int64
		rater string
	}
	current := map[key]int{}
	byItem := map[int64][]answer{}
	var mine []float64
	for _, a := range answers {
		score, ok := a.scores[dimension]
		if a.kind != "score" || a.repeatOf != 0 || !ok {
			continue
		}
		current[key{a.item, a.rater}] = score
		byItem[a.item] = append(byItem[a.item], a)
		if a.rater == rater {
			mine = append(mine, points(score))
		}
	}
	st := DimensionStats{Dimension: dimension}
	if len(mine) > 0 {
		st.Mean, st.StandardDeviation = meanAndDeviation(mine)
		st.LongestRun = longestRun(mine)
	}
	// Agreement with the others' mean on the photos both scored.
	var own, others []float64
	for _, a := range answers {
		score, ok := a.scores[dimension]
		if a.rater != rater || a.kind != "score" || a.repeatOf != 0 || !ok {
			continue
		}
		var sum float64
		var count int
		for _, other := range byItem[a.item] {
			if other.rater != rater {
				sum += points(other.scores[dimension])
				count++
			}
		}
		if count > 0 {
			own = append(own, points(score))
			others = append(others, sum/float64(count))
		}
	}
	st.AgreementPhotos = len(own)
	if len(own) >= minAgreementPhotos {
		if r, ok := pearson(own, others); ok {
			st.Agreement = &r
		}
	}
	// Repeats against the current score of the same photo.
	var difference float64
	var repeats int
	for _, a := range answers {
		score, ok := a.scores[dimension]
		if a.rater != rater || a.repeatOf == 0 || a.kind != "score" || !ok {
			continue
		}
		if was, ok := current[key{a.item, rater}]; ok {
			difference += math.Abs(points(score - was))
			repeats++
		}
	}
	if repeats > 0 {
		mean := difference / float64(repeats)
		st.RepeatDifference = &mean
	}
	return st
}

// points turns hundredths into points.
func points(hundredths int) float64 { return float64(hundredths) / 100 }

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func meanAndDeviation(values []float64) (float64, float64) {
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	var squares float64
	for _, v := range values {
		squares += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(squares / float64(len(values)))
}

func longestRun(values []float64) int {
	longest, run := 0, 0
	for i, v := range values {
		if i > 0 && v == values[i-1] {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	return longest
}

func pearson(x, y []float64) (float64, bool) {
	mx, sx := meanAndDeviation(x)
	my, sy := meanAndDeviation(y)
	if sx == 0 || sy == 0 {
		return 0, false
	}
	var covariance float64
	for i := range x {
		covariance += (x[i] - mx) * (y[i] - my)
	}
	return covariance / float64(len(x)) / (sx * sy), true
}
