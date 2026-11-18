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
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos/textread"
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
	keyframeWidth          = 512
	keyframeQuality        = 3
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

// queueDescribe queues the descriptions of object, in the transaction that
// stored its last quality result, unless they are made already.
func (s *Service) queueDescribe(ctx context.Context, tx *sql.Tx, object string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
		SELECT ?, ?, 'pending', ?, ? WHERE NOT EXISTS (SELECT 1 FROM video_card_descriptions WHERE object_id = ?)
		ON CONFLICT DO NOTHING`, object, describeDerivation, now, now, object)
	return err
}

// describeState says where the descriptions of object stand. A queued one
// whose Worker has no text reader is AnalysisUnavailable.
func (s *Service) describeState(ctx context.Context, q queryer, object string) (AnalysisState, error) {
	var state sql.NullString
	err := q.QueryRowContext(ctx, "SELECT state FROM jobs WHERE object_id = ? AND derivation = ?", object, describeDerivation).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var done bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM video_card_descriptions WHERE object_id = ?)", object).Scan(&done); err != nil || !done {
			return AnalysisNone, err
		}
		return AnalysisDone, nil
	case err != nil:
		return AnalysisNone, err
	case state.String == "failed":
		return AnalysisFailed, nil
	case s.ai.withoutReader():
		return AnalysisUnavailable, nil
	case state.String == "running":
		return AnalysisWorking, nil
	}
	return AnalysisQueued, nil
}

// cardDescriptions are the stored descriptions of object, in card order.
func (s *Service) cardDescriptions(ctx context.Context, object string) ([]CardDescription, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT card, IFNULL(result, ''), error FROM video_card_descriptions WHERE object_id = ? ORDER BY card", object)
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
// milliseconds from the video's start.
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
	rows, err := s.db.QueryContext(ctx, `SELECT c.card, c.start_ms, c.end_ms, c.keyframes, c.identity, q.result, IFNULL(d.identity, '')
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
		if err := rows.Scan(&card.card, &card.startMs, &card.endMs, &keyframes, &card.identity, &quality, &described); err != nil {
			return nil, nil, err
		}
		if !passedQuality(quality) {
			continue
		}
		if err := json.Unmarshal([]byte(keyframes), &card.keyframes); err != nil {
			return nil, nil, fmt.Errorf("card %d of %s: key frames: %w", card.card, object, err)
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

// withoutReader reports whether the Worker answered and has no text reader.
func (r *aiRunner) withoutReader() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.model != "" && r.reader == ""
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
		return true, true, lib.completeDescriptions(ctx, job, cards)
	}
	detector, _ := embedder.(Detector)
	r.switchPhase(ctx, detector, true)
	if open, reason := gate.Open(ctx, info.OCRMemoryBytes); !open {
		r.progress.set(AIPaused, reason, "")
		r.releaseModels(ctx, detector, reason)
		return true, false, lib.returnJob(ctx, job)
	}
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
	var hungUp atomic.Value
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		ticker := time.NewTicker(gateWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-working.Done():
				return
			case <-ticker.C:
				if open, reason := gate.Open(working, info.OCRMemoryBytes); !open && closesForeground(reason) {
					hungUp.Store(reason)
					cancel()
					return
				}
			}
		}
	}()
	durationMs := int(math.Round(job.duration * 1000))
	instants := make([]int, len(card.keyframes))
	times := make([]float64, len(card.keyframes))
	for i, at := range card.keyframes {
		// A frame within the last 100 ms may not be there.
		instants[i] = min(at, max(durationMs-100, 0))
		times[i] = float64(instants[i]) / 1000
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
	cancel()
	<-watched
	if reason, _ := hungUp.Load().(string); reason != "" || ctx.Err() != nil {
		return true, false, r.pauseDescribe(ctx, job, detector, reason)
	}
	if err != nil {
		if class := decodeFailure(err); class != jobWaiting {
			return true, true, lib.failJob(ctx, job, class)
		}
		return true, false, lib.releaseJob(ctx, job)
	}
	if len(frames) != len(times) {
		return true, true, lib.failJob(ctx, job, jobErrorIO)
	}

	// The description: any reason to stop hangs it up, as a reading does.
	describing, stop := context.WithCancel(ctx)
	defer stop()
	stopWatching := watchGate(describing, gate, info.OCRMemoryBytes, stop)
	r.markReaderLoaded()
	started := lib.now()
	model, template, description, err := describer.DescribeFrames(describing, frames, cardLayout(*card, instants))
	paused := stopWatching()
	stop()
	took := lib.now().Sub(started)
	switch {
	case ctx.Err() != nil, paused != "":
		return true, false, r.pauseDescribe(ctx, job, detector, paused)
	case errors.Is(err, ErrAIUnavailable) || (err == nil && (model != info.OCR || template != describeTemplate)):
		// Look again next round: the Worker may have lost or changed its reader.
		r.forgetModels()
		return true, false, lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected) || errors.Is(err, ErrAIInvalidResult):
		// This card alone goes undescribed.
		description, err = nil, lib.storeDescription(ctx, job.objectID, *card, info.OCR, nil, err.Error(), took)
	case err != nil:
		// The Worker failed after taking the frames: an attempt is spent.
		return true, true, lib.failJob(ctx, job, jobErrorIO)
	default:
		if description, err = maskDescription(description); err != nil {
			return true, true, lib.failJob(ctx, job, jobErrorInvalidResult)
		}
		err = lib.storeDescription(ctx, job.objectID, *card, info.OCR, description, "", took)
	}
	if err != nil {
		return true, false, errors.Join(err, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	if left == 1 {
		return true, true, lib.completeDescriptions(ctx, job, cards)
	}
	return true, true, lib.returnJob(ctx, job)
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

// storeDescription stores one card's description, masked already, or why
// it has none, replacing one of another identity.
func (s *Service) storeDescription(ctx context.Context, object string, card describeCard, reader string, description json.RawMessage, failure string, took time.Duration) error {
	var result any
	if description != nil {
		result = string(description)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO video_card_descriptions(object_id, card, result, error, identity, millis)
		VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(object_id, card) DO UPDATE SET result = excluded.result, error = excluded.error,
		identity = excluded.identity, millis = excluded.millis`,
		object, card.card, result, strings.TrimSpace(failure), describeIdentity(reader, card.identity), took.Milliseconds())
	return err
}

// completeDescriptions retires the job once every card that passed the
// quality check has its description, and drops those of cards that are no
// longer, or no longer pass.
func (s *Service) completeDescriptions(ctx context.Context, job claimedJob, cards []describeCard) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		keep := make([]any, 0, len(cards)+1)
		keep = append(keep, job.objectID)
		marks := make([]string, len(cards))
		for i, card := range cards {
			keep = append(keep, card.card)
			marks[i] = "?"
		}
		statement := "DELETE FROM video_card_descriptions WHERE object_id = ?"
		if len(cards) > 0 {
			statement += " AND card NOT IN (" + strings.Join(marks, ", ") + ")"
		}
		if _, err := tx.ExecContext(ctx, statement, keep...); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
}

// requeueStaleDescriptions queues again the descriptions of the objects a
// grant still refers to that another reader wrote.
func (s *Service) requeueStaleDescriptions(ctx context.Context, reader string) error {
	now := formatTime(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
		SELECT DISTINCT d.object_id, ?, 'pending', ?, ? FROM video_card_descriptions d
		WHERE instr(d.identity, ?) <> 1 AND d.object_id IN (`+referencedObjects+`)
		ON CONFLICT DO NOTHING`, describeDerivation, now, now, reader+describeIdentitySuffix+"|")
	return err
}
