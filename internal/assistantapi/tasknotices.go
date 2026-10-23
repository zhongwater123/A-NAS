package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TaskNotices keeps what a member was told of the runs of their scheduled
// tasks (accounts.Service): a notice per finished run, at most one unread
// per task.
type TaskNotices interface {
	AssistantTasksHeard(ctx context.Context, userID string) (map[string]time.Time, error)
	RecordAssistantTasks(ctx context.Context, userID string, runs []accounts.AssistantTaskRun, current []string) error
}

// taskWatchInterval is how often a signed-in member's tasks are checked for
// finished runs; tests shorten it.
var taskWatchInterval = time.Minute

// summaryRunes bounds the start of a run's answer a notice carries.
const summaryRunes = 200

// watchTasks tells a signed-in member of each run of their scheduled tasks
// that finished since they last heard, including runs while they were away.
// It runs beside the kept instance and ends with it; scheduled runs while
// nobody is signed in are told at the next sign-in.
func (h *handler) watchTasks(account string, k *keep) {
	for {
		h.keepsMu.Lock()
		session := ""
		if h.keeps[account] == k {
			for candidate := range k.sessions {
				session = candidate
				break
			}
		}
		h.keepsMu.Unlock()
		if session == "" {
			return
		}
		ctx, cancel := context.WithTimeout(accounts.WithSessionToken(h.ctx, session), requestTimeout)
		err := h.noteTaskRuns(ctx, account, k.userID)
		cancel()
		if err != nil && !errors.Is(err, ErrNoAssistant) && !errors.Is(err, ErrSessionRejected) {
			h.logger.Warn("assistant tasks not checked", "account", account, "error", err)
		}
		time.Sleep(taskWatchInterval)
	}
}

// noteTaskRuns records a notice for each task whose latest run the member
// has not heard of.
func (h *handler) noteTaskRuns(ctx context.Context, account, userID string) error {
	var jobs []hermesJob
	if err := h.restJSON(ctx, account, "/api/cron/jobs", &jobs); err != nil {
		return err
	}
	heard, err := h.options.Notices.AssistantTasksHeard(ctx, userID)
	if err != nil {
		return err
	}
	current := []string{}
	var runs []accounts.AssistantTaskRun
	for _, job := range jobs {
		if !taskIDPattern.MatchString(job.ID) {
			continue
		}
		current = append(current, job.ID)
		ranAt, ok := parseHermesTime(job.LastRunAt)
		if !ok {
			continue
		}
		if last, ok := heard[job.ID]; ok && !ranAt.After(last) {
			continue
		}
		run := accounts.AssistantTaskRun{TaskID: job.ID, Name: job.Name, Status: "ok", RanAt: ranAt.UTC()}
		if job.LastStatus != nil && *job.LastStatus != "" {
			run.Status = *job.LastStatus
		}
		if err := h.describeRun(ctx, account, job, &run); err != nil {
			return err
		}
		runs = append(runs, run)
	}
	return h.options.Notices.RecordAssistantTasks(ctx, userID, runs, current)
}

// describeRun adds a run's conversation and the start of its answer, or of
// its error. A run that answered only Hermes's silence marker had nothing to
// report: it is quiet.
func (h *handler) describeRun(ctx context.Context, account string, job hermesJob, run *accounts.AssistantTaskRun) error {
	if run.Status != "ok" && job.LastError != nil {
		run.Summary = summarize(*job.LastError)
	}
	var page struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := h.restJSON(ctx, account, "/api/cron/jobs/"+job.ID+"/runs?limit=1", &page); err != nil {
		return err
	}
	if len(page.Runs) == 0 || !storedIDPattern.MatchString(page.Runs[0].ID) {
		return nil
	}
	run.SessionID = page.Runs[0].ID
	if run.Status != "ok" {
		return nil
	}
	var transcript struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := h.restJSON(ctx, account, "/api/sessions/"+run.SessionID+"/messages?inline_images=false", &transcript); err != nil {
		return err
	}
	for _, message := range slices.Backward(transcript.Messages) {

		if message.Role != "assistant" {
			continue
		}
		if answer := contentText(message.Content); strings.TrimSpace(answer) != "" {
			run.Quiet = quietAnswer(answer)
			if !run.Quiet {
				run.Summary = summarize(answer)
			}
			break
		}
	}
	return nil
}

// restJSON gets path from the account's instance and decodes the reply.
func (h *handler) restJSON(ctx context.Context, account, path string, into any) error {
	status, data, err := h.rest(ctx, account, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s answered %d", strings.SplitN(path, "?", 2)[0], status)
	}
	return json.Unmarshal(data, into)
}

// contentText is a message's text: Hermes stores a string, or a list of
// parts.
func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var joined []string
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			joined = append(joined, part.Text)
		}
	}
	return strings.Join(joined, "\n")
}

func summarize(text string) string {
	return truncateText(strings.Join(strings.Fields(text), " "), summaryRunes)
}

// silenceMarkers are Hermes's (gateway/response_filters.py,
// LIVE_GATEWAY_SILENT_MARKERS): the cron prompt tells the model to answer
// "[SILENT]" when there is nothing new to report, and Hermes then delivers
// nothing.
var silenceMarkers = map[string]bool{
	"[SILENT]": true, "SILENT": true, "NO_REPLY": true, "NO REPLY": true,
	"[静默]": true, "静默": true, "[沉默]": true, "沉默": true,
}

// quietAnswer is Hermes's is_autonomous_silence_response: a bracketed marker
// opening the answer, or a marker that is the whole answer or its own first
// or last line. A marker within a sentence is content.
func quietAnswer(answer string) bool {
	stripped := strings.TrimSpace(answer)
	if stripped == "" {
		return false
	}
	upper := strings.ToUpper(stripped)
	for marker := range silenceMarkers {
		if strings.HasPrefix(marker, "[") && strings.HasPrefix(upper, marker) {
			return true
		}
	}
	var lines []string
	for line := range strings.SplitSeq(stripped, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return silenceOnly(stripped) || silenceOnly(lines[0]) || silenceOnly(lines[len(lines)-1])
}

// silenceOnly is Hermes's is_intentional_silence_response: exactly a marker,
// allowing stray punctuation around it (but not around a bracket).
func silenceOnly(text string) bool {
	stripped := strings.TrimSpace(text)
	if count := utf8.RuneCountInString(stripped); count == 0 || count > 64 {
		return false
	}
	bare := strings.TrimSpace(strings.TrimFunc(stripped, func(r rune) bool { return r != '[' && r != ']' && unicode.IsPunct(r) }))
	canonical := func(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
	return silenceMarkers[canonical(stripped)] || silenceMarkers[canonical(bare)]
}
