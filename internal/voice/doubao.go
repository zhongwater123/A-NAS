package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Doubao's 录音文件识别标准版: a task is submitted with a URL the service
// downloads the audio from, and its result is asked for until it is ready.
// https://docs.volcengine.com/docs/DoubaoVoice/task-submission-http-1
const (
	DoubaoSubmitURL = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/submit"
	DoubaoQueryURL  = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/query"
)

// DoubaoResource is 豆包录音文件识别模型 2.0.
const DoubaoResource = "volc.seedasr.auc"

// Status codes in the X-Api-Status-Code header.
const (
	doubaoOK         = "20000000"
	doubaoProcessing = "20000001"
	doubaoQueued     = "20000002"
	doubaoSilent     = "20000003"
	doubaoInvalid    = "45000001"
	doubaoEmpty      = "45000002"
	doubaoQuota      = "45000131"
	doubaoTooLarge   = "45000132"
	doubaoBadFormat  = "45000151"
	doubaoBusy       = "55000031"
)

// DoubaoCredentials are what the Volcengine console gives: the new console's
// API Key alone, or the old console's APP ID and Access Token; and the TOS
// bucket that relays the audio.
type DoubaoCredentials struct {
	APIKey      string
	AppID       string
	AccessToken string
	Storage     StorageCredentials
}

func (c DoubaoCredentials) usable() bool {
	return (c.APIKey != "" || c.AppID != "" && c.AccessToken != "") && c.Storage.usable()
}

// Doubao recognises speech with Doubao's recording recognition, using the
// credentials Credentials returns for every request, so a change an
// administrator makes applies at once.
type Doubao struct {
	Credentials func(context.Context) (DoubaoCredentials, error)
	HTTP        *http.Client
	// SubmitURL, QueryURL and Relay replace the service and TOS, for tests.
	SubmitURL string
	QueryURL  string
	Relay     func(StorageCredentials) Relay
}

func (d Doubao) Ready(ctx context.Context) bool {
	credentials, err := d.Credentials(ctx)
	return err == nil && credentials.usable()
}

type doubaoRequest struct {
	User struct {
		UID string `json:"uid"`
	} `json:"user"`
	Audio struct {
		URL      string `json:"url"`
		Format   string `json:"format"`
		Codec    string `json:"codec"`
		Rate     int    `json:"rate"`
		Bits     int    `json:"bits"`
		Channel  int    `json:"channel"`
		Language string `json:"language,omitempty"`
	} `json:"audio"`
	Request struct {
		ModelName         string        `json:"model_name"`
		EnableITN         bool          `json:"enable_itn"`
		EnablePunc        bool          `json:"enable_punc"`
		EnableSpeakerInfo bool          `json:"enable_speaker_info"`
		SSDVersion        string        `json:"ssd_version,omitempty"`
		SSDMode           int           `json:"ssd_mode,omitempty"`
		ShowUtterances    bool          `json:"show_utterances"`
		Corpus            *doubaoCorpus `json:"corpus,omitempty"`
	} `json:"request"`
}

type doubaoCorpus struct {
	Context string `json:"context"`
}

