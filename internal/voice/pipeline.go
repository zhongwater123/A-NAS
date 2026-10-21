package voice

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// maxOpusBytes bounds one recognition request's audio; the service takes 100 MB.
const maxOpusBytes = 90 << 20

// busyRetries are the waits before trying a busy recognition service again
// within one attempt; after them the recording waits for the next poll.
var busyRetries = []time.Duration{15 * time.Second, time.Minute}

// runAudio processes queued recordings one at a time: recognition is paid
// per request and one recording at a time keeps the device responsive.
func (s *Service) runAudio() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-s.base.Done():
			return
		case <-s.audio:
		case <-timer.C:
		}
		// Waiting recordings are looked at again: a member may have signed
		// in or an administrator configured the recognition service.
		_, _ = s.store.db.ExecContext(s.base, "UPDATE recordings SET status = ?, waiting = '' WHERE status = ?", StatusQueued, StatusWaiting)
		rows, err := s.store.db.QueryContext(s.base, "SELECT id FROM recordings WHERE status = ? ORDER BY created_at", StatusQueued)
		if err != nil {
			if s.base.Err() == nil {
				s.logger.Error("list queued recordings", "error", err)
			}
		} else {
			var queued []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					queued = append(queued, id)
				}
			}
			_ = rows.Close()
			for _, id := range queued {
				if s.base.Err() != nil {
					return
				}
				s.process(id)
			}
		}
		timer.Reset(s.poll)
	}
}

// job is the recording being processed.
type job struct {
	recording Recording
	owner     string
	attempts  int
}

func (s *Service) process(id string) {
	var current job
	row := s.store.db.QueryRowContext(s.base, "SELECT "+recordingColumns+", owner_id, attempts FROM recordings WHERE id = ? AND status = ?", id, StatusQueued)
	var err error
	current.recording, err = scanRecording(rowWithExtras{row: row, extras: []any{&current.owner, &current.attempts}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.base)
	s.mu.Lock()
	s.running[id] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
	}()
	s.stage(id, StagePreparing, 0)
	err = s.transcribe(ctx, current)
	if err == nil || ctx.Err() != nil && s.base.Err() == nil {
		// Done, or deleted while it ran.
		return
	}
	if s.base.Err() != nil {
		// Shutting down: Run queues it again next time.
		return
	}
	var waiting waitError
	switch {
	case errors.As(err, &waiting):
		s.wait(id, waiting.reason)
	case errors.Is(err, ErrRecognizerRejected):
		s.fail(id, FailRejected, err)
	case errors.Is(err, ErrNotAudio):
		s.fail(id, FailNotAudio, err)
	case errors.Is(err, ErrTooLong):
		s.fail(id, FailTooLong, err)
	case errors.Is(err, files.ErrNotFound), errors.Is(err, files.ErrForbidden):
		s.fail(id, FailMissing, err)
	case errors.Is(err, mediaworker.ErrUnavailable):
		s.retryOrFail(current, FailDecoder, err)
	default:
		s.retryOrFail(current, FailInternal, err)
	}
}

// rowWithExtras scans columns after a recording's.
type rowWithExtras struct {
	row    scanner
	extras []any
}

func (r rowWithExtras) Scan(dest ...any) error { return r.row.Scan(append(dest, r.extras...)...) }

// waitError stops processing until something outside the recording changes.
type waitError struct{ reason string }

func (e waitError) Error() string { return "waiting: " + e.reason }

func (s *Service) stage(id, stage string, progress float64) {
	_, _ = s.store.db.ExecContext(s.base, "UPDATE recordings SET status = ?, stage = ?, progress = ?, updated_at = ? WHERE id = ?",
		StatusProcessing, stage, progress, formatTime(s.now()), id)
}

func (s *Service) wait(id, reason string) {
	_, _ = s.store.db.ExecContext(s.base, "UPDATE recordings SET status = ?, stage = '', waiting = ?, progress = 0, updated_at = ? WHERE id = ?",
		StatusWaiting, reason, formatTime(s.now()), id)
}

