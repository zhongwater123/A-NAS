// Package voice is 语音处理: a member imports a recording, an ordinary audio
// file in a space they can read, and the Product Service transcribes it with
// a cloud recognition service (Doubao), tells its speakers apart and
// recognises the ones the member has named before by voiceprint, and
// translates and summarises the transcript with the assistant's text model
// (docs/architecture/voice.md, ADR 0027).
//
// The file stays where it is and is only ever read through files.Service as
// its member, so in production every read runs as that member through the
// File Broker (ADR 0008). Transcripts, speakers, voiceprints, translations
// and summaries are the member's own and live in voice.db on the system disk;
// no other account sees them. Voiceprints are computed on the device by the
// voice Worker and are never sent anywhere; the recording goes to the
// recognition service, and its transcript to the text model.
package voice

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

var (
	ErrNotFound = errors.New("voice resource not found")
	ErrInvalid  = errors.New("voice request is invalid")
	// ErrNotReady: the recording has no transcript yet.
	ErrNotReady = errors.New("the recording has no transcript yet")
	// ErrNotAudio: the file has no audio the media Worker can read.
	ErrNotAudio = errors.New("the file holds no readable audio")
	// ErrTooLong: the recording lasts longer than MaxDuration.
	ErrTooLong = errors.New("the recording is too long")
	// ErrDuplicate: the member already imported this file.
	ErrDuplicate = errors.New("the file is already imported")
	// ErrNameTaken: another person in the member's voiceprints has the name.
	ErrNameTaken = errors.New("another person has this name")
	// ErrUnavailable: a component the request needs is missing.
	ErrUnavailable = errors.New("voice processing is unavailable")
	// ErrForbidden: only administrators configure the recognition service.
	ErrForbidden = errors.New("only administrators may do this")
)

// Recording statuses.
const (
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	// StatusWaiting: the recording waits for something outside it; Waiting
	// says what.
	StatusWaiting = "waiting"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Stages of a recording while it is processed.
const (
	StagePreparing   = "preparing"
	StageRecognizing = "recognizing"
	StageIdentifying = "identifying"
)

// What a waiting recording waits for.
const (
	// WaitSignIn: its member has not signed in since the Product Service
	// started, so nothing can read the file as them.
	WaitSignIn = "sign_in"
	// WaitRecognizer: no administrator has configured the recognition service.
	WaitRecognizer = "recognizer"
	// WaitBusy: the recognition service was busy or unreachable; it is
	// tried again a little later.
	WaitBusy = "busy"
)

// Failure codes of recordings and of translation and summary jobs.
const (
	FailMissing     = "file_missing"
	FailNotAudio    = "not_audio"
	FailTooLong     = "too_long"
	FailRejected    = "recognizer_rejected"
	FailRecognizer  = "recognizer_failed"
	FailDecoder     = "decoder_unavailable"
	FailTextModel   = "text_model_failed"
	FailNoTextModel = "text_model_unavailable"
	FailInternal    = "internal"
)

// MaxDuration bounds what one recording may last: recognition is paid by the
// hour and the transcript must fit what the text model can summarise.
const MaxDuration = 5 * time.Hour

// Job statuses of translations and summaries.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobReady   = "ready"
	JobFailed  = "failed"
)

// Ways a speaker came to be named.
const (
	// AssignedAuto: their voice matched a named person's voiceprint.
	AssignedAuto = "auto"
	// AssignedUser: the member named them, which also teaches the voiceprint.
	AssignedUser = "user"
)

// Failure says why a recording or job stopped.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Recording is one imported audio file and where its processing stands.
type Recording struct {
	ID      string `json:"id"`
	FileID  string `json:"fileId"`
	SpaceID string `json:"spaceId"`
	// Name is the file's name when it was imported; Title is what the
	// recording is shown as: the name without its extension, the summary's
	// title once there is one, or what the member typed.
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	TitleEdited bool     `json:"titleEdited"`
	SizeBytes   int64    `json:"sizeBytes"`
	DurationMs  int64    `json:"durationMs"`
	Language    string   `json:"language"`
	Template    string   `json:"template"`
	TranslateTo string   `json:"translateTo"`
	Hotwords    []string `json:"hotwords"`
	Status      string   `json:"status"`
	Stage       string   `json:"stage,omitempty"`
	Waiting     string   `json:"waiting,omitempty"`
	// Progress is how far processing is, from 0 to 1.
	Progress float64  `json:"progress"`
	Error    *Failure `json:"error,omitempty"`
	// Transcode: browsers cannot play the file, so its audio is streamed
	// decoded from a start position.
	Transcode bool `json:"transcode"`
	// Speakers counts the voices in the transcript; People names those
	// that are named.
	Speakers int      `json:"speakers"`
	People   []string `json:"people"`
	// Preview is the start of a summary's overview, or of the transcript.
	Preview string `json:"preview,omitempty"`
	// Revisions count the edits that make translations (Text) and
	// summaries (Text and Names) out of date.
	TextRevision  int64 `json:"textRevision"`
	NamesRevision int64 `json:"namesRevision"`
	// Voiceprints: the speakers have voice vectors, so they can be
	// recognised and naming them teaches recognition.
	Voiceprints bool       `json:"voiceprints"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ProcessedAt *time.Time `json:"processedAt,omitempty"`
}

// Utterance is one sentence of the transcript.
type Utterance struct {
	Seq     int    `json:"seq"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Edited  bool   `json:"edited"`
}

