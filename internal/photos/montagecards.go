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
// (ai/anas_videocut). The frames come from the FFmpeg Worker, as the
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
var (
	ErrComputeUnavailable   = errors.New("the montage compute worker is not available")
	ErrComputeFailed        = errors.New("the montage compute worker failed")
	ErrComputeInvalidResult = errors.New("the montage compute worker refused the request")
)

// A successor may only use completed inputs. A failed predecessor also
// blocks its successors until the user retries that analysis. cardsAfter and
// qualityAfter are that alone; cardsReady and qualityReady also want a grant
// that still refers to the video.
const cardsAfter = "NOT EXISTS (SELECT 1 FROM jobs p WHERE p.object_id = j.object_id AND p.derivation = '" + windowsDerivation + "')"
const qualityAfter = cardsAfter + " AND NOT EXISTS (SELECT 1 FROM jobs p WHERE p.object_id = j.object_id AND p.derivation = '" + cardsDerivation + "')"
const cardsReady = "j.object_id IN (" + referencedObjects + ") AND " + cardsAfter
const qualityReady = "j.object_id IN (" + referencedObjects + ") AND " + qualityAfter

func cardsVersion(window string) string  { return cardsIdentity + "|input=" + window }
func qualityVersion(cards string) string { return qualityIdentity + "|input=" + cards }

func (s *Service) finishMontageError(ctx context.Context, job claimedJob, err error) (bool, error) {
	switch {
	case ctx.Err() != nil:
		return false, s.returnJob(context.WithoutCancel(ctx), job)
	case errors.Is(err, ErrComputeUnavailable), errors.Is(err, ffmpegworker.ErrUnavailable), errors.Is(err, context.Canceled):
		return false, s.releaseJob(ctx, job)
	case errors.Is(err, ErrComputeInvalidResult):
		return true, s.failJob(ctx, job, jobErrorInvalidResult)
	case errors.Is(err, ErrComputeFailed), errors.Is(err, context.DeadlineExceeded):
		return true, s.failJob(ctx, job, jobErrorIO)
	case errors.Is(err, fs.ErrNotExist):
		return true, s.failJob(ctx, job, jobErrorMissingOriginal)
	default:
		return true, s.failJob(ctx, job, decodeFailure(err))
	}
}

// enqueueMissingCards also covers videos whose windows completed on M5.
func (s *Service) enqueueMissingCards(ctx context.Context, tx *sql.Tx, object, now string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
        SELECT j.object_id, ?, 'pending', ?, ? FROM montage_objects j
        JOIN video_windows w ON w.object_id = j.object_id AND w.idx = 0
        LEFT JOIN montage_card_results m USING (object_id)
        WHERE j.object_id = ? AND `+cardsReady+` AND COALESCE(m.cards_identity, '') <> ? || w.identity
        ON CONFLICT DO NOTHING`, cardsDerivation, now, now, object, cardsVersion("")); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
        SELECT j.object_id, ?, 'pending', ?, ? FROM montage_card_results j
        WHERE j.object_id = ? AND `+qualityReady+` AND quality_identity <> ? || cards_identity
        ON CONFLICT DO NOTHING`, qualityDerivation, now, now, object, qualityVersion(""))
	return err
}

func resetMontageJob(ctx context.Context, tx *sql.Tx, object, derivation, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at) VALUES (?, ?, 'pending', ?, ?)
        ON CONFLICT(object_id, derivation) DO UPDATE SET state = 'pending', attempts = 0, lease_until = NULL,
            not_before = excluded.not_before, error_class = '' WHERE jobs.state <> 'running'`, object, derivation, now, now)
	return err
}

// cardOf is a stored card.
type cardOf struct {
	index      int
	start, end float64
	times      []float64
}

// dropUnreferencedCards removes the card and quality jobs nobody is working
// on of videos no grant refers to any more, as dropUnreferencedWindows does
// the window jobs: no round would take them, and they would wait for
// PruneMontageAnalysis. What they did stays until then; a grant asking again
// queues what is missing (enqueueMissingCards).
func (s *Service) dropUnreferencedCards(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE derivation IN (?, ?)
        AND (state = 'pending' OR (state = 'running' AND lease_until <= ?))
        AND object_id NOT IN (`+referencedObjects+`)`, cardsDerivation, qualityDerivation, formatTime(s.now()))
	return err
}