func (s *Service) fail(id, code string, err error) {
	s.logger.Warn("voice recording failed", "recording", id, "code", code, "error", err)
	_, _ = s.store.db.ExecContext(s.base, `
UPDATE recordings SET status = ?, stage = '', waiting = '', progress = 0, error_code = ?, error_message = ?, updated_at = ? WHERE id = ?`,
		StatusFailed, code, failureMessage(err), formatTime(s.now()), id)
}

// retryOrFail tries a recording that met an unexpected error three times.
func (s *Service) retryOrFail(current job, code string, err error) {
	if current.attempts+1 >= 3 {
		s.fail(current.recording.ID, code, err)
		return
	}
	s.logger.Warn("voice recording will be tried again", "recording", current.recording.ID, "error", err)
	_, _ = s.store.db.ExecContext(s.base, "UPDATE recordings SET status = ?, stage = '', waiting = ?, attempts = attempts + 1, progress = 0 WHERE id = ?",
		StatusWaiting, WaitBusy, current.recording.ID)
}

func failureMessage(err error) string {
	return truncateRunes(err.Error(), 300)
}

// sessionOf is a session of the member background work may act through.
func (s *Service) sessionOf(ctx context.Context, ownerID string) (accounts.Session, bool) {
	if s.sessions == nil {
		return accounts.Session{}, false
	}
	for _, session := range s.sessions(ctx) {
		if session.User.ID == ownerID && session.Token != "" && session.User.Status == accounts.UserStatusActive {
			return session, true
		}
	}
	return accounts.Session{}, false
}

// transcribe recognises the recording piece by piece, finds its voices and
// saves the transcript.
func (s *Service) transcribe(ctx context.Context, current job) error {
	recording := current.recording
	if s.media == nil {
		return fmt.Errorf("%w: no media worker", mediaworker.ErrUnavailable)
	}
	if s.recognizer == nil || !s.recognizer.Ready(ctx) {
		return waitError{WaitRecognizer}
	}
	session, ok := s.sessionOf(ctx, current.owner)
	if !ok {
		return waitError{WaitSignIn}
	}
	userCtx := accounts.WithSessionToken(ctx, session.Token)
	content, err := s.files.OpenContent(userCtx, session.User, recording.FileID)
	if err != nil {
		return err
	}
	defer content.Reader.Close()
	probe, err := s.media.Probe(ctx, content.Reader)
	if errors.Is(err, mediaworker.ErrUnreadable) {
		return ErrNotAudio
	}
	if err != nil {
		return err
	}
	if _, ok := firstAudio(probe); !ok {
		return ErrNotAudio
	}
	duration := time.Duration(probe.Format.Duration * float64(time.Second))
	if duration > MaxDuration {
		return ErrTooLong
	}
	if duration <= 0 {
		return ErrNotAudio
	}
	pieces := int((duration + s.chunk - 1) / s.chunk)
	hotwords := append(append([]string(nil), recording.Hotwords...), s.peopleNames(ctx, current.owner)...)
	var heard []piece
	for index := range pieces {
		start := time.Duration(index) * s.chunk
		length := min(s.chunk, duration-start)
		s.stage(recording.ID, StageRecognizing, 0.8*float64(index)/float64(pieces))
		recognition, err := s.recognizePiece(ctx, content.Reader, start, length, RecognizeRequest{
			Language: recording.Language, Hotwords: hotwords, Duration: length,
		})
		if err != nil {
			return err
		}
		heard = append(heard, piece{start: start, length: length, utterances: recognition.Utterances})
	}
	s.stage(recording.ID, StageIdentifying, 0.85)
	utterances, speakers, model, err := s.identify(ctx, content.Reader, heard)
	if err != nil {
		return err
	}
	return s.saveTranscript(ctx, current, duration, utterances, speakers, model)
}

