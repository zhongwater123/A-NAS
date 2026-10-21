package voice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/voiceworker"
)

// WorkerVoiceprints embeds voices in the voice Worker.
type WorkerVoiceprints struct {
	Client voiceworker.Client
}

func (w WorkerVoiceprints) Embed(ctx context.Context, samples []int16, groups []Group) (string, [][]float32, error) {
	segments := make([][]voiceworker.Segment, len(groups))
	for i, group := range groups {
		for _, span := range group.Spans {
			segments[i] = append(segments[i], voiceworker.Segment{Offset: span.Offset, Count: span.Count})
		}
	}
	return w.Client.EmbedSpeakers(ctx, samples, segments)
}

func (w WorkerVoiceprints) Cluster(ctx context.Context, samples []int16, spans []Span, threshold float64) (string, []int, [][]float32, error) {
	segments := make([]voiceworker.Segment, len(spans))
	for i, span := range spans {
		segments[i] = voiceworker.Segment{Offset: span.Offset, Count: span.Count}
	}
	return w.Client.ClusterSpeech(ctx, samples, segments, threshold)
}

// FakeRecognizer is the development stand-in for the recognition service: a
// conversation of two or three voices, a sentence every few seconds, chosen
// by the audio's bytes so the same file always reads the same.
type FakeRecognizer struct{}

func (FakeRecognizer) Ready(context.Context) bool { return true }

var fakeLines = []string{
	"好，我们开始吧，今天主要对一下下周家里装修的安排。",
	"先说厨房，橱柜的尺寸上周已经量好了，师傅说周三可以进场。",
	"周三我要上班，能不能改到周四上午？",
	"可以，我回头跟师傅确认一下，顺便把水电的验收也放在同一天。",
	"预算方面，目前比计划多了大概三千块，主要是瓷砖换了一个牌子。",
	"多出来的这部分从备用金里出吧，别动孩子的学费。",
	"同意。另外客厅的灯还没定，我挑了两款，晚上发到群里大家看看。",
	"灯的事不着急，先把厨房和卫生间做完。",
	"那就这么定：周四厨房进场，周五之前把灯选好。",
	"还有一件事，物业说装修押金要在开工前交，记得带上身份证。",
	"押金我明天去交，顺便问问电梯什么时候可以用。",
	"好，那今天就到这里，有问题随时在群里说。",
}

func (FakeRecognizer) Recognize(ctx context.Context, audio []byte, request RecognizeRequest) (Recognition, error) {
	if err := ctx.Err(); err != nil {
		return Recognition{}, err
	}
	digest := sha256.Sum256(audio)
	voices := 2 + int(digest[0]%2)
	length := request.Duration.Milliseconds()
	if length <= 0 {
		length = 60_000
	}
	var recognition Recognition
	at, index := int64(400), int(digest[1])
	for at+1500 < length {
		text := fakeLines[index%len(fakeLines)]
		span := int64(900 + 220*len([]rune(text))/2)
		end := min(at+span, length)
		speaker := 1 + (index+int(digest[2]))%voices
		recognition.Utterances = append(recognition.Utterances, Recognized{StartMs: at, EndMs: end, Text: text, Speaker: fmt.Sprint(speaker)})
		at = end + 300 + int64(digest[3+index%20])*4
		index++
	}
	return recognition, nil
}

// FakeVoiceprints gives each recognition label the same voice in every
// recording, so naming 发言人 1 once recognises 发言人 1 elsewhere: enough to
// try the interface without the voice Worker.
type FakeVoiceprints struct{}

const fakeDimensions = 32

func fakeVoice(label string, noise int) []float32 {
	vector := make([]float32, fakeDimensions)
	var norm float64
	for i := range vector {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", label, i)))
		value := float64(int8(digest[0])) / 128
		jitter := sha256.Sum256([]byte(fmt.Sprintf("%d/%d", noise, i)))
		value += 0.08 * float64(int8(jitter[0])) / 128
		vector[i] = float32(value)
		norm += value * value
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / norm)
	}
	return vector
}

func (FakeVoiceprints) Embed(_ context.Context, samples []int16, groups []Group) (string, [][]float32, error) {
	if len(samples) == 0 {
		return "", nil, errors.New("no samples")
	}
	vectors := make([][]float32, len(groups))
	for i, group := range groups {
		vectors[i] = fakeVoice(group.Label, len(group.Spans))
	}
	return "fake-voiceprints", vectors, nil
}

func (FakeVoiceprints) Cluster(_ context.Context, samples []int16, spans []Span, _ float64) (string, []int, [][]float32, error) {
	if len(samples) == 0 {
		return "", nil, nil, errors.New("no samples")
	}
	return "fake-voiceprints", make([]int, len(spans)), [][]float32{fakeVoice("1", 0)}, nil
}

// FakeTextModel answers translation and summary tasks without a model, from
// what the task holds, so the interface can be tried offline.
type FakeTextModel struct {
	// Delay makes answers take a while, as the real model does.
	Delay time.Duration
}

func (FakeTextModel) Name() string { return "fake-text-model" }

func (m FakeTextModel) Complete(ctx context.Context, prompt Prompt) (string, error) {
	if m.Delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(m.Delay):
		}
	}
	var task struct {
		Task       string           `json:"task"`
		Target     string           `json:"target"`
		Items      []translateItem  `json:"items"`
		Sections   []map[string]any `json:"sections"`
		Transcript string           `json:"transcript"`
		Parts      []SummaryContent `json:"parts"`
		Title      string           `json:"recordingTitle"`
		Actions    bool             `json:"wantActions"`
	}
	if err := json.Unmarshal([]byte(prompt.User), &task); err != nil {
		return "", err
	}
	switch task.Task {
	case "translate":
		items := make([]translateItem, len(task.Items))
		for i, item := range task.Items {
			items[i] = translateItem{ID: item.ID, Text: "〔" + task.Target + "〕" + item.Text}
		}
		answer, err := json.Marshal(map[string]any{"items": items})
		return string(answer), err
	case "summarize", "merge":
		lines := strings.Split(strings.TrimSpace(task.Transcript), "\n")
		if task.Task == "merge" {
			lines = nil
			for _, part := range task.Parts {
				for _, section := range part.Sections {
					lines = append(lines, section.Items...)
				}
			}
		}
		var sections []SummarySection
		for i, section := range task.Sections {
			key, _ := section["key"].(string)
			var items []string
			for j := i; j < len(lines) && len(items) < 3; j += max(1, len(task.Sections)) {
				items = append(items, strings.TrimSpace(afterSpeaker(lines[j])))
			}
			sections = append(sections, SummarySection{Key: key, Items: items})
		}
		content := SummaryContent{
			Title:    "家庭装修安排讨论",
			Overview: fmt.Sprintf("（本地演示摘要，未连接文本模型）这段录音共 %d 句，大家讨论了装修进场时间、预算与押金。", len(lines)),
			Sections: sections, Keywords: []string{"装修", "预算", "押金"},
		}
		if task.Actions {
			content.Actions = []ActionItem{{Task: "确认师傅周四上午进场", Owner: "发言人 2", Due: "本周"}, {Task: "交装修押金", Due: "明天"}}
		}
		answer, err := json.Marshal(content)
		return string(answer), err
	}
	return "", fmt.Errorf("unknown task %q", task.Task)
}

func afterSpeaker(line string) string {
	if _, text, ok := strings.Cut(line, "："); ok {
		return text
	}
	return line
}
