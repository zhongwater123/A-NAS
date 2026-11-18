package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
)

// After the windows, the montage analysis cuts a video's cards and measures
// each card's quality (montage-cards/v1, montage-quality/v1) with the
// montage compute Worker, which runs the prototype's computation unchanged
// (ai/anas_montage). The frames come from the FFmpeg Worker, as the
// prototype took them: keyframes 512 wide (FFmpeg quality 3) measured by
// blurdetect on the JPEG, shake frames 320 × 180 ten a second, and, on dark
// keyframes, a grey frame 1920 wide for its sharp points.

const (
	cardsDerivation   = "montage-cards/v1"
	qualityDerivation = "montage-quality/v1"
	cardsIdentity     = cardsDerivation + "|card=0.86,look=2,long=120,side=20|still=0.9,gap=12,fewest=3,most=20|flat=8"
	qualityIdentity   = qualityDerivation + "|key=w512q3,blurdetect|shake=320x180@10,30s|blocked=256x144|points=w1920,dark=60" +
		"|rules=blur8.0,moving1.0,shake5.6,blocked0.25,points60"
	keyframeWidth   = 512
	keyframeQuality = 3
	shakeRate       = 10
	shakeChunk      = 30.0
	sharpWidth      = 1920
	darkLight       = 60
)

// CardWindow is a window handed to the compute Worker.
type CardWindow struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// ComputedCard is a card the compute Worker cut.
type ComputedCard struct {
	Start float64   `json:"start"`
	End   float64   `json:"end"`
	Times []float64 `json:"times"`
}

// CardMeasures are a card's measures, keyframe by keyframe; a nil blur is a
// black keyframe, a nil point count a light one.
type CardMeasures struct {
	Moves   [][2]float64
	Blurs   []*float64
	Blocked []float64
	Points  []*float64
}

// CardQuality is a card's quality, and why it cannot be used if it cannot.
type CardQuality struct {
	Blur    *float64   `json:"blur"`
	Shake   float64    `json:"shake"`
	Blocked float64    `json:"blocked"`
	Points  []*float64 `json:"points"`
	Reasons []string   `json:"reasons"`
}

// MontageCompute is the montage compute Worker as the photo library uses it.
type MontageCompute interface {
	Cards(ctx context.Context, windows []CardWindow, vectors []float32, dimensions int, flat []bool) ([]ComputedCard, error)
	ShakeMoves(ctx context.Context, frames []byte, count int) ([][2]float64, error)
	Keyframe(ctx context.Context, jpeg []byte) (blocked, light float64, err error)
	SharpPoints(ctx context.Context, frame []byte, width, height int) (float64, error)
	Quality(ctx context.Context, measures CardMeasures) (CardQuality, error)
}

// ErrComputeUnavailable: the compute Worker could not be reached; the work
// waits for it.
var ErrComputeUnavailable = errors.New("the montage compute worker is not available")

// cardOf is a stored card.
type cardOf struct {
	index      int
	start, end float64
	times      []float64
}

// montageCards runs a due card or quality job, one card's quality a round,
// and reports whether it did any work.
func (r *aiRunner) montageCards(ctx context.Context) (bool, error) {
	lib := r.lib
	if lib.compute == nil {
		return false, nil
	}
	if worked, err := lib.cutCards(ctx); worked || err != nil {
		return worked, err
	}
	return lib.measureCard(ctx, r)
}