func (s *Service) recognizePiece(ctx context.Context, file *os.File, start, length time.Duration, request RecognizeRequest) (Recognition, error) {
	stream, err := s.media.Speech(ctx, file, mediaworker.SpeechRequest{Format: mediaworker.SpeechOpus, Start: start.Seconds(), Duration: length.Seconds()})
	if err != nil {
		return Recognition{}, err
	}
	audio, err := io.ReadAll(io.LimitReader(stream, maxOpusBytes+1))
	_ = stream.Close()
	if err != nil {
		return Recognition{}, err
	}
	if len(audio) > maxOpusBytes {
		return Recognition{}, fmt.Errorf("%w: a piece is larger than the service takes", ErrTooLong)
	}
	for attempt := 0; ; attempt++ {
		recognition, err := s.recognizer.Recognize(ctx, audio, request)
		if errors.Is(err, ErrRecognizerUnconfigured) {
			return Recognition{}, waitError{WaitRecognizer}
		}
		if !errors.Is(err, ErrRecognizerBusy) {
			return recognition, err
		}
		if attempt >= len(busyRetries) {
			s.logger.Warn("recognition service busy", "error", err)
			return Recognition{}, waitError{WaitBusy}
		}
		select {
		case <-ctx.Done():
			return Recognition{}, ctx.Err()
		case <-time.After(busyRetries[attempt]):
		}
	}
}

// piece is one recognition request's part of the recording.
type piece struct {
	start, length time.Duration
	utterances    []Recognized
}

// voice is one voice of one piece before the pieces are joined.
type voice struct {
	piece     int
	label     string
	profile   []float32
	profileMs int64
	first     int64
}

// identify gives every sentence its speaker: within a piece the recognition
// service's labels, or the voice Worker's clusters when it gave none; across
// pieces voices whose profiles agree are one. Without the voice Worker each
// piece's labels stay apart and nobody is recognised.
func (s *Service) identify(ctx context.Context, file *os.File, pieces []piece) ([]Utterance, []profiled, string, error) {
	var voices []voice
	labels := make([][]string, len(pieces))
	model := ""
	for index, piece := range pieces {
		labels[index] = make([]string, len(piece.utterances))
		labeled := false
		for i, utterance := range piece.utterances {
			labels[index][i] = utterance.Speaker
			labeled = labeled || utterance.Speaker != ""
		}
		if len(piece.utterances) == 0 {
			continue
		}
		found, pieceModel, err := s.pieceVoices(ctx, file, piece, labels[index], labeled)
		if err != nil {
			return nil, nil, "", err
		}
		if pieceModel != "" {
			model = pieceModel
		}
		for i := range found {
			found[i].piece = index
		}
		voices = append(voices, found...)
	}
	// Join the pieces' voices, in the order they first speak.
	sort.SliceStable(voices, func(i, j int) bool {
		if voices[i].piece != voices[j].piece {
			return voices[i].piece < voices[j].piece
		}
		return voices[i].first < voices[j].first
	})
	type joined struct {
		profile   []float32
		profileMs int64
		pieces    map[int]bool
	}
	var speakers []*joined
	keyOf := map[[2]string]int{}
	for _, v := range voices {
		target := -1
		best := SameVoiceThreshold
		if len(v.profile) > 0 {
			for i, speaker := range speakers {
				if speaker.pieces[v.piece] || len(speaker.profile) == 0 {
					continue
				}
				if score := cosine(v.profile, speaker.profile); score >= best {
					target, best = i, score
				}
			}
		}
		if target < 0 {
			speakers = append(speakers, &joined{profile: v.profile, profileMs: v.profileMs, pieces: map[int]bool{}})
			target = len(speakers) - 1
		} else {
			speaker := speakers[target]
			speaker.profile = blend(speaker.profile, speaker.profileMs, v.profile, v.profileMs)
			speaker.profileMs += v.profileMs
		}
		speakers[target].pieces[v.piece] = true
		keyOf[[2]string{strconv.Itoa(v.piece), v.label}] = target
	}
	var utterances []Utterance
	speech := make([]int64, len(speakers))
	order := map[int]int{}
	for index, piece := range pieces {
		offset := piece.start.Milliseconds()
		for i, recognized := range piece.utterances {
			target, ok := keyOf[[2]string{strconv.Itoa(index), labels[index][i]}]
			if !ok {
				continue
			}
			if _, seen := order[target]; !seen {
				order[target] = len(order) + 1
			}
			speech[target] += recognized.EndMs - recognized.StartMs
			utterances = append(utterances, Utterance{
				Seq: len(utterances) + 1, StartMs: offset + recognized.StartMs, EndMs: offset + recognized.EndMs,
				Speaker: "s" + strconv.Itoa(order[target]), Text: recognized.Text,
			})
		}
	}
	result := make([]profiled, 0, len(order))
	for target, number := range order {
		result = append(result, profiled{
			key: "s" + strconv.Itoa(number), number: number, profile: speakers[target].profile,
			profileMs: speakers[target].profileMs, speechMs: speech[target],
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].number < result[j].number })
	if model == "" {
		for i := range result {
			result[i].profile = nil
		}
	}
	return utterances, result, model, nil
}