// montageCards runs a due card or quality job, one card's quality a round.
// It reports whether it did any work, and whether the round ends with it, as
// a chunk of windows does (montageWindows): the gate hung the work up, or
// the service is stopping. A compute or FFmpeg Worker that is away does not
// end the round: its job waits aiRetryDelay without spending an attempt, and
// the runner looks again no later than that, so ending every round on it
// would keep the photos' own work waiting for as long as the Worker is away.
func (r *aiRunner) montageCards(ctx context.Context, gate Gate, detector Detector) (worked, ended bool, err error) {
	lib := r.lib
	if lib.compute == nil {
		return false, false, nil
	}
	if open, _ := gate.Open(ctx, 0); !open {
		return false, false, nil
	}
	// Most rounds have none: a read says so before any write. It also sees
	// the jobs of videos no grant refers to any more, which go first.
	now := formatTime(lib.now())
	var due bool
	err = lib.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs j
        WHERE ((j.derivation = ? AND `+cardsAfter+`) OR (j.derivation = ? AND `+qualityAfter+`))
        AND ((j.state = 'pending' AND j.not_before <= ?) OR (j.state = 'running' AND j.lease_until <= ?)))`,
		cardsDerivation, qualityDerivation, now, now).Scan(&due)
	if err != nil || !due {
		return false, false, err
	}
	if err := lib.dropUnreferencedCards(ctx); err != nil {
		return false, false, err
	}
	// Someone using the library, or short memory, hangs the work up at once.
	working, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatching := watchGate(working, continuing(gate), 0, cancel)
	worked, err = lib.cutCards(working)
	if !worked && err == nil && working.Err() == nil {
		worked, err = lib.measureCard(working, r)
	}
	paused := stopWatching()
	switch {
	case ctx.Err() != nil:
		// Stopping: the job went back to its place with its attempt.
		return worked, true, err
	case paused != "":
		// Hung up: the round ends here, as a hung-up chunk of windows does.
		r.progress.set(AIPaused, paused, "")
		r.releaseModels(ctx, detector, paused)
		return worked, true, err
	}
	return worked, false, err
}

// cutCards cuts the cards of a video whose windows are done.
func (s *Service) cutCards(ctx context.Context) (bool, error) {
	job, err := s.claimJobWhere(ctx, cardsDerivation, "", "j.not_before, j.object_id", cardsReady)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stopLease := s.keepLease(ctx, job)
	defer stopLease()
	windows, vectors, flat, dimensions, identity, err := s.windowsOf(ctx, job.objectID)
	if err != nil {
		return s.finishMontageError(ctx, job, errors.Join(ErrComputeFailed, err))
	}
	if len(windows) == 0 {
		return s.finishMontageError(ctx, job, ErrComputeFailed)
	}
	cards, err := s.compute.Cards(ctx, windows, vectors, dimensions, flat)
	if err != nil || ctx.Err() != nil {
		return s.finishMontageError(ctx, job, err)
	}
	version := cardsVersion(identity)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var current bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs j WHERE j.object_id = ? AND j.derivation = ? AND j.state = 'running' AND j.attempts = ? AND "+cardsReady+
			" AND EXISTS (SELECT 1 FROM video_windows w WHERE w.object_id = j.object_id AND w.identity = ?))",
			job.objectID, cardsDerivation, job.attempts, identity).Scan(&current); err != nil {
			return err
		}
		if !current {
			_, err := tx.ExecContext(ctx, "UPDATE jobs SET state = 'pending', attempts = 0, lease_until = NULL WHERE object_id = ? AND derivation = ? AND state = 'running' AND attempts = ?",
				job.objectID, job.derivation, job.attempts)
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_cards WHERE object_id = ?", job.objectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_card_quality WHERE object_id = ?", job.objectID); err != nil {
			return err
		}
		// New cards are described anew, even if the old job exhausted its retries.
		if _, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, describeDerivation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_card_descriptions WHERE object_id = ?", job.objectID); err != nil {
			return err
		}
		for n, card := range cards {
			times, _ := json.Marshal(card.Times)
			if _, err := tx.ExecContext(ctx, "INSERT INTO video_cards(object_id, card, start_ms, end_ms, keyframes, identity) VALUES(?, ?, ?, ?, ?, ?)",
				job.objectID, n+1, int(math.Round(card.Start*1000)), int(math.Round(card.End*1000)), string(times), version); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO montage_card_results(object_id, cards_identity) VALUES (?, ?)
            ON CONFLICT(object_id) DO UPDATE SET cards_identity = excluded.cards_identity, quality_identity = ''`, job.objectID, version); err != nil {
			return err
		}
		now := formatTime(s.now())
		return resetMontageJob(ctx, tx, job.objectID, qualityDerivation, now)
	})
	if err != nil {
		return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
	}
	return true, nil
}