// cutCards cuts the cards of a video whose windows are done.
func (s *Service) cutCards(ctx context.Context) (bool, error) {
	job, err := s.claimJob(ctx, cardsDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	windows, vectors, flat, dimensions, err := s.windowsOf(ctx, job.objectID)
	if err != nil {
		return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
	}
	if len(windows) == 0 {
		// The windows went, as when a newer model is computing them again.
		return true, s.withTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
			return err
		})
	}
	cards, err := s.compute.Cards(ctx, windows, vectors, dimensions, flat)
	switch {
	case errors.Is(err, ErrComputeUnavailable):
		return false, s.releaseJob(ctx, job)
	case err != nil:
		return true, s.failJob(ctx, job, jobErrorInvalidResult)
	}
	return true, s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_cards WHERE object_id = ?", job.objectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_card_quality WHERE object_id = ?", job.objectID); err != nil {
			return err
		}
		for n, card := range cards {
			times, _ := json.Marshal(card.Times)
			if _, err := tx.ExecContext(ctx, "INSERT INTO video_cards(object_id, card, start_ms, end_ms, keyframes, identity) VALUES(?, ?, ?, ?, ?, ?)",
				job.objectID, n+1, int(math.Round(card.Start*1000)), int(math.Round(card.End*1000)), string(times), cardsIdentity); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation); err != nil {
			return err
		}
		now := formatTime(s.now())
		_, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at) VALUES(?, ?, 'pending', ?, ?)
			ON CONFLICT DO NOTHING`, job.objectID, qualityDerivation, now, now)
		return err
	})
}

// windowsOf reads a video's windows for cutting its cards.
func (s *Service) windowsOf(ctx context.Context, object string) (windows []CardWindow, vectors []float32, flat []bool, dimensions int, err error) {
	rows, err := s.db.QueryContext(ctx, "SELECT start_ms, end_ms, flat, vector FROM video_windows WHERE object_id = ? ORDER BY idx", object)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var start, end int
		var isFlat bool
		var vector []byte
		if err := rows.Scan(&start, &end, &isFlat, &vector); err != nil {
			return nil, nil, nil, 0, err
		}
		decoded := decodeVector(vector)
		dimensions = len(decoded)
		windows, vectors, flat = append(windows, CardWindow{Start: float64(start) / 1000, End: float64(end) / 1000}),
			append(vectors, decoded...), append(flat, isFlat)
	}
	return windows, vectors, flat, dimensions, rows.Err()
}

// nextUnmeasured is the first card of object without its quality.
func (s *Service) nextUnmeasured(ctx context.Context, object string) (cardOf, bool, error) {
	var card cardOf
	var startMs, endMs int
	var times string
	err := s.db.QueryRowContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes FROM video_cards c
		WHERE c.object_id = ? AND NOT EXISTS (SELECT 1 FROM video_card_quality q WHERE q.object_id = c.object_id AND q.card = c.card)
		ORDER BY c.card LIMIT 1`, object).Scan(&card.index, &startMs, &endMs, &times)
	if errors.Is(err, sql.ErrNoRows) {
		return cardOf{}, false, nil
	}
	if err != nil {
		return cardOf{}, false, err
	}
	card.start, card.end = float64(startMs)/1000, float64(endMs)/1000
	return card, true, json.Unmarshal([]byte(times), &card.times)
}