// pieceVoices finds the voices of one piece and their profiles. When the
// recognition service gave no labels, it writes the voice Worker's clusters
// into labels.
func (s *Service) pieceVoices(ctx context.Context, file *os.File, piece piece, labels []string, labeled bool) ([]voice, string, error) {
	firsts := map[string]int64{}
	indexes := map[string][]int{}
	for i, label := range labels {
		if _, ok := firsts[label]; !ok {
			firsts[label] = piece.utterances[i].StartMs
		}
		indexes[label] = append(indexes[label], i)
	}
	plain := func() []voice {
		var voices []voice
		for label, first := range firsts {
			voices = append(voices, voice{label: label, first: first})
		}
		return voices
	}
	if s.voiceprints == nil {
		if !labeled {
			for i := range labels {
				labels[i] = "1"
			}
			return []voice{{label: "1", first: piece.utterances[0].StartMs}}, "", nil
		}
		return plain(), "", nil
	}
	if labeled {
		names := make([]string, 0, len(indexes))
		for label := range indexes {
			names = append(names, label)
		}
		sort.Strings(names)
		var chosen []stretch
		var groupLabels []string
		// One request carries at most ClusterBudget of speech, shared by the voices.
		budget := min(int64(ProfileBudget), int64(ClusterBudget)/int64(len(names)))
		for _, label := range names {
			stretches := profileStretches(piece.utterances, indexes[label], label, budget)
			if len(stretches) == 0 {
				continue
			}
			chosen = append(chosen, stretches...)
			groupLabels = append(groupLabels, label)
		}
		if len(groupLabels) > 64 {
			return plain(), "", nil
		}
		voices := plain()
		if len(chosen) == 0 {
			return voices, "", nil
		}
		samples, spans, err := s.readStretches(ctx, file, piece, chosen)
		if err != nil {
			return nil, "", err
		}
		groups := make([]Group, len(groupLabels))
		position := map[string]int{}
		for i, label := range groupLabels {
			groups[i].Label = label
			position[label] = i
		}
		for i, span := range spans {
			if span.Count < MinStretchMs*16 {
				continue
			}
			group := &groups[position[chosen[i].label]]
			group.Spans = append(group.Spans, span)
		}
		var kept []Group
		for _, group := range groups {
			if len(group.Spans) > 0 {
				kept = append(kept, group)
			}
		}
		if len(kept) == 0 {
			return voices, "", nil
		}
		model, vectors, err := s.voiceprints.Embed(ctx, samples, kept)
		if err != nil {
			return s.withoutVoiceprints(err, voices)
		}
		s.rememberModel(model)
		for i, group := range kept {
			for j := range voices {
				if voices[j].label == group.Label {
					voices[j].profile = vectors[i]
					for _, span := range group.Spans {
						voices[j].profileMs += int64(span.Count / 16)
					}
				}
			}
		}
		return voices, model, nil
	}
	// No labels: cluster the sentences long enough to embed, and give each
	// shorter one the voice of the nearest clustered sentence.
	chosen := clusterStretches(piece.utterances)
	if len(chosen) == 0 {
		for i := range labels {
			labels[i] = "1"
		}
		return []voice{{label: "1", first: piece.utterances[0].StartMs}}, "", nil
	}
	samples, read, err := s.readStretches(ctx, file, piece, chosen)
	if err != nil {
		return nil, "", err
	}
	// The decoded audio may end before the recognition service said.
	var spans []Span
	kept := chosen[:0:0]
	for i, span := range read {
		if span.Count >= MinStretchMs*16 {
			spans = append(spans, span)
			kept = append(kept, chosen[i])
		}
	}
	chosen = kept
	if len(spans) == 0 {
		for i := range labels {
			labels[i] = "1"
		}
		return []voice{{label: "1", first: piece.utterances[0].StartMs}}, "", nil
	}
	model, clusters, centroids, err := s.voiceprints.Cluster(ctx, samples, spans, ClusterDistance)
	if err != nil {
		for i := range labels {
			labels[i] = "1"
		}
		return s.withoutVoiceprints(err, []voice{{label: "1", first: piece.utterances[0].StartMs}})
	}
	s.rememberModel(model)
	clustered := map[int]int{}
	for i, stretch := range chosen {
		clustered[stretch.utterance] = clusters[i]
	}
	for i := range labels {
		cluster, ok := clustered[i]
		if !ok {
			cluster = nearestCluster(piece.utterances, clustered, i)
		}
		labels[i] = strconv.Itoa(cluster + 1)
	}
	var voices []voice
	seen := map[string]bool{}
	for i, label := range labels {
		if seen[label] {
			continue
		}
		seen[label] = true
		cluster, _ := strconv.Atoi(label)
		var ms int64
		for j, stretch := range chosen {
			if clusters[j] == cluster-1 {
				ms += stretch.endMs - stretch.startMs
			}
		}
		voices = append(voices, voice{label: label, first: piece.utterances[i].StartMs, profile: centroids[cluster-1], profileMs: ms})
	}
	return voices, model, nil
}

