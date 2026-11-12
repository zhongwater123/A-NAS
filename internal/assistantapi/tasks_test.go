package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func init() {
	taskWatchInterval = 20 * time.Millisecond
}

// askedTasks stands in for the File Broker: the tasks it was asked to run
// now, for the member behind the request's session.
type askedTasks struct {
	mu    sync.Mutex
	asked []string
	fail  error
}

func (a *askedTasks) RunTask(ctx context.Context, task string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	a.asked = append(a.asked, accounts.SessionToken(ctx)+" "+task)
	return nil
}

func (a *askedTasks) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

// fakeJob is one of alice's scheduled tasks in the fake Hermes: its latest
// run, and what that run answered.
type fakeJob struct {
	id, name, state string
	lastRunAt       string
	lastStatus      string
	lastError       string
	answer          string
	running         bool
	runs            int
}

func (j *fakeJob) runID() string { return fmt.Sprintf("cron_%s_20261020_08%04d", j.id, j.runs) }

func (j *fakeJob) json() string {
	optional := func(value string) string {
		if value == "" {
			return "null"
		}
		return fmt.Sprintf("%q", value)
	}
	claim := "null"
	if j.running {
		claim = fmt.Sprintf(`{"at":%q,"by":"host:1"}`, time.Now().Format(time.RFC3339Nano))
	}
	return fmt.Sprintf(`{"id":%q,"name":%q,"prompt":"总结一下共享空间","schedule":{"kind":"cron","expr":"0 8 * * *","display":"0 8 * * *"},
		"schedule_display":"0 8 * * *","state":%q,"enabled":%t,"next_run_at":"2026-10-21T08:00:00+08:00","last_run_at":%s,
		"last_status":%s,"last_error":%s,"created_at":"2026-10-19T21:30:00.123456+08:00","repeat":{"times":null,"completed":%d},
		"fire_claim":%s,"hermes_home":"/srv/a-nas/data/assistant/alice/hermes","model":"deepseek-chat","deliver":"local"}`,
		j.id, j.name, j.state, j.state != "paused", optional(j.lastRunAt), optional(j.lastStatus), optional(j.lastError), j.runs, claim)
}

