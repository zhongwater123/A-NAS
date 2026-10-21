package voice

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// Options configure the service. Every component may be missing; Status
// says which are, and recordings wait or fail with the reason.
type Options struct {
	Media       MediaWorker
	Recognizer  Recognizer
	TextModel   TextModel
	Voiceprints Voiceprints
	// Sessions are the sessions background work may read files through
	// (accounts.Service.RememberedSessions).
	Sessions func(context.Context) []accounts.Session
	Now      func() time.Time
	Random   io.Reader
	Logger   *slog.Logger
	// Chunk is how much audio one recognition request carries; it defaults
	// to an hour, about 11 MB of Opus.
	Chunk time.Duration
	// Poll is how often waiting recordings are looked at again.
	Poll time.Duration
}

// Service is 语音处理.
type Service struct {
	store       *Store
	files       Files
	media       MediaWorker
	recognizer  Recognizer
	textModel   TextModel
	voiceprints Voiceprints
	sessions    func(context.Context) []accounts.Session
	now         func() time.Time
	random      io.Reader
	logger      *slog.Logger
	chunk       time.Duration
	poll        time.Duration

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	audio  chan struct{}
	text   chan struct{}
	mu     sync.Mutex
	// running cancels the work on a recording that is deleted.
	running map[string]context.CancelFunc
	// voiceModel is the voice Worker's model, once it answered.
	voiceModel string
}

// New opens 语音处理 on store. Run starts the background work.
func New(store *Store, fileService Files, options Options) *Service {
	base, cancel := context.WithCancel(context.Background())
	service := &Service{
		store: store, files: fileService, media: options.Media, recognizer: options.Recognizer,
		textModel: options.TextModel, voiceprints: options.Voiceprints, sessions: options.Sessions,
		now: options.Now, random: options.Random, logger: options.Logger, chunk: options.Chunk, poll: options.Poll,
		base: base, cancel: cancel, audio: make(chan struct{}, 1), text: make(chan struct{}, 1),
		running: map[string]context.CancelFunc{},
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.random == nil {
		service.random = rand.Reader
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.chunk <= 0 {
		service.chunk = time.Hour
	}
	if service.poll <= 0 {
		service.poll = 30 * time.Second
	}
	// The model the newest voiceprints came from, until the Worker answers
	// again; asking it here would start it for nothing.
	_ = store.db.QueryRow("SELECT voiceprint_model FROM recordings WHERE voiceprint_model != '' ORDER BY processed_at DESC LIMIT 1").Scan(&service.voiceModel)
	return service
}

// Run processes recordings, translations and summaries until Close. Work
// interrupted by a restart starts again.
func (s *Service) Run() {
	ctx := context.Background()
	_, _ = s.store.db.ExecContext(ctx, "UPDATE recordings SET status = ?, stage = '', progress = 0 WHERE status = ?", StatusQueued, StatusProcessing)
	_, _ = s.store.db.ExecContext(ctx, "UPDATE translations SET status = ? WHERE status = ?", JobQueued, JobRunning)
	_, _ = s.store.db.ExecContext(ctx, "UPDATE summaries SET status = ? WHERE status = ?", JobQueued, JobRunning)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.runAudio() }()
	go func() { defer s.wg.Done(); s.runText() }()
}

// Close stops the background work and waits for it.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func kick(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

// Changed tells the service that something it waits for may have arrived,
// such as a member signing in.
func (s *Service) Changed() { kick(s.audio) }

func (s *Service) randomID(prefix string) string {
	value := make([]byte, 12)
	if _, err := io.ReadFull(s.random, value); err != nil {
		panic(err)
	}
	return prefix + ":" + hex.EncodeToString(value)
}

// Status says which parts of 语音处理 this device has.
type Status struct {
	Recognizer      ComponentStatus `json:"recognizer"`
	TextModel       ComponentStatus `json:"textModel"`
	Voiceprints     ComponentStatus `json:"voiceprints"`
	Decoder         ComponentStatus `json:"decoder"`
	Templates       []Template      `json:"templates"`
	Languages       []Language      `json:"languages"`
	SpokenLanguages []Language      `json:"spokenLanguages"`
}

type ComponentStatus struct {
	Available bool   `json:"available"`
	Name      string `json:"name,omitempty"`
}

func (s *Service) Status(ctx context.Context) Status {
	status := Status{Templates: Templates, Languages: Languages, SpokenLanguages: SpokenLanguages}
	if s.recognizer != nil {
		status.Recognizer = ComponentStatus{Available: s.recognizer.Ready(ctx), Name: "豆包语音 · 录音文件识别极速版"}
	}
	if s.textModel != nil {
		status.TextModel = ComponentStatus{Available: true, Name: s.textModel.Name()}
	}
	if s.voiceprints != nil {
		s.mu.Lock()
		model := s.voiceModel
		s.mu.Unlock()
		status.Voiceprints = ComponentStatus{Available: true, Name: model}
	}
	status.Decoder = ComponentStatus{Available: s.media != nil}
	return status
}

// ImportRequest is what a member chooses when importing a file.
type ImportRequest struct {
	FileID      string   `json:"fileId"`
	Language    string   `json:"language"`
	Template    string   `json:"template"`
	TranslateTo string   `json:"translateTo"`
	Hotwords    []string `json:"hotwords"`
}

const (
	maxTitleRunes   = 120
	maxHotwordCount = 100
	maxTextRunes    = 4000
	maxNameRunes    = 40
)

// audioExtensions are what the import dialog offers; any file the media
// Worker finds audio in is accepted.
var audioExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".aac": true, ".wav": true, ".flac": true, ".ogg": true, ".opus": true, ".oga": true,
	".amr": true, ".wma": true, ".webm": true, ".mp4": true, ".mov": true, ".mkv": true, ".3gp": true, ".aiff": true,
	".aif": true, ".caf": true, ".ape": true, ".m4b": true,
}

