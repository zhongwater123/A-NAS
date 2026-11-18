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
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The montage center's card descriptions, montage-describe/v1
// (docs/architecture/montage-center.md#卡片描述): the text reader writes the
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
// cards of cardsIdentity: new cards make new descriptions.
func describeIdentity(reader, cardsIdentity string) string {
	return reader + describeIdentitySuffix + "|cards=" + cardsIdentity
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
const descriptionInputs = "c.identity || '|quality=' || q.identity"
const eligibleQuality = "json_type(q.result, '$.reasons') = 'array' AND json_array_length(q.result, '$.reasons') = 0"

func (r *aiRunner) descriptionReader() (reader string, unavailable bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reader, r.model != "" && r.reader == ""
}

// fillDescriptionProgress checks all available videos in a lookup in one
// query, including cards for which a previous model left an error.
func (s *Service) fillDescriptionProgress(ctx context.Context, assets []GrantedAsset) error {
	reader, unavailable := s.ai.descriptionReader()
	var ids []any
	for _, asset := range assets {
		if asset.Status == GrantAvailable {
			ids = append(ids, asset.ObjectID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	in := sqlitedb.Placeholders(len(ids))
	type progress struct {
		job, input, first, last string
		eligible, described     int
	}
	byObject := make(map[string]progress)
	err := s.eachRow(ctx, `SELECT m.object_id, IFNULL(j.state, ''), IFNULL(MIN(`+descriptionInputs+`), ''),
  COUNT(*) FILTER (WHERE `+eligibleQuality+`),
  COUNT(d.identity) FILTER (WHERE `+eligibleQuality+`),
  IFNULL(MIN(d.identity) FILTER (WHERE `+eligibleQuality+`), ''),
  IFNULL(MAX(d.identity) FILTER (WHERE `+eligibleQuality+`), '')
  FROM montage_objects m
  LEFT JOIN jobs j ON j.object_id=m.object_id AND j.derivation=?
  LEFT JOIN video_cards c ON c.object_id=m.object_id
  LEFT JOIN video_card_quality q ON q.object_id=c.object_id AND q.card=c.card
  LEFT JOIN video_card_descriptions d ON d.object_id=c.object_id AND d.card=c.card
  WHERE m.object_id IN (`+in+`) GROUP BY m.object_id`, append([]any{describeDerivation}, ids...), func(rows *sql.Rows) error {
		var object string
		var p progress
		if err := rows.Scan(&object, &p.job, &p.input, &p.eligible, &p.described, &p.first, &p.last); err != nil {
			return err
		}
		byObject[object] = p
		return nil
	})
	if err != nil {
		return err
	}
	for i := range assets {
		a := &assets[i]
		p, ok := byObject[a.ObjectID]
		if !ok || a.Quality != AnalysisDone {
			continue
		}
		identity := describeIdentity(reader, p.input)
		switch {
		case p.eligible == 0:
			a.Descriptions, a.descriptionIdentity = AnalysisDone, identity
		case unavailable:
			a.Descriptions = AnalysisUnavailable
		case p.job == "failed":
			a.Descriptions = AnalysisFailed
		case p.job == "running":
			a.Descriptions = AnalysisWorking
		case p.job != "":
			a.Descriptions = AnalysisQueued
		case p.described == p.eligible && p.first == p.last && (p.first == identity || reader == "" && strings.HasSuffix(p.first, identity)):
			a.Descriptions, a.descriptionIdentity = AnalysisDone, p.first
		}
	}
	return nil
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

// cardsToDescribe are the cards of object whose picture passed the quality
// check, in card order, and how many of them have a description of
// reader's identity.
func (s *Service) cardsToDescribe(ctx context.Context, object, reader string) (cards []describeCard, done map[int]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes, `+descriptionInputs+`, q.result, IFNULL(d.identity, '')
		FROM video_cards c JOIN video_card_quality q ON q.object_id = c.object_id AND q.card = c.card
		LEFT JOIN video_card_descriptions d ON d.object_id = c.object_id AND d.card = c.card
		WHERE c.object_id = ? ORDER BY c.card`, object)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	done = map[int]bool{}
	for rows.Next() {
		var card describeCard
		var keyframes, quality, described string
		var seconds []float64
		if err := rows.Scan(&card.card, &card.startMs, &card.endMs, &keyframes, &card.identity, &quality, &described); err != nil {
			return nil, nil, err
		}
		if !passedQuality(quality) {
			continue
		}
		if err := json.Unmarshal([]byte(keyframes), &seconds); err != nil {
			return nil, nil, fmt.Errorf("card %d of %s: key frames: %w", card.card, object, err)
		}
		for _, at := range seconds {
			card.keyframes = append(card.keyframes, int(math.Round(at*1000)))
		}
		cards = append(cards, card)
		if described == describeIdentity(reader, card.identity) {
			done[card.card] = true
		}
	}
	return cards, done, rows.Err()
}

// passedQuality reports whether a quality result names no reason the card
// cannot be used.
func passedQuality(result string) bool {
	var quality struct {
		Reasons *[]string `json:"reasons"`
	}
	return json.Unmarshal([]byte(result), &quality) == nil && quality.Reasons != nil && len(*quality.Reasons) == 0
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

// montageDescribe describes one card of a due description job. It reports
// whether it claimed one, which ends the round, and whether it did any
// work. A card that cannot be described, past the context or with an
// answer that never fits, is stored as failed and the video goes on. A job
// with cards left goes back to the head of the queue without spending its
// attempt.
func (r *aiRunner) montageDescribe(ctx context.Context, embedder Embedder, gate Gate, info EmbedderInfo) (claimed, processed bool, err error) {
	lib := r.lib
	describer, ok := embedder.(CardDescriber)
	taker, taking := lib.media.(FrameTaker)
	if !ok || !taking || info.OCR == "" {
		return false, false, nil
	}
	job, err := lib.claimJob(ctx, describeDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !job.video || job.duration <= 0 {
		return true, true, lib.failJob(ctx, job, jobErrorUndecodable)
	}
	cards, done, err := lib.cardsToDescribe(ctx, job.objectID, info.OCR)
	if err != nil {
		return true, false, errors.Join(err, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	var card *describeCard
	left := 0
	for i := range cards {
		if !done[cards[i].card] {
			if card == nil {
				card = &cards[i]
			}
			left++
		}
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
		return true, false, errors.Join(err, lib.releaseJob(ctx, job))
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
		return true, true, lib.finishDescription(ctx, job, *card, info.OCR, nil, class, 0, left == 1)
	}

	// The description: any reason to stop hangs it up, as a reading does.
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
	return true, true, lib.finishDescription(ctx, job, *card, info.OCR, description, failure, took, left == 1)
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
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ? AND state = 'running')", job.objectID, job.derivation).Scan(&running); err != nil || !running {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO video_card_descriptions(object_id, card, result, error, identity, millis)
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
  JOIN video_card_quality q ON q.object_id=c.object_id AND q.card=c.card
  LEFT JOIN video_card_descriptions d ON d.object_id=c.object_id AND d.card=c.card
  WHERE c.object_id IN (`+scope+`)
  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.object_id=c.object_id AND j.derivation IN (?, ?, ?))
  AND `+eligibleQuality+` AND (d.identity IS NULL OR d.identity <> ? || `+descriptionInputs+`)
  ON CONFLICT(object_id,derivation) DO UPDATE SET state='pending', attempts=0, lease_until=NULL,
   not_before=excluded.not_before, error_class='' WHERE jobs.state='failed' AND ?`, args...)
	return err
}