// withoutVoiceprints carries on without profiles when the voice Worker
// cannot be reached; a rejected input is a bug worth failing on.
func (s *Service) withoutVoiceprints(err error, voices []voice) ([]voice, string, error) {
	if errors.Is(err, context.Canceled) {
		return nil, "", err
	}
	s.logger.Warn("voiceprints unavailable; speakers are not recognised", "error", err)
	for i := range voices {
		voices[i].profile = nil
	}
	return voices, "", nil
}

func (s *Service) rememberModel(model string) {
	s.mu.Lock()
	s.voiceModel = model
	s.mu.Unlock()
}

// nearestCluster is the cluster of the clustered sentence closest in time.
func nearestCluster(utterances []Recognized, clustered map[int]int, index int) int {
	best, distance := 0, int64(-1)
	for other, cluster := range clustered {
		gap := utterances[index].StartMs - utterances[other].EndMs
		if gap < 0 {
			gap = utterances[other].StartMs - utterances[index].EndMs
		}
		if gap < 0 {
			gap = 0
		}
		if distance < 0 || gap < distance || gap == distance && other < index {
			best, distance = cluster, gap
		}
	}
	return best
}

// readStretches decodes the piece to 16 kHz samples and keeps only the
// chosen stretches, packed one after another; spans locate them.
func (s *Service) readStretches(ctx context.Context, file *os.File, piece piece, chosen []stretch) ([]int16, []Span, error) {
	order := make([]int, len(chosen))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return chosen[order[i]].startMs < chosen[order[j]].startMs })
	stream, err := s.media.Speech(ctx, file, mediaworker.SpeechRequest{Format: mediaworker.SpeechPCM, Start: piece.start.Seconds(), Duration: piece.length.Seconds()})
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()
	reader := bufio.NewReaderSize(stream, 1<<20)
	var samples []int16
	spans := make([]Span, len(chosen))
	position := 0
	var buffer []byte
	for _, index := range order {
		chosenStretch := chosen[index]
		begin := max(int(chosenStretch.startMs*16), position)
		end := max(int(chosenStretch.endMs*16), begin)
		spans[index] = Span{Offset: len(samples), Label: chosenStretch.label}
		if _, err := reader.Discard(2 * (begin - position)); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, err
		}
		position = begin
		if cap(buffer) < 2*(end-begin) {
			buffer = make([]byte, 2*(end-begin))
		}
		read, err := io.ReadFull(reader, buffer[:2*(end-begin)])
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, err
		}
		for i := 0; i+1 < read; i += 2 {
			samples = append(samples, int16(binary.LittleEndian.Uint16(buffer[i:])))
		}
		position += read / 2
		spans[index].Count = len(samples) - spans[index].Offset
		if err != nil {
			break
		}
	}
	return samples, spans, nil
}

