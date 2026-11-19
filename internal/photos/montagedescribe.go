package photos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

// The montage center's card descriptions, montage-describe/v1
// (docs/architecture/photo-ai.md, 剪辑中心的分析): the text reader writes the
// beats, process, scene and camera of every card whose picture passed the
// quality check, from its key frames. A card a round, in the reading phase:
// the reader shares llama-server and its memory with the text in photos.

const (
	describeDerivation = "montage-describe/v1"
	// describeTemplate is the prompt the Worker describes with; an answer
	// under another is a Worker that changed.
	describeTemplate = "montage-card/v1"
	// describeIdentitySuffix is what a description is besides the reader
	// and the cards: the template and its genre, the key frames (512 wide,
	// FFmpeg quality 3, the prototype's stills) and the version of the
	// number filter, which textread does not version itself.
	describeIdentitySuffix = "|tpl=" + describeTemplate + ",genre=TRAVEL,frame=w512q3,mask=1"
)

// AnalysisUnavailable says the analysis waits for a local model the AI
// Worker does not have: the descriptions, without a text reader.
const AnalysisUnavailable AnalysisState = "unavailable"

// describeIdentity is the identity of descriptions written by reader of
// cards measured under qualityIdentity, which also identifies their source windows.
func describeIdentity(reader, qualityIdentity string) string {
	return reader + describeIdentitySuffix + "|quality=" + qualityIdentity
}

// CardDescription is one card's description, or why it has none: such a
// card stays out of the montage center's catalog, and its video's other
// cards go on.
type CardDescription struct {
	Card        int             `json:"card"`
	Description json.RawMessage `json:"description,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// queueDescribe queues missing descriptions in the transaction that completed
// quality. A reader installed later will queue these when the runner starts.
func (s *Service) queueDescribe(ctx context.Context, tx *sql.Tx, object string) error {
	reader, _ := s.ai.descriptionReader()
	return s.queueMissingDescriptions(ctx, tx, reader, true, "?", object)
}

// The identity of the inputs to a description and the cards eligible to be
// described. The same predicates are used for lookup and requeue.
const eligibleQuality = "json_type(q.result, '$.reasons') = 'array' AND json_array_length(q.result, '$.reasons') = 0"

func (r *aiRunner) descriptionReader() (reader string, unavailable bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reader, r.model != "" && r.reader == ""
}

// fillDescriptionProgress checks all available videos in a lookup in one
// query, including cards for which a previous model left an error.
func (s *Service) fillDescriptionProgress(ctx context.Context, progress map[string]analysisProgress, in string, objects []any) error {
	reader, unavailable := s.ai.descriptionReader()
	return s.eachRow(ctx, `SELECT m.object_id, m.quality_identity, COALESCE(j.state, ''),
        COUNT(q.card), COUNT(d.identity), COALESCE(MIN(d.identity), ''), COALESCE(MAX(d.identity), '')
        FROM montage_card_results m
        LEFT JOIN jobs j ON j.object_id = m.object_id AND j.derivation = ?
        LEFT JOIN video_card_quality q ON q.object_id = m.object_id AND q.identity = m.quality_identity AND `+eligibleQuality+`
        LEFT JOIN video_card_descriptions d ON d.object_id = q.object_id AND d.card = q.card
        WHERE m.object_id IN (`+in+`) GROUP BY m.object_id`, append([]any{describeDerivation}, objects...), func(rows *sql.Rows) error {
		var object, input, job, first, last string
		var eligible, described int
		if err := rows.Scan(&object, &input, &job, &eligible, &described, &first, &last); err != nil {
			return err
		}
		p := progress[object]
		if p.quality != AnalysisDone || p.qualityIdentity != input {
			return nil
		}
		identity := describeIdentity(reader, input)
		switch {
		case eligible == 0:
			// An empty result depends only on quality; no model produced it.
			p.described, p.descriptionIdentity = AnalysisDone, describeIdentity("", input)
		case unavailable:
			p.described = AnalysisUnavailable
		case job != "":
			p.described = montageStageState(job, "")
		case eligible == described && first == last && (first == identity || reader == "" && strings.HasSuffix(first, identity)):
			p.described, p.descriptionIdentity, p.describedCount = AnalysisDone, first, described
		}
		progress[object] = p
		return nil
	})
}

// cardDescriptions are the stored descriptions of object, in card order.
func (s *Service) cardDescriptions(ctx context.Context, object, identity string) ([]CardDescription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.card, IFNULL(d.result, ''), d.error FROM video_card_descriptions d
 JOIN video_card_quality q ON q.object_id=d.object_id AND q.card=d.card
 WHERE d.object_id=? AND d.identity=? AND `+eligibleQuality+` ORDER BY d.card`, object, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []CardDescription
	for rows.Next() {
		var card CardDescription
		var result string
		if err := rows.Scan(&card.Card, &result, &card.Error); err != nil {
			return nil, err
		}
		if result != "" {
			card.Description = json.RawMessage(result)
		}
		found = append(found, card)
	}
	return found, rows.Err()
}