// windowsOf reads a video's windows for cutting its cards.
func (s *Service) windowsOf(ctx context.Context, object string) (windows []CardWindow, vectors []float32, flat []bool, dimensions int, identity string, err error) {
	rows, err := s.db.QueryContext(ctx, "SELECT start_ms, end_ms, flat, vector, identity FROM video_windows WHERE object_id = ? ORDER BY idx", object)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var start, end int
		var isFlat bool
		var vector []byte
		var windowIdentity string
		if err := rows.Scan(&start, &end, &isFlat, &vector, &windowIdentity); err != nil {
			return nil, nil, nil, 0, "", err
		}
		decoded := decodeVector(vector)
		if len(windows) == 0 {
			dimensions, identity = len(decoded), windowIdentity
		} else if identity != windowIdentity || dimensions != len(decoded) {
			return nil, nil, nil, 0, "", ErrComputeFailed
		}
		windows, vectors, flat = append(windows, CardWindow{Start: float64(start) / 1000, End: float64(end) / 1000}),
			append(vectors, decoded...), append(flat, isFlat)
	}
	return windows, vectors, flat, dimensions, identity, rows.Err()
}

// nextUnmeasured is the first card of object without its quality.
func (s *Service) nextUnmeasured(ctx context.Context, object, cardsID string) (cardOf, bool, error) {
	var card cardOf
	var startMs, endMs int
	var times string
	err := s.db.QueryRowContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes FROM video_cards c
		WHERE c.object_id = ? AND c.identity = ? AND NOT EXISTS (
            SELECT 1 FROM video_card_quality q WHERE q.object_id = c.object_id AND q.card = c.card AND q.identity = ?)
		ORDER BY c.card LIMIT 1`, object, cardsID, qualityVersion(cardsID)).Scan(&card.index, &startMs, &endMs, &times)
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
	job, err := s.claimJobWhere(ctx, qualityDerivation, "", "j.not_before, j.object_id", qualityReady)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var version string
	if err := s.db.QueryRowContext(ctx, "SELECT cards_identity FROM montage_card_results WHERE object_id = ?", job.objectID).Scan(&version); err != nil {
		return s.finishMontageError(ctx, job, errors.Join(ErrComputeFailed, err))
	}
	card, found, err := s.nextUnmeasured(ctx, job.objectID, version)
	if err != nil {
		return s.finishMontageError(ctx, job, errors.Join(ErrComputeFailed, err))
	}
	if !found {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			updated, err := tx.ExecContext(ctx, `UPDATE montage_card_results SET quality_identity = ?
                WHERE object_id = ? AND cards_identity = ? AND EXISTS (SELECT 1 FROM jobs j WHERE
                    j.object_id = montage_card_results.object_id AND j.derivation = ? AND
                    j.state = 'running' AND j.attempts = ? AND `+qualityReady+`)`,
				qualityVersion(version), job.objectID, version, job.derivation, job.attempts)
			if err != nil {
				return err
			}
			if count, err := updated.RowsAffected(); err != nil || count == 0 {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation); err != nil {
				return err
			}
			return s.queueDescribe(ctx, tx, job.objectID)
		})
		if err != nil {
			return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
		}
		return true, nil
	}
	stopLease := s.keepLease(ctx, job)
	defer stopLease()
	r.progress.set(AIWorking, "", "")
	quality, err := s.cardQuality(ffmpegworker.WithPriority(ctx, ffmpegworker.Background), taker, job, card)
	if err != nil || ctx.Err() != nil {
		return s.finishMontageError(ctx, job, err)
	}
	result, err := json.Marshal(quality)
	if err != nil {
		return s.finishMontageError(ctx, job, errors.Join(ErrComputeInvalidResult, err))
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO video_card_quality(object_id, card, result, identity)
            SELECT ?, ?, ?, ? FROM jobs j JOIN montage_card_results m USING (object_id)
            WHERE j.object_id = ? AND j.derivation = ? AND j.state = 'running' AND j.attempts = ?
                AND m.cards_identity = ? AND `+qualityReady+`
            ON CONFLICT(object_id, card) DO UPDATE SET result = excluded.result, identity = excluded.identity`,
			job.objectID, card.index, string(result), qualityVersion(version), job.objectID, job.derivation, job.attempts, version)
		if err != nil {
			return err
		}
		// The next card comes in the next round, after the gate.
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET state = 'pending', attempts = 0, lease_until = NULL, error_class = '' WHERE object_id = ? AND derivation = ? AND state = 'running' AND attempts = ?",
			job.objectID, job.derivation, job.attempts)
		return err
	})
	if err != nil {
		return false, errors.Join(err, s.returnJob(context.WithoutCancel(ctx), job))
	}
	return true, nil
}