// saveTranscript replaces the recording's transcript and queues the
// translation and summary its member asked for.
func (s *Service) saveTranscript(ctx context.Context, current job, duration time.Duration, utterances []Utterance, speakers []profiled, model string) error {
	recording := current.recording
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings WHERE id = ?", recording.ID).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	for _, statement := range []string{"DELETE FROM utterances WHERE recording_id = ?", "DELETE FROM speakers WHERE recording_id = ?"} {
		if _, err := tx.ExecContext(ctx, statement, recording.ID); err != nil {
			return err
		}
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO utterances (recording_id, seq, start_ms, end_ms, speaker, text) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, utterance := range utterances {
		if _, err := insert.ExecContext(ctx, recording.ID, utterance.Seq, utterance.StartMs, utterance.EndMs, utterance.Speaker, utterance.Text); err != nil {
			return err
		}
	}
	for _, speaker := range speakers {
		var profile any
		if len(speaker.profile) > 0 {
			profile = encodeVector(speaker.profile)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO speakers (recording_id, key, number, speech_ms, profile, profile_ms) VALUES (?, ?, ?, ?, ?, ?)`,
			recording.ID, speaker.key, speaker.number, speaker.speechMs, profile, speaker.profileMs); err != nil {
			return err
		}
	}
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `
UPDATE recordings SET status = ?, stage = '', waiting = '', progress = 1, error_code = '', error_message = '', attempts = 0,
    duration_ms = ?, voiceprint_model = ?, text_revision = text_revision + 1, names_revision = names_revision + 1,
    processed_at = ?, updated_at = ?
WHERE id = ?`, StatusReady, duration.Milliseconds(), model, now, now, recording.ID); err != nil {
		return err
	}
	if len(utterances) > 0 {
		if recording.Template != "" {
			if err := queueSummary(ctx, tx, recording.ID, recording.Template, now); err != nil {
				return err
			}
		}
		if recording.TranslateTo != "" {
			if err := queueTranslation(ctx, tx, recording.ID, recording.TranslateTo, now); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.rematch(ctx, current.owner); err != nil {
		return err
	}
	kick(s.text)
	return nil
}

func queueSummary(ctx context.Context, tx *sql.Tx, recordingID, template, now string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO summaries (recording_id, template, status, requested_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (recording_id, template) DO UPDATE SET status = excluded.status, error_code = '', error_message = '', updated_at = excluded.updated_at`,
		recordingID, template, JobQueued, now, now)
	return err
}

func queueTranslation(ctx context.Context, tx *sql.Tx, recordingID, language, now string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO translations (recording_id, language, status, requested_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (recording_id, language) DO UPDATE SET status = excluded.status, progress = 0, error_code = '', error_message = '', updated_at = excluded.updated_at`,
		recordingID, language, JobQueued, now, now)
	return err
}