// describeCard is a card to describe: its span and key frames, in
// milliseconds from the video's start (the cards store the key frames in
// seconds).
type describeCard struct {
	card           int
	startMs, endMs int
	keyframes      []int
	identity       string
}

// nextDescription reads only the next eligible card's keyframes. A second
// matching row tells the runner to keep the job for another round.
func (s *Service) nextDescription(ctx context.Context, object, reader string) (*describeCard, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes, q.identity
		FROM video_cards c JOIN video_card_quality q USING (object_id, card)
		JOIN montage_card_results m ON m.object_id = c.object_id AND m.cards_identity = c.identity AND m.quality_identity = q.identity
		LEFT JOIN video_card_descriptions d ON d.object_id = c.object_id AND d.card = c.card
		WHERE c.object_id = ? AND `+eligibleQuality+` AND (d.identity IS NULL OR d.identity <> ? || q.identity)
		ORDER BY c.card LIMIT 2`, object, describeIdentity(reader, ""))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	var card describeCard
	var keyframes string
	if err := rows.Scan(&card.card, &card.startMs, &card.endMs, &keyframes, &card.identity); err != nil {
		return nil, false, err
	}
	var seconds []float64
	if err := json.Unmarshal([]byte(keyframes), &seconds); err != nil {
		return nil, false, fmt.Errorf("card %d of %s: key frames: %w", card.card, object, err)
	}
	for _, at := range seconds {
		card.keyframes = append(card.keyframes, int(math.Round(at*1000)))
	}
	more := rows.Next()
	return &card, more, rows.Err()
}

// cardLayout places a card's key frames, at instants in milliseconds from
// the video's start, for the Worker as the prototype does: each frame a
// beat of its own.
func cardLayout(card describeCard, instants []int) CardLayout {
	layout := CardLayout{Duration: float64(card.endMs-card.startMs) / 1000}
	for i, at := range instants {
		layout.Offsets = append(layout.Offsets, float64(max(0, at-card.startMs))/1000)
		layout.Groups = append(layout.Groups, i+1)
	}
	return layout
}

// maskDescription replaces the numbers textread's rules find in every
// text of a description with their label: the description reaches a
// member's assistant, and the library keeps no original.
func maskDescription(description json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(description))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var masked bytes.Buffer
	encoder := json.NewEncoder(&masked)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(maskTexts(value)); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(masked.Bytes())), nil
}

func maskTexts(value any) any {
	switch v := value.(type) {
	case string:
		return textread.MaskNumbers(v, func(number textread.Number) string { return "[" + number.Label + "]" })
	case []any:
		for i := range v {
			v[i] = maskTexts(v[i])
		}
	case map[string]any:
		for key, inner := range v {
			v[key] = maskTexts(inner)
		}
	}
	return value
}

const describeReady = qualityReady + " AND NOT EXISTS (SELECT 1 FROM jobs p WHERE p.object_id = j.object_id AND p.derivation = '" + qualityDerivation + "')"

// montageDescribe describes one card of a due description job. It reports
// whether it claimed one, which ends the round, and whether it did any
// work; a job that only waits for memory to load the reader goes back
// unclaimed, so that the round goes on with the photos' own work. A card
// that cannot be described, past the context or with an answer that never
// fits, is stored as failed and the video goes on. A job with cards left
// goes back to the head of the queue without spending its attempt.
func (r *aiRunner) montageDescribe(ctx context.Context, embedder Embedder, gate Gate, info EmbedderInfo) (claimed, processed bool, err error) {
	lib := r.lib
	describer, ok := embedder.(CardDescriber)
	taker, taking := lib.media.(FrameTaker)
	if !ok || !taking || info.OCR == "" {
		return false, false, nil
	}
	now := formatTime(lib.now())
	var due bool
	err = lib.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs j WHERE j.derivation = ?
		AND ((j.state = 'pending' AND j.not_before <= ?) OR (j.state = 'running' AND j.lease_until <= ?))
		AND `+describeReady+`)`, describeDerivation, now, now).Scan(&due)
	if err != nil || !due {
		return false, false, err
	}
	job, err := lib.claimJobWhere(ctx, describeDerivation, "", "j.not_before, j.object_id", describeReady)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !job.video || job.duration <= 0 {
		return true, true, lib.failJob(ctx, job, jobErrorUndecodable)
	}
	card, more, err := lib.nextDescription(ctx, job.objectID, info.OCR)
	if err != nil {
		return true, false, errors.Join(err, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	if card == nil {
		_, err := lib.db.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ? AND state = 'running'", job.objectID, job.derivation)
		return true, true, err
	}
	detector, _ := embedder.(Detector)
	if open, reason := gate.Open(ctx, info.OCRMemoryBytes); !open {
		r.progress.set(AIPaused, reason, "")
		r.releaseModels(ctx, detector, reason)
		return reason != "memory_for_models", false, lib.returnJob(ctx, job)
	}
	r.switchPhase(ctx, detector, true)
	r.progress.set(AIWorking, "", info.Model)
	original, err := lib.openObject(job.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return true, true, lib.failJob(ctx, job, jobErrorMissingOriginal)
	}
	if err != nil {
		// A fault of storage, as for the card's quality (cardQuality): tried
		// again with an attempt spent, not once a minute for ever.
		return true, true, lib.failJob(ctx, job, jobErrorIO)
	}
	defer func() { _ = original.Close() }()
	stopLease := lib.keepLease(ctx, job)
	defer stopLease()

	// The frames: someone using the library or short memory hangs FFmpeg
	// up, as for the windows; other pressure lets the card's frames finish.
	working, cancel := context.WithCancel(ctx)
	defer cancel()
	stopFrames := watchGate(working, continuing(gate), info.OCRMemoryBytes, cancel)
	instants := card.keyframes
	times := make([]float64, len(instants))
	for i, at := range instants {
		times[i] = float64(at) / 1000
	}
	var frames []*os.File
	defer func() {
		for _, frame := range frames {
			_ = frame.Close()
		}
	}()
	request := ffmpegworker.FramesRequest{Times: times, Format: ffmpegworker.FormatJPEG, Width: keyframeWidth, Quality: keyframeQuality}
	_, err = taker.Frames(ffmpegworker.WithPriority(working, ffmpegworker.Background), original, request, func(frame []byte) error {
		file, err := memoryFile(frame)
		if err == nil {
			frames = append(frames, file)
		}
		return err
	})
	reason := stopFrames()
	cancel()
	if reason != "" || ctx.Err() != nil {
		return true, false, r.pauseDescribe(ctx, job, detector, reason)
	}
	if err == nil && len(frames) != len(times) {
		err = ffmpegworker.ErrFailed
	}
	if err != nil {
		class := decodeFailure(err)
		if class == jobWaiting {
			return true, false, lib.releaseJob(ctx, job)
		}
		if class != jobErrorUndecodable && job.attempts < jobMaxAttempts {
			return true, true, lib.failJob(ctx, job, class)
		}
		return true, true, lib.finishDescription(ctx, job, *card, info.OCR, nil, class, 0, !more)
	}

	// The description is hung up for the same reasons, within the watch's
	// interval: it takes minutes on the NAS.
	describing, stop := context.WithCancel(ctx)
	defer stop()
	stopWatching := watchGate(describing, continuing(gate), info.OCRMemoryBytes, stop)
	r.markReaderLoaded()
	started := lib.now()
	model, template, description, err := describer.DescribeFrames(describing, frames, cardLayout(*card, instants))
	paused := stopWatching()
	stop()
	took := lib.now().Sub(started)
	var failure string
	switch {
	case ctx.Err() != nil, paused != "":
		return true, false, r.pauseDescribe(ctx, job, detector, paused)
	case errors.Is(err, ErrAIUnavailable) || (err == nil && model != info.OCR):
		r.forgetModels()
		return true, false, lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected) || errors.Is(err, ErrAIInvalidResult):
		failure = err.Error()
	case err != nil:
		if job.attempts < jobMaxAttempts {
			return true, true, lib.failJob(ctx, job, jobErrorIO)
		}
		failure = jobErrorIO
	case template != describeTemplate:
		failure = "unsupported description template"
	default:
		if description, err = maskDescription(description); err != nil {
			failure = jobErrorInvalidResult
		}
	}
	if failure != "" {
		description = nil
	}
	return true, true, lib.finishDescription(ctx, job, *card, info.OCR, description, failure, took, !more)
}