// measureCard measures one card's quality: a round each, so the gate is
// asked between cards and a long video does not hold the queue.
func (s *Service) measureCard(ctx context.Context, r *aiRunner) (bool, error) {
	taker, ok := s.media.(FrameTaker)
	if !ok {
		return false, nil
	}
	job, err := s.claimJob(ctx, qualityDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	card, found, err := s.nextUnmeasured(ctx, job.objectID)
	if err != nil {
		return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
	}
	if !found {
		return true, s.withTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
			return err
		})
	}
	stopLease := s.keepLease(ctx, job)
	defer stopLease()
	r.progress.set(AIWorking, "", "")
	quality, err := s.cardQuality(ffmpegworker.WithPriority(ctx, ffmpegworker.Background), taker, job, card)
	switch {
	case ctx.Err() != nil:
		return false, s.returnJob(context.WithoutCancel(ctx), job)
	case errors.Is(err, ErrComputeUnavailable):
		return false, s.releaseJob(ctx, job)
	case err != nil:
		if class := decodeFailure(err); class == jobWaiting {
			return false, s.releaseJob(ctx, job)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return true, s.failJob(ctx, job, class)
		}
		return true, s.failJob(ctx, job, jobErrorMissingOriginal)
	}
	result, err := json.Marshal(quality)
	if err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO video_card_quality(object_id, card, result, identity) VALUES(?, ?, ?, ?)
		ON CONFLICT(object_id, card) DO UPDATE SET result = excluded.result, identity = excluded.identity`,
		job.objectID, card.index, string(result), qualityIdentity); err != nil {
		return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
	}
	// The next card comes in the next round, after the gate.
	return true, s.returnJob(ctx, job)
}

// cardQuality takes a card's frames and has the compute Worker judge them.
func (s *Service) cardQuality(ctx context.Context, taker FrameTaker, job claimedJob, card cardOf) (CardQuality, error) {
	var width, height int
	if err := s.db.QueryRowContext(ctx, "SELECT width, height FROM objects WHERE id = ?", job.objectID).Scan(&width, &height); err != nil {
		return CardQuality{}, err
	}
	original, err := s.openObject(job.objectID)
	if err != nil {
		return CardQuality{}, err
	}
	defer func() { _ = original.Close() }()
	measures := CardMeasures{}
	// Keyframes: 512 wide, blurdetect on the JPEG.
	var keyframes [][]byte
	result, err := taker.Frames(ctx, original, ffmpegworker.FramesRequest{Times: card.times, Format: ffmpegworker.FormatJPEG,
		Width: keyframeWidth, Quality: keyframeQuality, Blur: true}, func(frame []byte) error {
		keyframes = append(keyframes, frame)
		return nil
	})
	if err != nil {
		return CardQuality{}, err
	}
	if len(result.Blur) != len(keyframes) || len(keyframes) != len(card.times) {
		return CardQuality{}, fmt.Errorf("%w: %d keyframes for %d instants", ffmpegworker.ErrFailed, len(keyframes), len(card.times))
	}
	measures.Blurs = result.Blur
	for i, frame := range keyframes {
		blocked, light, err := s.compute.Keyframe(ctx, frame)
		if err != nil {
			return CardQuality{}, err
		}
		measures.Blocked = append(measures.Blocked, blocked)
		var points *float64
		if light <= darkLight {
			counted, err := s.sharpPoints(ctx, taker, original, result.Times[i], width, height)
			if err != nil {
				return CardQuality{}, err
			}
			points = &counted
		}
		measures.Points = append(measures.Points, points)
	}
	// Shake: 320 × 180 ten a second over the card, 30 s a chunk, each chunk
	// starting on the last frame of the one before.
	for at := card.start; at < card.end; at += shakeChunk - 1.0/shakeRate {
		end := min(card.end, at+shakeChunk)
		var frames []byte
		count := 0
		if _, err := taker.Frames(ctx, original, ffmpegworker.FramesRequest{Start: at, End: end, Rate: shakeRate,
			Format: ffmpegworker.FormatGray, Width: 320, Height: 180}, func(frame []byte) error {
			frames, count = append(frames, frame...), count+1
			return nil
		}); err != nil {
			return CardQuality{}, err
		}
		if count >= 2 {
			moves, err := s.compute.ShakeMoves(ctx, frames, count)
			if err != nil {
				return CardQuality{}, err
			}
			measures.Moves = append(measures.Moves, moves...)
		}
		if end >= card.end {
			break
		}
	}
	return s.compute.Quality(ctx, measures)
}

// sharpPoints counts the sharp points of the grey frame 1920 wide at at, the
// height FFmpeg's -2 gives.
func (s *Service) sharpPoints(ctx context.Context, taker FrameTaker, original *os.File, at float64, width, height int) (float64, error) {
	if width <= 0 || height <= 0 {
		return 0, fmt.Errorf("%w: the video's size is unknown", ffmpegworker.ErrUnreadable)
	}
	tall := int(math.Round(float64(sharpWidth)*float64(height)/float64(width)/2)) * 2
	var frame []byte
	if _, err := taker.Frames(ctx, original, ffmpegworker.FramesRequest{Times: []float64{at}, Format: ffmpegworker.FormatGray,
		Width: sharpWidth, Height: tall}, func(f []byte) error {
		frame = f
		return nil
	}); err != nil {
		return 0, err
	}
	return s.compute.SharpPoints(ctx, frame, sharpWidth, tall)
}

// CardAnalysis is one card of a video's montage analysis.
type CardAnalysis struct {
	Index   int          `json:"card"`
	StartMs int          `json:"startMs"`
	EndMs   int          `json:"endMs"`
	Times   []float64    `json:"times"`
	Quality *CardQuality `json:"quality,omitempty"`
}

// cardsOf reads a video's cards and the quality measured so far.
func (s *Service) cardsOf(ctx context.Context, object string) ([]CardAnalysis, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes, q.result FROM video_cards c
		LEFT JOIN video_card_quality q ON q.object_id = c.object_id AND q.card = c.card WHERE c.object_id = ? ORDER BY c.card`, object)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []CardAnalysis
	for rows.Next() {
		var card CardAnalysis
		var times string
		var quality sql.NullString
		if err := rows.Scan(&card.Index, &card.StartMs, &card.EndMs, &times, &quality); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(times), &card.Times); err != nil {
			return nil, err
		}
		if quality.Valid {
			card.Quality = &CardQuality{}
			if err := json.Unmarshal([]byte(quality.String), card.Quality); err != nil {
				return nil, err
			}
		}
		cards = append(cards, card)
	}
	return cards, rows.Err()
}

// derivationState says where one montage derivation of object stands.
func (s *Service) derivationState(ctx context.Context, object, derivation, done string) (AnalysisState, error) {
	var state string
	err := s.db.QueryRowContext(ctx, "SELECT state FROM jobs WHERE object_id = ? AND derivation = ?", object, derivation).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var exists bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS ("+done+")", object).Scan(&exists); err != nil || !exists {
			return AnalysisNone, err
		}
		return AnalysisDone, nil
	case err != nil:
		return AnalysisNone, err
	case state == "failed":
		return AnalysisFailed, nil
	case state == "running":
		return AnalysisWorking, nil
	}
	return AnalysisQueued, nil
}

func (s *Service) cardsState(ctx context.Context, object string) (AnalysisState, error) {
	return s.derivationState(ctx, object, cardsDerivation, "SELECT 1 FROM video_cards WHERE object_id = ?")
}

func (s *Service) qualityState(ctx context.Context, object string) (AnalysisState, error) {
	state, err := s.derivationState(ctx, object, qualityDerivation, "SELECT 1 FROM video_card_quality WHERE object_id = ?")
	if err == nil && state == AnalysisDone {
		// Done only once every card has its quality.
		var missing bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM video_cards c WHERE c.object_id = ?
			AND NOT EXISTS (SELECT 1 FROM video_card_quality q WHERE q.object_id = c.object_id AND q.card = c.card))`, object).Scan(&missing)
		if missing {
			state = AnalysisQueued
		}
	}
	return state, err
}
