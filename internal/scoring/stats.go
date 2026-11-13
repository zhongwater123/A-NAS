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
	// MedianViewMilliseconds is over Scores. Mean, StandardDeviation and
	// RepeatDifference are in points, 0 to 5.
	MedianViewMilliseconds  int
	Mean, StandardDeviation float64
	// Agreement is the Pearson correlation of the rater's scores with the
	// other raters' mean on the same photos; nil below minAgreementPhotos.
	Agreement *float64
	// AgreementPhotos is how many photos it is computed on.
	AgreementPhotos int
	// RepeatDifference is the mean absolute difference between a repeat
	// and the rater's current score of that photo; nil without repeats.
	RepeatDifference *float64
	// FastShare is the share of Scores given within a second.
	FastShare float64
	// LongestRun is the longest run of the same score in a row.
	LongestRun int
	LastAt     time.Time
}

const minAgreementPhotos = 20

type answer struct {
	id       int64
	item     int64
	rater    string
	kind     string
	score    int
	repeatOf int64
	viewMs   int
	created  string
}

// Stats summarizes the raters of a batch.
func (s *Store) Stats(ctx context.Context, batchID string) ([]RaterStats, error) {
	if _, err := s.Batch(ctx, batchID); err != nil {
		return nil, err
	}
	var answers []answer
	err := each(ctx, s.db, `SELECT id, item_id, rater, kind, coalesce(score, 0), coalesce(repeat_of, 0), view_ms, created_at
		FROM ratings WHERE batch_id = ? AND superseded_at IS NULL ORDER BY id`, []any{batchID}, func(rows *sql.Rows) error {
		var a answer
		if err := rows.Scan(&a.id, &a.item, &a.rater, &a.kind, &a.score, &a.repeatOf, &a.viewMs, &a.created); err != nil {
			return err
		}
		answers = append(answers, a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summarize(answers), nil
}

// summarize computes RaterStats from a batch's current answers in order. A
// repeat is a rating of the same photo, so it is compared with the rater's
// current score of that photo, which may have been changed since.
func summarize(answers []answer) []RaterStats {
	type key struct {
		item  int64
		rater string
	}
	current := map[key]int{}
	byItem := map[int64][]answer{}
	for _, a := range answers {
		if a.kind == "score" && a.repeatOf == 0 {
			current[key{a.item, a.rater}] = a.score
			byItem[a.item] = append(byItem[a.item], a)
		}
	}
	stats := map[string]*RaterStats{}
	views := map[string][]int{}
	scores := map[string][]float64{}
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
			scores[a.rater] = append(scores[a.rater], points(a.score))
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
			st.Mean, st.StandardDeviation = meanAndDeviation(scores[rater])
			st.LongestRun = longestRun(scores[rater])
		}
		// Agreement with the others' mean on the photos both scored.
		var mine, others []float64
		for _, a := range answers {
			if a.rater != rater || a.kind != "score" || a.repeatOf != 0 {
				continue
			}
			var sum float64
			var count int
			for _, other := range byItem[a.item] {
				if other.rater != rater {
					sum += points(other.score)
					count++
				}
			}
			if count > 0 {
				mine = append(mine, points(a.score))
				others = append(others, sum/float64(count))
			}
		}
		st.AgreementPhotos = len(mine)
		if len(mine) >= minAgreementPhotos {
			if r, ok := pearson(mine, others); ok {
				st.Agreement = &r
			}
		}
		// Repeats against the current score of the same photo.
		var difference float64
		var repeats int
		for _, a := range answers {
			if a.rater != rater || a.repeatOf == 0 || a.kind != "score" {
				continue
			}
			if score, ok := current[key{a.item, rater}]; ok {
				difference += math.Abs(points(a.score - score))
				repeats++
			}
		}
		if repeats > 0 {
			mean := difference / float64(repeats)
			st.RepeatDifference = &mean
		}
		result = append(result, *st)
	}
	return result
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