// pauseDescribe puts a stopped description job back in its place; a gate
// that closed frees the models as it does for other work.
func (r *aiRunner) pauseDescribe(ctx context.Context, job claimedJob, detector Detector, reason string) error {
	if reason != "" {
		r.progress.set(AIPaused, reason, "")
	}
	err := r.lib.returnJob(context.WithoutCancel(ctx), job)
	if reason != "" && ctx.Err() == nil {
		r.releaseModels(ctx, detector, reason)
	}
	return err
}

// finishDescription records one card and resets the retry budget for the next
// in the same transaction. A purge or recut may have retired the running job.
func (s *Service) finishDescription(ctx context.Context, job claimedJob, card describeCard, reader string, description json.RawMessage, failure string, took time.Duration, last bool) error {
	var result any
	if description != nil {
		result = string(description)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var running bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs j JOIN montage_card_results m USING (object_id)
			WHERE j.object_id = ? AND j.derivation = ? AND j.state = 'running' AND j.attempts = ?
			AND m.quality_identity = ? AND `+describeReady+`)`, job.objectID, job.derivation, job.attempts, card.identity).Scan(&running)
		if err != nil || !running {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO video_card_descriptions(object_id, card, result, error, identity, millis)
			VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(object_id, card) DO UPDATE SET result = excluded.result, error = excluded.error,
			identity = excluded.identity, millis = excluded.millis`,
			job.objectID, card.card, result, strings.TrimSpace(failure), describeIdentity(reader, card.identity), took.Milliseconds())
		if err != nil {
			return err
		}
		if last {
			_, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET state = 'pending', attempts = 0, error_class = '', lease_until = NULL WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
}