// cron serves Hermes's cron routes over the fake's jobs, and the transcripts
// of their runs.
func (f *fakeServe) cron(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/sessions/cron_") {
		if r.Method == http.MethodDelete {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		for _, job := range f.jobs {
			if r.URL.Path == "/api/sessions/"+job.runID()+"/messages" {
				_, _ = fmt.Fprintf(w, `{"messages":[{"role":"user","content":"[IMPORTANT: You are running as a scheduled cron job]"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1"}]},{"role":"tool","content":"3 files"},
					{"role":"assistant","content":[{"type":"text","text":%q}]}]}`, job.answer)
				return
			}
		}
		http.Error(w, `{"detail":"Session not found"}`, http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/cron/jobs" {
		items := make([]string, 0, len(f.jobs))
		for _, job := range f.jobs {
			items = append(items, job.json())
		}
		_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		return
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/cron/jobs/")
	id, action, _ := strings.Cut(rest, "/")
	var job *fakeJob
	for _, candidate := range f.jobs {
		if candidate.id == id {
			job = candidate
		}
	}
	if job == nil {
		http.Error(w, `{"detail":"Job not found"}`, http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "runs":
		if job.runs == 0 {
			_, _ = io.WriteString(w, `{"runs":[],"limit":20}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"runs":[{"id":%q,"title":"%s · Oct 20 08:00","source":"cron","started_at":1760918400,"ended_at":1760918412.5,
			"message_count":4,"system_prompt":"secret"},{"id":"cron_output:%s:2026-10-19_08-00-00","started_at":1760832000,"message_count":0}],"limit":20}`,
			job.runID(), job.name, job.id)
	case r.Method == http.MethodPost && (action == "pause" || action == "resume"):
		job.state = map[string]string{"pause": "paused", "resume": "scheduled"}[action]
		_, _ = io.WriteString(w, job.json())
	case r.Method == http.MethodGet && action == "":
		_, _ = io.WriteString(w, job.json())
	case r.Method == http.MethodPost && action == "trigger":
		// Hermes's own "run now", which starts a gateway inside the instance
		// (#469): the Product Service must never ask for it.
		f.triggered++
		http.Error(w, `{"detail":"a run was asked of the instance"}`, http.StatusInternalServerError)
	case r.Method == http.MethodDelete && action == "":
		f.jobs = removeJob(f.jobs, job)
		_, _ = io.WriteString(w, `{"ok":true}`)
	default:
		http.Error(w, `{"detail":"Not found"}`, http.StatusNotFound)
	}
}

func removeJob(jobs []*fakeJob, gone *fakeJob) []*fakeJob {
	var kept []*fakeJob
	for _, job := range jobs {
		if job != gone {
			kept = append(kept, job)
		}
	}
	return kept
}

func (f *fakeServe) addJob(job *fakeJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
}

func TestTasksShowWhatTheMemberAskedForAndNotHermesInternals(t *testing.T) {
	h := newHarness(t)
	h.serve.addJob(&fakeJob{id: "a1b2c3d4e5f6", name: "每日小结", state: "scheduled", lastRunAt: "2026-10-20T08:00:12.5+08:00", lastStatus: "ok", runs: 1})
	h.serve.addJob(&fakeJob{id: "bbbbbbbbbbbb", name: "看看新照片", state: "paused"})

	status, body := h.request(http.MethodGet, "/tasks", "")
	if status != http.StatusOK {
		t.Fatalf("tasks = %d %s", status, body)
	}
	for _, internal := range []string{"hermes_home", "/srv/a-nas", "deepseek", "deliver", "fire_claim"} {
		if strings.Contains(body, internal) {
			t.Fatalf("tasks pass on %q: %s", internal, body)
		}
	}
	var page struct {
		Tasks []task `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Tasks) != 2 {
		t.Fatalf("tasks = %s (%v)", body, err)
	}
	daily := page.Tasks[0]
	if daily.Name != "每日小结" || daily.Prompt != "总结一下共享空间" || daily.Schedule.Kind != "cron" || daily.Schedule.Expr != "0 8 * * *" ||
		daily.LastStatus != "ok" || daily.LastRunAt == nil || *daily.LastRunAt != 1792454412.5 || daily.NextRunAt == nil || *daily.NextRunAt != 1792540800 ||
		daily.CreatedAt == nil || *daily.CreatedAt != 1792416600.123 || daily.Running {
		t.Fatalf("daily task = %+v", daily)
	}
	if page.Tasks[1].State != "paused" || page.Tasks[1].LastRunAt != nil {
		t.Fatalf("paused task = %+v", page.Tasks[1])
	}

	status, body = h.request(http.MethodGet, "/tasks/a1b2c3d4e5f6/runs", "")
	if status != http.StatusOK || !strings.Contains(body, `"id":"cron_a1b2c3d4e5f6_20261020_080001"`) || strings.Contains(body, "system_prompt") ||
		strings.Contains(body, "cron_output") || !strings.Contains(body, `"endedAt":1760918412.5`) {
		t.Fatalf("runs = %d %s", status, body)
	}
	// A run is a conversation: it opens like one, though it is not listed with them.
	if status, body := h.request(http.MethodGet, "/sessions/cron_a1b2c3d4e5f6_20261020_080001/messages", ""); status != http.StatusOK {
		t.Fatalf("a run's messages = %d %s", status, body)
	}
	h.request(http.MethodGet, "/sessions", "")
	if rest := strings.Join(h.serve.rest, "\n"); !strings.Contains(rest, "exclude_sources=cron") {
		t.Fatalf("the conversation list asked Hermes %v", h.serve.rest)
	}

	if status, body := h.request(http.MethodPost, "/tasks/bbbbbbbbbbbb/resume", ""); status != http.StatusOK || !strings.Contains(body, `"state":"scheduled"`) {
		t.Fatalf("resume = %d %s", status, body)
	}
	if status, body := h.request(http.MethodPost, "/tasks/bbbbbbbbbbbb/pause", ""); status != http.StatusOK || !strings.Contains(body, `"state":"paused"`) {
		t.Fatalf("pause = %d %s", status, body)
	}
	if status, _ := h.request(http.MethodDelete, "/tasks/bbbbbbbbbbbb", ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	if status, body := h.request(http.MethodDelete, "/tasks/bbbbbbbbbbbb", ""); status != http.StatusNotFound || !strings.Contains(body, "task_not_found") {
		t.Fatalf("delete again = %d %s", status, body)
	}
	// Deleting a task deletes its runs first: Hermes finds them through it.
	if status, _ := h.request(http.MethodDelete, "/tasks/a1b2c3d4e5f6", ""); status != http.StatusNoContent {
		t.Fatalf("delete with a run = %d", status)
	}
	rest := strings.Join(h.serve.rest, "\n")
	run, job := strings.Index(rest, "DELETE /api/sessions/cron_a1b2c3d4e5f6_20261020_080001"), strings.Index(rest, "DELETE /api/cron/jobs/a1b2c3d4e5f6")
	if run < 0 || job < run || strings.Contains(rest, "DELETE /api/sessions/cron_output") {
		t.Fatalf("deleting a task asked Hermes %v", h.serve.rest)
	}
	// Hermes also finds a job by its name; only its id is passed on.
	for _, id := range []string{"每日小结", "a1b2c3d4e5f", "A1B2C3D4E5F6", "a1b2c3d4e5f6%2Fpause"} {
		if status, _ := h.request(http.MethodPost, "/tasks/"+id+"/pause", ""); status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Fatalf("pause %q = %d", id, status)
		}
	}
	for _, call := range h.serve.rest {
		if strings.Contains(call, "每日小结") || strings.Contains(call, "a1b2c3d4e5f/") {
			t.Fatalf("a name or a bad id reached Hermes: %v", h.serve.rest)
		}
	}
}

func TestATaskRunsNowInARoundOfItsMembersTasksNotInTheInstance(t *testing.T) {
	h := newHarness(t)
	job := &fakeJob{id: "a1b2c3d4e5f6", name: "每日小结", state: "paused"}
	h.serve.addJob(job)

	// The run is asked of the File Broker with the member's session, and
	// answered as started: its end shows in the task and as a notice.
	status, body := h.request(http.MethodPost, "/tasks/a1b2c3d4e5f6/run", "")
	if status != http.StatusAccepted || strings.TrimSpace(body) != `{"finished":false}` {
		t.Fatalf("running a paused task = %d %s", status, body)
	}
	if asked := h.tasks.all(); len(asked) != 1 || asked[0] != "alice-session a1b2c3d4e5f6" {
		t.Fatalf("asked of the File Broker: %v", asked)
	}
	// A task that is running, or whose runs are over, is not asked for.
	for state, code := range map[string]string{"running": "task_running", "completed": "task_finished", "error": "task_finished"} {
		h.serve.mu.Lock()
		job.running, job.state = state == "running", map[bool]string{true: "scheduled", false: state}[state == "running"]
		h.serve.mu.Unlock()
		if status, body := h.request(http.MethodPost, "/tasks/a1b2c3d4e5f6/run", ""); status != http.StatusConflict || !strings.Contains(body, code) {
			t.Fatalf("running a %s task = %d %s, want %s", state, status, body, code)
		}
	}
	if status, body := h.request(http.MethodPost, "/tasks/0123456789ab/run", ""); status != http.StatusNotFound || !strings.Contains(body, "task_not_found") {
		t.Fatalf("running a task that is not hers = %d %s", status, body)
	}
	if status, _ := h.request(http.MethodPost, "/tasks/每日小结/run", ""); status != http.StatusBadRequest {
		t.Fatalf("running a task by name = %d", status)
	}
	if asked := h.tasks.all(); len(asked) != 1 {
		t.Fatalf("asked of the File Broker after refusals: %v", asked)
	}
	// A session the File Broker does not accept is the member's to renew.
	h.serve.mu.Lock()
	job.running, job.state = false, "scheduled"
	h.serve.mu.Unlock()
	h.tasks.fail = ErrSessionRejected
	if status, body := h.request(http.MethodPost, "/tasks/a1b2c3d4e5f6/run", ""); status != http.StatusUnauthorized {
		t.Fatalf("running with a refused session = %d %s", status, body)
	}
	// Never through Hermes's own trigger: the instance would start a gateway.
	h.serve.mu.Lock()
	triggered := h.serve.triggered
	h.serve.mu.Unlock()
	if triggered != 0 {
		t.Fatalf("the instance was asked to run a task %d times", triggered)
	}
}

func TestWithoutTheFileBrokerNoTaskRunsNow(t *testing.T) {
	h := newHarness(t)
	h.serve.addJob(&fakeJob{id: "a1b2c3d4e5f6", name: "每日小结", state: "scheduled"})
	h.api.options.Tasks = nil
	if status, body := h.request(http.MethodPost, "/tasks/a1b2c3d4e5f6/run", ""); status != http.StatusServiceUnavailable || !strings.Contains(body, "assistant_unavailable") {
		t.Fatalf("running a task without the File Broker = %d %s", status, body)
	}
}

// heardRuns stands in for the account store's task notices.
type heardRuns struct {
	mu       sync.Mutex
	recorded []accounts.AssistantTaskRun
	current  []string
}

func (n *heardRuns) AssistantTasksHeard(_ context.Context, userID string) (map[string]time.Time, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	heard := map[string]time.Time{}
	if userID != alice.ID {
		return heard, nil
	}
	for _, run := range n.recorded {
		if run.RanAt.After(heard[run.TaskID]) {
			heard[run.TaskID] = run.RanAt
		}
	}
	return heard, nil
}

func (n *heardRuns) RecordAssistantTasks(_ context.Context, userID string, runs []accounts.AssistantTaskRun, current []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if userID != alice.ID {
		return fmt.Errorf("recorded for %s", userID)
	}
	n.recorded = append(n.recorded, runs...)
	n.current = current
	return nil
}

func (n *heardRuns) wait(t *testing.T, count int) []accounts.AssistantTaskRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n.mu.Lock()
		recorded := append([]accounts.AssistantTaskRun(nil), n.recorded...)
		n.mu.Unlock()
		if len(recorded) >= count {
			return recorded
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorded %d runs, want %d: %+v", len(recorded), count, recorded)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestASignedInMemberHearsOfEachFinishedRunOnce(t *testing.T) {
	serve := newFakeServe(t)
	serve.addJob(&fakeJob{id: "aaaaaaaaaaaa", name: "每日小结", state: "scheduled", lastRunAt: "2026-10-20T08:00:12+08:00", lastStatus: "ok", runs: 1,
		answer: "## 今日小结\n共享空间新增 **3** 个文件。"})
	serve.addJob(&fakeJob{id: "bbbbbbbbbbbb", name: "看看新照片", state: "scheduled", lastRunAt: "2026-10-20T08:00:30+08:00", lastStatus: "ok", runs: 1,
		answer: "[SILENT]"})
	serve.addJob(&fakeJob{id: "cccccccccccc", name: "整理下载", state: "scheduled", lastRunAt: "2026-10-20T08:01:00+08:00", lastStatus: "error", runs: 1,
		lastError: "RuntimeError: the model did not answer"})
	serve.addJob(&fakeJob{id: "dddddddddddd", name: "还没运行过", state: "scheduled"})
	notices := &heardRuns{}
	h := New(Options{Connector: NewSocketConnector(serve.dir, serve.dir), SocketDir: serve.dir, Notices: notices,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h.Keep(alice, "alice-session")
	t.Cleanup(func() { h.Forget("alice-session") })

	runs := notices.wait(t, 3)
	byTask := map[string]accounts.AssistantTaskRun{}
	for _, run := range runs {
		byTask[run.TaskID] = run
	}
	daily := byTask["aaaaaaaaaaaa"]
	if daily.Name != "每日小结" || daily.Status != "ok" || daily.Quiet || daily.SessionID != "cron_aaaaaaaaaaaa_20261020_080001" ||
		daily.Summary != "## 今日小结 共享空间新增 **3** 个文件。" || !daily.RanAt.Equal(time.Date(2026, 10, 20, 0, 0, 12, 0, time.UTC)) {
		t.Fatalf("daily run = %+v", daily)
	}
	if !byTask["bbbbbbbbbbbb"].Quiet || byTask["bbbbbbbbbbbb"].Summary != "" {
		t.Fatalf("a run that answered [SILENT] = %+v, want quiet", byTask["bbbbbbbbbbbb"])
	}
	if failed := byTask["cccccccccccc"]; failed.Status != "error" || failed.Summary != "RuntimeError: the model did not answer" || failed.Quiet {
		t.Fatalf("failed run = %+v", failed)
	}
	if _, ok := byTask["dddddddddddd"]; ok {
		t.Fatalf("a task that never ran was told: %+v", runs)
	}

	// Checked again, nothing is told twice; a new run is.
	time.Sleep(5 * taskWatchInterval)
	if got := notices.wait(t, 3); len(got) != 3 {
		t.Fatalf("runs told twice: %+v", got)
	}
	serve.mu.Lock()
	serve.jobs[0].runs++
	serve.jobs[0].lastRunAt = "2026-10-21T08:00:09+08:00"
	serve.jobs[0].answer = "今天没有新文件。"
	serve.mu.Unlock()
	if got := notices.wait(t, 4); got[3].TaskID != "aaaaaaaaaaaa" || got[3].Summary != "今天没有新文件。" || got[3].SessionID != "cron_aaaaaaaaaaaa_20261020_080002" {
		t.Fatalf("the next run = %+v", got[3])
	}
	notices.mu.Lock()
	current := strings.Join(notices.current, ",")
	notices.mu.Unlock()
	if current != "aaaaaaaaaaaa,bbbbbbbbbbbb,cccccccccccc,dddddddddddd" {
		t.Fatalf("current tasks = %s", current)
	}

	// Signing out ends the watch.
	h.Forget("alice-session")
	time.Sleep(5 * taskWatchInterval)
	serve.mu.Lock()
	serve.jobs[0].runs++
	serve.jobs[0].lastRunAt = "2026-10-22T08:00:09+08:00"
	serve.mu.Unlock()
	time.Sleep(10 * taskWatchInterval)
	notices.mu.Lock()
	told := len(notices.recorded)
	notices.mu.Unlock()
	if told != 4 {
		t.Fatalf("a run was told after signing out: %d", told)
	}
}

func TestQuietAnswersAreHermessSilenceMarkers(t *testing.T) {
	for answer, quiet := range map[string]bool{
		"[SILENT]":               true,
		"  [silent]  ":           true,
		"[SILENT] 没有新照片":         true,
		"NO_REPLY":               true,
		"*NO_REPLY*.":            true,
		"今天没有变化。\n\n[SILENT]":    true,
		"静默。":                    true,
		"【静默】":                   true,
		"Silent retry succeeded": false,
		"共享空间新增 3 个文件，没有需要 [SILENT] 的事。": false,
		"[SILENT": false,
		"":        false,
	} {
		if got := quietAnswer(answer); got != quiet {
			t.Errorf("quietAnswer(%q) = %t, want %t", answer, got, quiet)
		}
	}
}

func TestAStoredIDIsAConversationOrARunOfAScheduledTask(t *testing.T) {
	for id, want := range map[string]bool{
		fakeStored:                          true,
		"cron_a1b2c3d4e5f6_20261020_080001": true,
		// A run on Hermes's gateway keeps the id the runtime minted.
		"local-" + strings.Repeat("0a", 32): true,
		"local-" + strings.Repeat("0a", 31): false,
		"local-" + strings.Repeat("0A", 32): false,
		"local-":                            false,
		"20261015":                          false,
		"计划":                                false,
	} {
		if storedIDPattern.MatchString(id) != want {
			t.Errorf("stored id %q accepted = %t, want %t", id, !want, want)
		}
	}
}