type doubaoResponse struct {
	AudioInfo struct {
		Duration int64 `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text       string `json:"text"`
		Utterances []struct {
			StartTime int64           `json:"start_time"`
			EndTime   int64           `json:"end_time"`
			Text      string          `json:"text"`
			Additions json.RawMessage `json:"additions"`
		} `json:"utterances"`
	} `json:"result"`
}

// maxHotwords bounds the words sent with a request; the service takes 5000.
const maxHotwords = 200

func (d Doubao) relay(storage StorageCredentials) Relay {
	if d.Relay != nil {
		return d.Relay(storage)
	}
	return TOS{Credentials: storage}
}

// Submit relays the audio through TOS and hands the service its URL.
func (d Doubao) Submit(ctx context.Context, audio []byte, request RecognizeRequest) (Task, error) {
	credentials, err := d.Credentials(ctx)
	if err != nil {
		return Task{}, err
	}
	if !credentials.usable() {
		return Task{}, ErrRecognizerUnconfigured
	}
	task := Task{ID: requestID(), Bucket: credentials.Storage.Bucket, Region: credentials.Storage.Region}
	task.Object = relayPrefix + task.ID + ".ogg"
	relay := d.relay(credentials.Storage)
	url, err := relay.Put(ctx, task.Object, audio)
	if err != nil {
		return Task{}, err
	}
	var body doubaoRequest
	body.User.UID = "a-nas"
	body.Audio.URL = url
	body.Audio.Format, body.Audio.Codec = "ogg", "opus"
	body.Audio.Rate, body.Audio.Bits, body.Audio.Channel = 16000, 16, 1
	body.Audio.Language = request.Language
	body.Request.ModelName = "bigmodel"
	body.Request.EnableITN, body.Request.EnablePunc, body.Request.ShowUtterances = true, true, true
	// The service tells speakers apart only for Mandarin or an unnamed
	// language; others are told apart on the device by voiceprint.
	if request.Language == "" || request.Language == "zh-CN" {
		body.Request.EnableSpeakerInfo = true
		body.Request.SSDVersion, body.Request.SSDMode = speakerSeparation(request)
	}
	if words := hotwords(request.Hotwords); len(words) > 0 {
		encoded, _ := json.Marshal(map[string]any{"hotwords": words})
		body.Request.Corpus = &doubaoCorpus{Context: string(encoded)}
	}
	answer, err := d.call(ctx, d.SubmitURL, DoubaoSubmitURL, credentials, task.ID, body)
	if err == nil && answer.status != doubaoOK {
		err = answer.err()
	}
	if err != nil {
		_ = relay.Delete(context.WithoutCancel(ctx), task.Object)
		return Task{}, err
	}
	return task, nil
}

// speakerSeparation picks the service's separation model as its best
// practice says: the standard model for audio under 3 minutes, the long
// non-meeting mode for calls and memos, the meeting model otherwise.
// https://docs.volcengine.com/docs/DoubaoVoice/speaker-separation
func speakerSeparation(request RecognizeRequest) (version string, mode int) {
	switch {
	case request.Duration > 0 && request.Duration < 3*time.Minute:
		return "200", 0
	case request.Scene == "call" || request.Scene == "memo":
		return "200", 1
	}
	return "300", 0
}

// Result asks the service for the task's transcript.
func (d Doubao) Result(ctx context.Context, task Task) (Recognition, error) {
	credentials, err := d.Credentials(ctx)
	if err != nil {
		return Recognition{}, err
	}
	if !credentials.usable() {
		return Recognition{}, ErrRecognizerUnconfigured
	}
	answer, err := d.call(ctx, d.QueryURL, DoubaoQueryURL, credentials, task.ID, struct{}{})
	if err != nil {
		return Recognition{}, err
	}
	switch answer.status {
	case doubaoOK:
	case doubaoProcessing, doubaoQueued:
		return Recognition{}, ErrRecognizing
	case doubaoSilent, doubaoEmpty:
		// No voice: an empty transcript, not a failure.
		return Recognition{}, nil
	case doubaoInvalid:
		// Asked about a task it does not have, or no longer has.
		return Recognition{}, fmt.Errorf("%w: %s", ErrRecognitionLost, answer.describe())
	default:
		return Recognition{}, answer.err()
	}
	var result doubaoResponse
	if err := json.Unmarshal(answer.body, &result); err != nil {
		return Recognition{}, fmt.Errorf("decode the recognition result: %w", err)
	}
	recognition := Recognition{Utterances: make([]Recognized, 0, len(result.Result.Utterances))}
	for _, utterance := range result.Result.Utterances {
		text := strings.TrimSpace(utterance.Text)
		if text == "" {
			continue
		}
		recognition.Utterances = append(recognition.Utterances, Recognized{
			StartMs: utterance.StartTime, EndMs: max(utterance.EndTime, utterance.StartTime), Text: text,
			Speaker: speakerOf(utterance.Additions),
		})
	}
	if len(recognition.Utterances) == 0 && strings.TrimSpace(result.Result.Text) != "" {
		recognition.Utterances = []Recognized{{StartMs: 0, EndMs: result.AudioInfo.Duration, Text: strings.TrimSpace(result.Result.Text)}}
	}
	return recognition, nil
}

// Release deletes the relayed audio, from the bucket it was put in.
func (d Doubao) Release(ctx context.Context, task Task) error {
	if task.Object == "" {
		return nil
	}
	credentials, err := d.Credentials(ctx)
	if err != nil {
		return err
	}
	storage := credentials.Storage
	storage.Bucket, storage.Region = task.Bucket, task.Region
	return d.relay(storage).Delete(ctx, task.Object)
}

// doubaoAnswer is a response's status headers and body.
type doubaoAnswer struct {
	http    int
	status  string
	message string
	logID   string
	body    []byte
}

func (a doubaoAnswer) describe() string {
	return fmt.Sprintf("%s %s (logid %s)", a.status, a.message, a.logID)
}

// err maps a status that is not a result to the service's errors.
func (a doubaoAnswer) err() error {
	switch {
	case a.http == http.StatusUnauthorized || a.http == http.StatusForbidden || strings.HasPrefix(a.status, "4500001"):
		return fmt.Errorf("%w: %s", ErrRecognizerRejected, a.describe())
	case a.status == doubaoBusy || a.status == doubaoQuota || strings.HasPrefix(a.status, "550") ||
		a.http == http.StatusTooManyRequests || a.http >= 500:
		return fmt.Errorf("%w: %s", ErrRecognizerBusy, a.describe())
	case a.status == doubaoBadFormat:
		return fmt.Errorf("%w: %s", ErrNotAudio, a.describe())
	case a.status == doubaoTooLarge:
		return fmt.Errorf("%w: %s", ErrTooLong, a.describe())
	}
	return fmt.Errorf("%w: HTTP %d, %s", ErrRecognitionFailed, a.http, a.describe())
}

// call posts one request with the task's ID and the credentials' headers.
func (d Doubao) call(ctx context.Context, url, fallback string, credentials DoubaoCredentials, taskID string, body any) (doubaoAnswer, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return doubaoAnswer{}, err
	}
	if url == "" {
		url = fallback
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return doubaoAnswer{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if credentials.APIKey != "" {
		request.Header.Set("X-Api-Key", credentials.APIKey)
	} else {
		request.Header.Set("X-Api-App-Key", credentials.AppID)
		request.Header.Set("X-Api-Access-Key", credentials.AccessToken)
	}
	request.Header.Set("X-Api-Resource-Id", DoubaoResource)
	request.Header.Set("X-Api-Request-Id", taskID)
	request.Header.Set("X-Api-Sequence", "-1")
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return doubaoAnswer{}, ctx.Err()
		}
		return doubaoAnswer{}, fmt.Errorf("%w: %v", ErrRecognizerBusy, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return doubaoAnswer{}, fmt.Errorf("%w: %v", ErrRecognizerBusy, err)
	}
	answer := doubaoAnswer{
		http: response.StatusCode, status: response.Header.Get("X-Api-Status-Code"),
		message: strings.TrimSpace(response.Header.Get("X-Api-Message")), logID: response.Header.Get("X-Tt-Logid"), body: data,
	}
	if answer.message == "" {
		answer.message = doubaoErrorMessage(data)
	}
	return answer, nil
}

// speakerOf reads the speaker label from an utterance's additions, which
// carries it as a string or a number.
func speakerOf(additions json.RawMessage) string {
	if len(additions) == 0 {
		return ""
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(additions, &values) != nil {
		return ""
	}
	raw, ok := values["speaker"]
	if !ok {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func doubaoErrorMessage(data []byte) string {
	var envelope struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Message != "" {
		return envelope.Message
	}
	return strings.TrimSpace(string(data[:min(len(data), 200)]))
}

func hotwords(words []string) []map[string]string {
	seen := map[string]bool{}
	var list []map[string]string
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" || seen[word] || len([]rune(word)) > 30 {
			continue
		}
		seen[word] = true
		list = append(list, map[string]string{"word": word})
		if len(list) == maxHotwords {
			break
		}
	}
	return list
}

// requestID is a random UUID, as the service asks for; it names the task.
func requestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