// cardQuality takes a card's frames and has the compute Worker judge them.
func (s *Service) cardQuality(ctx context.Context, taker FrameTaker, job claimedJob, card cardOf) (CardQuality, error) {
	var width, height int
	if err := s.db.QueryRowContext(ctx, "SELECT width, height FROM objects WHERE id = ?", job.objectID).Scan(&width, &height); err != nil {
		return CardQuality{}, errors.Join(ErrComputeFailed, err)
	}
	original, err := s.openObject(job.objectID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// A storage fault, not an original that cannot be read: tried again
		// like the Catalog's above, and as a thumbnail's is (renderJob).
		return CardQuality{}, errors.Join(ErrComputeFailed, err)
	}
	if err != nil {
		return CardQuality{}, err
	}
	defer func() { _ = original.Close() }()
	measures := CardMeasures{Moves: make([][2]float64, 0)}
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
	if len(result.Times) != len(keyframes) || len(result.Blur) != len(keyframes) || len(keyframes) != len(card.times) {
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
		// The range is half-open. FFmpeg may emit one extra frame at an
		// exact boundary, which would duplicate the overlap of two chunks.
		limit := int(math.Ceil((end-at)*shakeRate - 1e-6))
		if _, err := taker.Frames(ctx, original, ffmpegworker.FramesRequest{Start: at, End: end, Rate: shakeRate,
			Format: ffmpegworker.FormatGray, Width: 320, Height: 180}, func(frame []byte) error {
			if count < limit {
				frames, count = append(frames, frame...), count+1
			}
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
	Description *CardDescription `json:"description,omitempty"`
	Index       int              `json:"card"`
	StartMs     int              `json:"startMs"`
	EndMs       int              `json:"endMs"`
	Times       []float64        `json:"times"`
	Quality     *CardQuality     `json:"quality,omitempty"`
}

// cardsOf reads a video's cards and the quality measured so far.
func (s *Service) cardsOf(ctx context.Context, object, identity string) ([]CardAnalysis, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes, q.result FROM video_cards c
		LEFT JOIN video_card_quality q ON q.object_id = c.object_id AND q.card = c.card AND q.identity = ?
        WHERE c.object_id = ? AND c.identity = ? ORDER BY c.card`, qualityVersion(identity), object, identity)
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

func montageStageState(job, identity string) AnalysisState {
	switch job {
	case "failed":
		return AnalysisFailed
	case "running":
		return AnalysisWorking
	case "pending":
		return AnalysisQueued
	}
	if identity != "" {
		return AnalysisDone
	}
	return AnalysisNone
}