// Speaker is one voice in a recording.
type Speaker struct {
	Key string `json:"key"`
	// Number orders the voices by when they first speak; Label is the
	// person's name when the speaker is named, otherwise "发言人 N".
	Number   int         `json:"number"`
	Label    string      `json:"label"`
	PersonID string      `json:"personId,omitempty"`
	Assigned string      `json:"assigned,omitempty"`
	Score    float64     `json:"score,omitempty"`
	Suggest  *Suggestion `json:"suggestion,omitempty"`
	SpeechMs int64       `json:"speechMs"`
	// Voiceprint: the speaker has a voice vector.
	Voiceprint bool `json:"voiceprint"`
}

// Suggestion is a named person whose voice is close but not close enough
// to name the speaker without asking.
type Suggestion struct {
	PersonID string  `json:"personId"`
	Name     string  `json:"name"`
	Score    float64 `json:"score"`
}

// Detail is a recording with its transcript and results.
type Detail struct {
	Recording
	Utterances   []Utterance   `json:"utterances"`
	SpeakerList  []Speaker     `json:"speakerList"`
	Translations []Translation `json:"translations"`
	Summaries    []Summary     `json:"summaries"`
}

// Translation is the transcript in another language, line by line.
type Translation struct {
	Language  string           `json:"language"`
	Status    string           `json:"status"`
	Error     *Failure         `json:"error,omitempty"`
	Revision  int64            `json:"revision"`
	Model     string           `json:"model,omitempty"`
	Progress  float64          `json:"progress"`
	Lines     []TranslatedLine `json:"lines"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// TranslatedLine is one utterance in the translation.
type TranslatedLine struct {
	Seq  int    `json:"seq"`
	Text string `json:"text"`
}

// Summary is the transcript summarised with a scene template.
type Summary struct {
	Template      string          `json:"template"`
	Status        string          `json:"status"`
	Error         *Failure        `json:"error,omitempty"`
	TextRevision  int64           `json:"textRevision"`
	NamesRevision int64           `json:"namesRevision"`
	Model         string          `json:"model,omitempty"`
	Content       *SummaryContent `json:"content,omitempty"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// SummaryContent is what the text model wrote, in the template's sections.
type SummaryContent struct {
	Title    string           `json:"title"`
	Overview string           `json:"overview"`
	Sections []SummarySection `json:"sections"`
	Actions  []ActionItem     `json:"actions"`
	Keywords []string         `json:"keywords"`
}

type SummarySection struct {
	Key   string   `json:"key"`
	Title string   `json:"title"`
	Items []string `json:"items"`
}

// ActionItem is a task the recording agreed on.
type ActionItem struct {
	Task  string `json:"task"`
	Owner string `json:"owner,omitempty"`
	Due   string `json:"due,omitempty"`
}

// Person is someone the member named in a recording; their voiceprint is
// what those recordings taught.
type Person struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Samples    int       `json:"samples"`
	Recordings int       `json:"recordings"`
	SpeechMs   int64     `json:"speechMs"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Files is the part of files.Service recordings are read through.
type Files interface {
	Lookup(ctx context.Context, actor accounts.User, entryID string) (files.Entry, string, error)
	OpenContent(ctx context.Context, actor accounts.User, entryID string) (files.Content, error)
}

// MediaWorker is what the service needs from the media Worker.
type MediaWorker interface {
	Probe(ctx context.Context, file *os.File) (mediaworker.Probe, error)
	Audio(ctx context.Context, file *os.File, request mediaworker.AudioRequest) (io.ReadCloser, error)
	Speech(ctx context.Context, file *os.File, request mediaworker.SpeechRequest) (io.ReadCloser, error)
}

// Recognizer transcribes speech. Audio is Ogg Opus, 16 kHz mono.
type Recognizer interface {
	// Ready says whether an administrator configured it.
	Ready(ctx context.Context) bool
	Recognize(ctx context.Context, audio []byte, request RecognizeRequest) (Recognition, error)
}

// RecognizeRequest describes one piece of a recording.
type RecognizeRequest struct {
	// Language is the recognition service's language code, "" to let it
	// recognise Mandarin, English and dialects by itself.
	Language string
	Hotwords []string
	// Duration is how long the audio lasts.
	Duration time.Duration
}

// Recognition is what the recognition service heard in one request.
type Recognition struct {
	Utterances []Recognized
}

// Recognized is one sentence; Speaker is the service's label for the voice
// within this request, "" when it told none apart.
type Recognized struct {
	StartMs int64
	EndMs   int64
	Text    string
	Speaker string
}

var (
	// ErrRecognizerRejected: the service refused the credentials.
	ErrRecognizerRejected = errors.New("the recognition service rejected the credentials")
	// ErrRecognizerBusy: the service is busy or unreachable; try later.
	ErrRecognizerBusy = errors.New("the recognition service is busy")
	// ErrRecognizerUnconfigured: no administrator configured the service.
	ErrRecognizerUnconfigured = errors.New("the recognition service is not configured")
)

// TextModel translates and summarises.
type TextModel interface {
	Name() string
	Complete(ctx context.Context, prompt Prompt) (string, error)
}

// Prompt is one chat completion: User is a JSON task, JSON asks for a JSON
// object back.
type Prompt struct {
	System    string
	User      string
	JSON      bool
	MaxTokens int
}

// Voiceprints embeds voices: Embed returns one unit vector per group of
// spans, Cluster groups spans by voice.
type Voiceprints interface {
	Embed(ctx context.Context, samples []int16, groups []Group) (string, [][]float32, error)
	Cluster(ctx context.Context, samples []int16, spans []Span, threshold float64) (string, []int, [][]float32, error)
}

// Span is Count samples from Offset of 16 kHz mono speech. Label is the
// recognition service's label for its voice, "" when it gave none; the
// voice Worker never sees it, but it lets development fakes act as voices.
type Span struct {
	Offset int
	Count  int
	Label  string
}

// Group is the spans of one voice.
type Group struct {
	Label string
	Spans []Span
}
