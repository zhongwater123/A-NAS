package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DoubaoFlashURL is Doubao's 录音文件识别极速版: one request carries the
// audio itself and returns the transcript, without the standard version's
// submit/query, which only takes a URL the service downloads from and so
// cannot reach a NAS on a home network.
// https://www.volcengine.com/docs/6561/1631584
const DoubaoFlashURL = "https://openspeech.bytedance.com/api/v3/auc/bigmodel/recognize/flash"

// DoubaoResource is the flash version's resource ID.
const DoubaoResource = "volc.bigasr.auc_turbo"

// Status codes in the X-Api-Status-Code header.
const (
	doubaoOK        = "20000000"
	doubaoSilent    = "20000003"
	doubaoBusy      = "55000031"
	doubaoEmpty     = "45000002"
	doubaoBadFormat = "45000151"
)

// DoubaoCredentials are what the Volcengine console gives: the new console's
// API Key alone, or the old console's APP ID and Access Token.
type DoubaoCredentials struct {
	APIKey      string
	AppID       string
	AccessToken string
}

func (c DoubaoCredentials) usable() bool {
	return c.APIKey != "" || c.AppID != "" && c.AccessToken != ""
}

// Doubao recognises speech with Doubao's flash recognition, using the
// credentials Credentials returns for every request, so a change an
// administrator makes applies at once.
type Doubao struct {
	Credentials func(context.Context) (DoubaoCredentials, error)
	HTTP        *http.Client
	URL         string
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
		Data     string `json:"data"`
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

func (d Doubao) Recognize(ctx context.Context, audio []byte, request RecognizeRequest) (Recognition, error) {
	credentials, err := d.Credentials(ctx)
	if err != nil {
		return Recognition{}, err
	}
	if !credentials.usable() {
		return Recognition{}, ErrRecognizerUnconfigured
	}
	var body doubaoRequest
	body.User.UID = "a-nas"
	body.Audio.Data = base64.StdEncoding.EncodeToString(audio)
	body.Audio.Format, body.Audio.Codec = "ogg", "opus"
	body.Audio.Rate, body.Audio.Bits, body.Audio.Channel = 16000, 16, 1
	body.Audio.Language = request.Language
	body.Request.ModelName = "bigmodel"
	body.Request.EnableITN, body.Request.EnablePunc, body.Request.ShowUtterances = true, true, true
	// Speakers are told apart in every language the service can; the
	// newer separation only runs for Mandarin or an unnamed language.
	body.Request.EnableSpeakerInfo = true
	if request.Language == "" || request.Language == "zh-CN" {
		body.Request.SSDVersion = "200"
	}
	if words := hotwords(request.Hotwords); len(words) > 0 {
		encoded, _ := json.Marshal(map[string]any{"hotwords": words})
		body.Request.Corpus = &doubaoCorpus{Context: string(encoded)}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Recognition{}, err
	}
	url := d.URL
	if url == "" {
		url = DoubaoFlashURL
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Recognition{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if credentials.APIKey != "" {
		httpRequest.Header.Set("X-Api-Key", credentials.APIKey)
	} else {
		httpRequest.Header.Set("X-Api-App-Key", credentials.AppID)
		httpRequest.Header.Set("X-Api-Access-Key", credentials.AccessToken)
	}
	httpRequest.Header.Set("X-Api-Resource-Id", DoubaoResource)
	httpRequest.Header.Set("X-Api-Request-Id", requestID())
	httpRequest.Header.Set("X-Api-Sequence", "-1")
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return Recognition{}, ctx.Err()
		}
		return Recognition{}, fmt.Errorf("%w: %v", ErrRecognizerBusy, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return Recognition{}, fmt.Errorf("%w: %v", ErrRecognizerBusy, err)
	}
	status := response.Header.Get("X-Api-Status-Code")
	message := strings.TrimSpace(response.Header.Get("X-Api-Message"))
	if message == "" {
		message = doubaoErrorMessage(data)
	}
	logID := response.Header.Get("X-Tt-Logid")
	switch {
	case status == doubaoOK:
	case status == doubaoSilent || status == doubaoEmpty:
		// No voice: an empty transcript, not a failure.
		return Recognition{}, nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || strings.HasPrefix(status, "4500001"):
		return Recognition{}, fmt.Errorf("%w: %s (logid %s)", ErrRecognizerRejected, message, logID)
	case status == doubaoBusy || strings.HasPrefix(status, "550") || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return Recognition{}, fmt.Errorf("%w: %s %s (logid %s)", ErrRecognizerBusy, status, message, logID)
	case status == doubaoBadFormat:
		return Recognition{}, fmt.Errorf("%w: %s", ErrNotAudio, message)
	default:
		return Recognition{}, fmt.Errorf("recognition failed: HTTP %d, status %s: %s (logid %s)", response.StatusCode, status, message, logID)
	}
	var result doubaoResponse
	if err := json.Unmarshal(data, &result); err != nil {
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
		duration := result.AudioInfo.Duration
		if duration <= 0 {
			duration = request.Duration.Milliseconds()
		}
		recognition.Utterances = []Recognized{{StartMs: 0, EndMs: duration, Text: strings.TrimSpace(result.Result.Text)}}
	}
	return recognition, nil
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

// requestID is a random UUID, as the service asks for.
func requestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