// queueMissingDescriptions queues only current, eligible inputs whose result
// is missing or stale, after all earlier stages have finished. scope selects
// either an analyzed object's id or the objects still held by live grants.
func (s *Service) queueMissingDescriptions(ctx context.Context, q queryer, reader string, resetFailed bool, scope string, arg ...any) error {
	if reader == "" {
		return nil
	}
	now := formatTime(s.now())
	args := []any{describeDerivation, now, now}
	args = append(args, arg...)
	args = append(args, windowsDerivation, cardsDerivation, qualityDerivation, describeIdentity(reader, ""), resetFailed)
	_, err := q.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
  SELECT DISTINCT c.object_id, ?, 'pending', ?, ? FROM video_cards c
  JOIN montage_card_results m ON m.object_id=c.object_id AND m.cards_identity=c.identity
  JOIN video_card_quality q ON q.object_id=c.object_id AND q.card=c.card AND q.identity=m.quality_identity
  LEFT JOIN video_card_descriptions d ON d.object_id=c.object_id AND d.card=c.card
  WHERE c.object_id IN (`+scope+`)
  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.object_id=c.object_id AND j.derivation IN (?, ?, ?))
  AND `+eligibleQuality+` AND (d.identity IS NULL OR d.identity <> ? || q.identity)
  ON CONFLICT(object_id,derivation) DO UPDATE SET state='pending', attempts=0, lease_until=NULL,
   not_before=excluded.not_before, error_class='' WHERE jobs.state='failed' AND ?`, args...)
	return err
}