// browserAudio are the codecs Chromium plays in these containers.
var browserAudio = map[string][]string{
	"mp3": {"mp3"}, "aac": {"aac"}, "mp4": {"aac", "mp3", "flac", "opus", "alac"}, "m4a": {"aac", "alac"},
	"wav": {"pcm_s16le", "pcm_s24le", "pcm_f32le", "pcm_u8"}, "ogg": {"opus", "vorbis", "flac"}, "flac": {"flac"},
	"webm": {"opus", "vorbis"}, "matroska": {"opus", "vorbis", "aac", "mp3", "flac"},
}

// Import registers a recording of the member's file and queues it.
func (s *Service) Import(ctx context.Context, actor accounts.User, request ImportRequest) (Recording, error) {
	if s.media == nil {
		return Recording{}, fmt.Errorf("%w: no media worker", ErrUnavailable)
	}
	if !spokenLanguage(request.Language) {
		return Recording{}, fmt.Errorf("%w: unknown language", ErrInvalid)
	}
	if request.Template != "" {
		if _, ok := templateByID(request.Template); !ok {
			return Recording{}, fmt.Errorf("%w: unknown template", ErrInvalid)
		}
	}
	if request.TranslateTo != "" {
		if _, ok := languageByCode(request.TranslateTo); !ok {
			return Recording{}, fmt.Errorf("%w: unknown translation language", ErrInvalid)
		}
	}
	words, err := cleanHotwords(request.Hotwords)
	if err != nil {
		return Recording{}, err
	}
	entry, _, err := s.files.Lookup(ctx, actor, request.FileID)
	if err != nil {
		return Recording{}, err
	}
	if entry.Kind != files.EntryKindFile {
		return Recording{}, fmt.Errorf("%w: not a file", ErrNotAudio)
	}
	content, err := s.files.OpenContent(ctx, actor, entry.ID)
	if err != nil {
		return Recording{}, err
	}
	defer content.Reader.Close()
	probe, err := s.media.Probe(ctx, content.Reader)
	if errors.Is(err, mediaworker.ErrUnreadable) {
		return Recording{}, ErrNotAudio
	}
	if err != nil {
		return Recording{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	audio, ok := firstAudio(probe)
	if !ok {
		return Recording{}, ErrNotAudio
	}
	duration := time.Duration(probe.Format.Duration * float64(time.Second))
	if duration > MaxDuration {
		return Recording{}, ErrTooLong
	}
	now := s.now().UTC()
	recording := Recording{
		ID: s.randomID("recording"), FileID: entry.ID, SpaceID: entry.SpaceID, Name: entry.Name,
		Title: titleOf(entry.Name), SizeBytes: entry.SizeBytes, DurationMs: duration.Milliseconds(),
		Language: request.Language, Template: request.Template, TranslateTo: request.TranslateTo, Hotwords: words,
		Status: StatusQueued, Transcode: !playable(probe.Format.Name, audio.Codec), People: []string{},
		CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.store.db.ExecContext(ctx, `
INSERT INTO recordings (id, owner_id, file_id, space_id, name, title, size_bytes, duration_ms, language, template,
    translate_to, hotwords, status, transcode, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		recording.ID, actor.ID, recording.FileID, recording.SpaceID, recording.Name, recording.Title, recording.SizeBytes,
		recording.DurationMs, recording.Language, recording.Template, recording.TranslateTo, strings.Join(words, "\n"),
		recording.Status, boolInt(recording.Transcode), formatTime(now), formatTime(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Recording{}, ErrDuplicate
		}
		return Recording{}, err
	}
	kick(s.audio)
	return recording, nil
}

func firstAudio(probe mediaworker.Probe) (mediaworker.ProbeStream, bool) {
	for _, stream := range probe.Streams {
		if stream.Type == "audio" {
			return stream, true
		}
	}
	return mediaworker.ProbeStream{}, false
}

// playable says whether Chromium plays the container and codec directly.
func playable(container, codec string) bool {
	for _, name := range strings.Split(container, ",") {
		for _, allowed := range browserAudio[strings.TrimSpace(name)] {
			if allowed == codec {
				return true
			}
		}
	}
	return false
}

func titleOf(name string) string {
	title := strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if title == "" {
		title = name
	}
	return truncateRunes(title, maxTitleRunes)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func cleanHotwords(words []string) ([]string, error) {
	seen := map[string]bool{}
	cleaned := []string{}
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" || seen[word] {
			continue
		}
		if utf8.RuneCountInString(word) > 30 || strings.ContainsAny(word, "\n\r\t") {
			return nil, fmt.Errorf("%w: a hotword is too long", ErrInvalid)
		}
		seen[word] = true
		cleaned = append(cleaned, word)
	}
	if len(cleaned) > maxHotwordCount {
		return nil, fmt.Errorf("%w: too many hotwords", ErrInvalid)
	}
	return cleaned, nil
}

const recordingColumns = `id, file_id, space_id, name, title, title_edited, size_bytes, duration_ms, language, template,
    translate_to, hotwords, status, stage, waiting, progress, error_code, error_message, transcode, text_revision,
    names_revision, voiceprint_model, created_at, updated_at, processed_at`

type scanner interface{ Scan(...any) error }

func scanRecording(row scanner) (Recording, error) {
	var recording Recording
	var titleEdited, transcode int
	var hotwordText, errorCode, errorMessage, voiceModel, created, updated, processed string
	err := row.Scan(&recording.ID, &recording.FileID, &recording.SpaceID, &recording.Name, &recording.Title, &titleEdited,
		&recording.SizeBytes, &recording.DurationMs, &recording.Language, &recording.Template, &recording.TranslateTo,
		&hotwordText, &recording.Status, &recording.Stage, &recording.Waiting, &recording.Progress, &errorCode,
		&errorMessage, &transcode, &recording.TextRevision, &recording.NamesRevision, &voiceModel, &created, &updated, &processed)
	if err != nil {
		return Recording{}, err
	}
	recording.TitleEdited, recording.Transcode = titleEdited == 1, transcode == 1
	recording.Hotwords = []string{}
	if hotwordText != "" {
		recording.Hotwords = strings.Split(hotwordText, "\n")
	}
	recording.Error = failureOf(errorCode, errorMessage)
	recording.Voiceprints = voiceModel != ""
	recording.CreatedAt, recording.UpdatedAt, recording.ProcessedAt = parseTime(created), parseTime(updated), optionalTime(processed)
	recording.People = []string{}
	return recording, nil
}

// List returns the member's recordings, newest first.
func (s *Service) List(ctx context.Context, actor accounts.User) ([]Recording, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+recordingColumns+" FROM recordings WHERE owner_id = ? ORDER BY created_at DESC", actor.ID)
	if err != nil {
		return nil, err
	}
	recordings := []Recording{}
	index := map[string]int{}
	for rows.Next() {
		recording, err := scanRecording(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		index[recording.ID] = len(recordings)
		recordings = append(recordings, recording)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.fillListing(ctx, actor, recordings, index); err != nil {
		return nil, err
	}
	return recordings, nil
}

// fillListing adds speaker counts, names and previews.
func (s *Service) fillListing(ctx context.Context, actor accounts.User, recordings []Recording, index map[string]int) error {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT s.recording_id, COUNT(*), COALESCE(GROUP_CONCAT(p.name, char(10)), '')
FROM speakers s JOIN recordings r ON r.id = s.recording_id LEFT JOIN people p ON p.id = s.person_id
WHERE r.owner_id = ? GROUP BY s.recording_id`, actor.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, names string
		var count int
		if err := rows.Scan(&id, &count, &names); err != nil {
			_ = rows.Close()
			return err
		}
		if i, ok := index[id]; ok {
			recordings[i].Speakers = count
			if names != "" {
				recordings[i].People = strings.Split(names, "\n")
				sort.Strings(recordings[i].People)
			}
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// The preview prefers the newest summary's overview, else the first line.
	rows, err = s.store.db.QueryContext(ctx, `
SELECT r.id,
    COALESCE((SELECT content FROM summaries m WHERE m.recording_id = r.id AND m.status = 'ready' ORDER BY m.updated_at DESC LIMIT 1), ''),
    COALESCE((SELECT text FROM utterances u WHERE u.recording_id = r.id ORDER BY u.seq LIMIT 1), '')
FROM recordings r WHERE r.owner_id = ?`, actor.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, summary, first string
		if err := rows.Scan(&id, &summary, &first); err != nil {
			return err
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		preview := first
		if summary != "" {
			var content SummaryContent
			if json.Unmarshal([]byte(summary), &content) == nil && content.Overview != "" {
				preview = content.Overview
			}
		}
		recordings[i].Preview = truncateRunes(preview, 90)
	}
	return rows.Err()
}

// owned loads a recording of the member's.
func (s *Service) owned(ctx context.Context, actor accounts.User, id string) (Recording, error) {
	recording, err := scanRecording(s.store.db.QueryRowContext(ctx, "SELECT "+recordingColumns+" FROM recordings WHERE id = ? AND owner_id = ?", id, actor.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	return recording, err
}

// Get returns a recording with its transcript, speakers and results.
func (s *Service) Get(ctx context.Context, actor accounts.User, id string) (Detail, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Recording: recording, Utterances: []Utterance{}, SpeakerList: []Speaker{}, Translations: []Translation{}, Summaries: []Summary{}}
	rows, err := s.store.db.QueryContext(ctx, "SELECT seq, start_ms, end_ms, speaker, text, edited FROM utterances WHERE recording_id = ? ORDER BY seq", id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		var utterance Utterance
		var edited int
		if err := rows.Scan(&utterance.Seq, &utterance.StartMs, &utterance.EndMs, &utterance.Speaker, &utterance.Text, &edited); err != nil {
			_ = rows.Close()
			return Detail{}, err
		}
		utterance.Edited = edited == 1
		detail.Utterances = append(detail.Utterances, utterance)
	}
	_ = rows.Close()
	if detail.SpeakerList, err = s.speakers(ctx, id); err != nil {
		return Detail{}, err
	}
	detail.Speakers = len(detail.SpeakerList)
	for _, speaker := range detail.SpeakerList {
		if speaker.PersonID != "" {
			detail.People = append(detail.People, speaker.Label)
		}
	}
	if detail.Translations, err = s.translations(ctx, id); err != nil {
		return Detail{}, err
	}
	if detail.Summaries, err = s.summaries(ctx, id); err != nil {
		return Detail{}, err
	}
	return detail, nil
}

func (s *Service) speakers(ctx context.Context, recordingID string) ([]Speaker, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT s.key, s.number, COALESCE(s.person_id, ''), COALESCE(p.name, ''), s.assigned, s.score,
    COALESCE(s.suggested_id, ''), COALESCE(q.name, ''), s.suggested_score, s.speech_ms, s.profile IS NOT NULL
FROM speakers s LEFT JOIN people p ON p.id = s.person_id LEFT JOIN people q ON q.id = s.suggested_id
WHERE s.recording_id = ? ORDER BY s.number`, recordingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	speakers := []Speaker{}
	for rows.Next() {
		var speaker Speaker
		var name, suggestedID, suggestedName string
		var suggestedScore float64
		var voiceprint int
		if err := rows.Scan(&speaker.Key, &speaker.Number, &speaker.PersonID, &name, &speaker.Assigned, &speaker.Score,
			&suggestedID, &suggestedName, &suggestedScore, &speaker.SpeechMs, &voiceprint); err != nil {
			return nil, err
		}
		speaker.Voiceprint = voiceprint == 1
		speaker.Label = fmt.Sprintf("发言人 %d", speaker.Number)
		if speaker.PersonID != "" {
			speaker.Label = name
		} else {
			speaker.Assigned, speaker.Score = "", 0
		}
		if suggestedID != "" && speaker.PersonID == "" {
			speaker.Suggest = &Suggestion{PersonID: suggestedID, Name: suggestedName, Score: suggestedScore}
		}
		speakers = append(speakers, speaker)
	}
	return speakers, rows.Err()
}

func (s *Service) translations(ctx context.Context, recordingID string) ([]Translation, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT language, status, revision, model, progress, error_code, error_message, lines, updated_at
FROM translations WHERE recording_id = ? ORDER BY requested_at`, recordingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	translations := []Translation{}
	for rows.Next() {
		var translation Translation
		var code, message, lines, updated string
		if err := rows.Scan(&translation.Language, &translation.Status, &translation.Revision, &translation.Model,
			&translation.Progress, &code, &message, &lines, &updated); err != nil {
			return nil, err
		}
		translation.Error, translation.UpdatedAt = failureOf(code, message), parseTime(updated)
		translation.Lines = []TranslatedLine{}
		_ = json.Unmarshal([]byte(lines), &translation.Lines)
		translations = append(translations, translation)
	}
	return translations, rows.Err()
}

func (s *Service) summaries(ctx context.Context, recordingID string) ([]Summary, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT template, status, text_revision, names_revision, model, error_code, error_message, content, updated_at
FROM summaries WHERE recording_id = ? ORDER BY requested_at`, recordingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []Summary{}
	for rows.Next() {
		var summary Summary
		var code, message, content, updated string
		if err := rows.Scan(&summary.Template, &summary.Status, &summary.TextRevision, &summary.NamesRevision, &summary.Model,
			&code, &message, &content, &updated); err != nil {
			return nil, err
		}
		summary.Error, summary.UpdatedAt = failureOf(code, message), parseTime(updated)
		if content != "" {
			var parsed SummaryContent
			if json.Unmarshal([]byte(content), &parsed) == nil {
				summary.Content = &parsed
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// Rename sets the recording's title; an empty title returns to the name.
func (s *Service) Rename(ctx context.Context, actor accounts.User, id, title string) (Recording, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return Recording{}, err
	}
	title = strings.TrimSpace(title)
	edited := title != ""
	if !edited {
		title = titleOf(recording.Name)
	}
	if utf8.RuneCountInString(title) > maxTitleRunes || strings.ContainsAny(title, "\n\r") {
		return Recording{}, fmt.Errorf("%w: title", ErrInvalid)
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE recordings SET title = ?, title_edited = ?, updated_at = ? WHERE id = ?",
		title, boolInt(edited), formatTime(s.now()), id); err != nil {
		return Recording{}, err
	}
	return s.owned(ctx, actor, id)
}

// Delete removes the recording from 语音处理 with everything derived from
// it; the file stays. Voiceprints it taught are forgotten.
func (s *Service) Delete(ctx context.Context, actor accounts.User, id string) error {
	if _, err := s.owned(ctx, actor, id); err != nil {
		return err
	}
	s.mu.Lock()
	for _, key := range []string{id, "text:" + id} {
		if cancel, ok := s.running[key]; ok {
			cancel()
		}
	}
	s.mu.Unlock()
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM recordings WHERE id = ?", id); err != nil {
		return err
	}
	return s.rematch(ctx, actor.ID)
}

// Retry queues a failed recording again, from the start.
func (s *Service) Retry(ctx context.Context, actor accounts.User, id string) (Recording, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return Recording{}, err
	}
	if recording.Status != StatusFailed && recording.Status != StatusWaiting {
		return Recording{}, fmt.Errorf("%w: only a failed or waiting recording is tried again", ErrInvalid)
	}
	if _, err := s.store.db.ExecContext(ctx, `
UPDATE recordings SET status = ?, stage = '', waiting = '', progress = 0, error_code = '', error_message = '', attempts = 0, updated_at = ?
WHERE id = ?`, StatusQueued, formatTime(s.now()), id); err != nil {
		return Recording{}, err
	}
	kick(s.audio)
	return s.owned(ctx, actor, id)
}

// Audio opens the recording's file to play as its member; the caller closes it.
func (s *Service) Audio(ctx context.Context, actor accounts.User, id string) (files.Content, Recording, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return files.Content{}, Recording{}, err
	}
	content, err := s.files.OpenContent(ctx, actor, recording.FileID)
	return content, recording, err
}

// DecodedAudio streams the recording decoded to FLAC from start, for files
// browsers cannot play.
func (s *Service) DecodedAudio(ctx context.Context, actor accounts.User, id string, start float64) (io.ReadCloser, error) {
	if s.media == nil {
		return nil, fmt.Errorf("%w: no media worker", ErrUnavailable)
	}
	content, _, err := s.Audio(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	defer content.Reader.Close()
	return s.media.Audio(ctx, content.Reader, mediaworker.AudioRequest{Start: start, Bits: 16})
}

// EditUtterance corrects a sentence's text or moves it to another speaker.
func (s *Service) EditUtterance(ctx context.Context, actor accounts.User, id string, seq int, text *string, speaker *string) (Detail, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return Detail{}, err
	}
	if recording.Status != StatusReady {
		return Detail{}, ErrNotReady
	}
	var current Utterance
	if err := s.store.db.QueryRowContext(ctx, "SELECT speaker, text FROM utterances WHERE recording_id = ? AND seq = ?", id, seq).
		Scan(&current.Speaker, &current.Text); errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNotFound
	} else if err != nil {
		return Detail{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	now := formatTime(s.now())
	if text != nil && strings.TrimSpace(*text) != current.Text {
		value := strings.TrimSpace(*text)
		if value == "" || utf8.RuneCountInString(value) > maxTextRunes {
			return Detail{}, fmt.Errorf("%w: text", ErrInvalid)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE utterances SET text = ?, edited = 1 WHERE recording_id = ? AND seq = ?", value, id, seq); err != nil {
			return Detail{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE recordings SET text_revision = text_revision + 1, updated_at = ? WHERE id = ?", now, id); err != nil {
			return Detail{}, err
		}
	}
	if speaker != nil && *speaker != current.Speaker {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM speakers WHERE recording_id = ? AND key = ?", id, *speaker).Scan(&exists); err != nil {
			return Detail{}, err
		}
		if exists == 0 {
			return Detail{}, fmt.Errorf("%w: unknown speaker", ErrInvalid)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE utterances SET speaker = ? WHERE recording_id = ? AND seq = ?", *speaker, id, seq); err != nil {
			return Detail{}, err
		}
		if err := recountSpeech(ctx, tx, id); err != nil {
			return Detail{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE recordings SET names_revision = names_revision + 1, updated_at = ? WHERE id = ?", now, id); err != nil {
			return Detail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, id)
}

// recountSpeech sets each speaker's talk time from their sentences and drops
// speakers left without any.
func recountSpeech(ctx context.Context, tx *sql.Tx, recordingID string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE speakers SET speech_ms = COALESCE((SELECT SUM(end_ms - start_ms) FROM utterances u
    WHERE u.recording_id = speakers.recording_id AND u.speaker = speakers.key), 0)
WHERE recording_id = ?`, recordingID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
DELETE FROM speakers WHERE recording_id = ? AND NOT EXISTS (
    SELECT 1 FROM utterances u WHERE u.recording_id = speakers.recording_id AND u.speaker = speakers.key)`, recordingID)
	return err
}
