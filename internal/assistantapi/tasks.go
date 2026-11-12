package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// Scheduled tasks (ADR 0024 decision 8) are Hermes's cron jobs: the member
// makes one in a conversation, with Hermes's own cronjob tool, and
// anas-assistant-cron-waker runs it when due in the member's own sandbox,
// with nobody there to confirm a change. These routes show the member's tasks
// and each task's runs, and pause, resume or delete a task, through Hermes's
// own cron routes as Hermes Desktop uses them; running a task now is asked of
// the File Broker instead (runTask). They do not make or edit tasks: what a
// task does is what the member asked for in a conversation. A run is a
// conversation of its own (source cron), listed under its task, not among the
// conversations.

// taskIDPattern is the id Hermes gives a job (uuid4 hex, 12 digits); matching
// it also shuts out Hermes's lookups by job name.
var taskIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

const (
	taskRunLimit = 20
	// runTimeout is the longest a run lasts: the cron unit's own
	// TimeoutStartSec.
	runTimeout = time.Hour
)

// TaskRunner has one of the signed-in member's scheduled tasks run now, in a
// round of the member's cron unit, where every scheduled task runs. It acts
// for the member whose session the context carries, and returns once the
// round was asked for.
type TaskRunner interface {
	RunTask(ctx context.Context, task string) error
}

// task is what the window shows of one of the member's scheduled tasks.
// Fields Hermes keeps for itself (the model, the profile's home, delivery
// targets, claims) are not passed on.
type task struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Prompt   string       `json:"prompt"`
	Schedule taskSchedule `json:"schedule"`
	// State is Hermes's: scheduled, paused, completed (a one-off that ran,
	// or a repeat count used up) or error (the schedule cannot be computed).
	State      string   `json:"state"`
	Running    bool     `json:"running,omitempty"`
	NextRunAt  *float64 `json:"nextRunAt,omitempty"`
	LastRunAt  *float64 `json:"lastRunAt,omitempty"`
	LastStatus string   `json:"lastStatus,omitempty"`
	LastError  string   `json:"lastError,omitempty"`
	CreatedAt  *float64 `json:"createdAt,omitempty"`
	Completed  int      `json:"completed"`
	Times      *int     `json:"times,omitempty"`
}

// taskSchedule is Hermes's parsed schedule: a cron expression, an interval
// in minutes, or one time.
type taskSchedule struct {
	Kind    string   `json:"kind"`
	Expr    string   `json:"expr,omitempty"`
	Minutes int      `json:"minutes,omitempty"`
	RunAt   *float64 `json:"runAt,omitempty"`
	Display string   `json:"display"`
}

type hermesJob struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Schedule struct {
		Kind    string  `json:"kind"`
		Expr    string  `json:"expr"`
		Minutes float64 `json:"minutes"`
		RunAt   *string `json:"run_at"`
		Display string  `json:"display"`
	} `json:"schedule"`
	ScheduleDisplay string  `json:"schedule_display"`
	State           string  `json:"state"`
	NextRunAt       *string `json:"next_run_at"`
	LastRunAt       *string `json:"last_run_at"`
	LastStatus      *string `json:"last_status"`
	LastError       *string `json:"last_error"`
	CreatedAt       *string `json:"created_at"`
	Repeat          *struct {
		Times     *int `json:"times"`
		Completed int  `json:"completed"`
	} `json:"repeat"`
	// FireClaim is set while a run of the job is under way.
	FireClaim *struct {
		At *string `json:"at"`
	} `json:"fire_claim"`
}

func (j hermesJob) view() task {
	t := task{ID: j.ID, Name: j.Name, Prompt: j.Prompt, State: j.State, NextRunAt: hermesTime(j.NextRunAt),
		LastRunAt: hermesTime(j.LastRunAt), CreatedAt: hermesTime(j.CreatedAt)}
	t.Schedule = taskSchedule{Kind: j.Schedule.Kind, Expr: j.Schedule.Expr, Minutes: int(j.Schedule.Minutes),
		RunAt: hermesTime(j.Schedule.RunAt), Display: j.ScheduleDisplay}
	if t.Schedule.Display == "" {
		t.Schedule.Display = j.Schedule.Display
	}
	if j.LastStatus != nil {
		t.LastStatus = *j.LastStatus
	}
	if j.LastError != nil {
		t.LastError = truncateText(*j.LastError, 500)
	}
	if j.Repeat != nil {
		t.Completed, t.Times = j.Repeat.Completed, j.Repeat.Times
	}
	// A claim older than the longest run is one a run left behind when it
	// was stopped; Hermes recovers it at the next run.
	if j.FireClaim != nil {
		if at := hermesTime(j.FireClaim.At); at != nil && time.Since(time.Unix(int64(*at), 0)) < runTimeout {
			t.Running = true
		}
	}
	return t
}

// hermesTime reads one of Hermes's ISO 8601 times as Unix seconds.
func hermesTime(value *string) *float64 {
	parsed, ok := parseHermesTime(value)
	if !ok {
		return nil
	}
	seconds := float64(parsed.UnixMilli()) / 1000
	return &seconds
}

// parseHermesTime reads one of Hermes's ISO 8601 times. Hermes writes them
// with the zone's offset; one without is the device's time.
func parseHermesTime(value *string) (time.Time, bool) {
	if value == nil || *value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if parsed, err := time.ParseInLocation(layout, *value, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request, account string) {
	var jobs []hermesJob
	if !h.taskCall(w, r, account, http.MethodGet, "/api/cron/jobs", &jobs) {
		return
	}
	tasks := make([]task, 0, len(jobs))
	for _, job := range jobs {
		if taskIDPattern.MatchString(job.ID) {
			tasks = append(tasks, job.view())
		}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// taskRun is one run of a task. A run that did not involve the model (a
// task that only runs a script) has no conversation and no id.
type taskRun struct {
	ID           string   `json:"id,omitempty"`
	StartedAt    float64  `json:"startedAt"`
	EndedAt      *float64 `json:"endedAt,omitempty"`
	MessageCount int      `json:"messageCount"`
}

func (h *Handler) taskRuns(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var page struct {
		Runs []struct {
			ID           string   `json:"id"`
			StartedAt    float64  `json:"started_at"`
			EndedAt      *float64 `json:"ended_at"`
			MessageCount int      `json:"message_count"`
		} `json:"runs"`
	}
	query := url.Values{"limit": {fmt.Sprint(taskRunLimit)}}
	if !h.taskCall(w, r, account, http.MethodGet, "/api/cron/jobs/"+id+"/runs?"+query.Encode(), &page) {
		return
	}
	runs := make([]taskRun, 0, len(page.Runs))
	for _, item := range page.Runs {
		run := taskRun{StartedAt: item.StartedAt, EndedAt: item.EndedAt, MessageCount: item.MessageCount}
		if storedIDPattern.MatchString(item.ID) {
			run.ID = item.ID
		}
		runs = append(runs, run)
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"runs": runs})
}

// changeTask pauses or resumes a task.
func (h *Handler) changeTask(action string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, account string) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var job hermesJob
		if h.taskCall(w, r, account, http.MethodPost, "/api/cron/jobs/"+id+"/"+action, &job) {
			h.logger.Info("assistant task changed", "account", account, "task", id, "action", action)
			httpjson.Write(w, http.StatusOK, map[string]any{"task": job.view()})
		}
	}
}

// deleteTask deletes a task with its runs: the window shows a run only under
// its task. Hermes finds a job's runs through the job, so they go first; a run
// that is still being written stays.
func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	deleted := 0
	for tried := map[string]bool{}; ; {
		var page struct {
			Runs []struct {
				ID string `json:"id"`
			} `json:"runs"`
		}
		if !h.taskCall(w, r, account, http.MethodGet, "/api/cron/jobs/"+id+"/runs?limit=100", &page) {
			return
		}
		fresh := 0
		for _, run := range page.Runs {
			if !storedIDPattern.MatchString(run.ID) || tried[run.ID] {
				continue
			}
			tried[run.ID] = true
			fresh++
			if status, _, err := h.writeSession(r.Context(), account, http.MethodDelete, run.ID, nil); err == nil && status < 300 {
				deleted++
			}
		}
		if fresh == 0 {
			break
		}
	}
	if h.taskCall(w, r, account, http.MethodDelete, "/api/cron/jobs/"+id, nil) {
		h.logger.Info("assistant task deleted", "account", account, "task", id, "runs", deleted)
		w.WriteHeader(http.StatusNoContent)
	}
}

// runTask has a task run now, and answers once that was asked for (202): the
// run's end shows in the task and as a notice. The task runs in a round of
// the member's cron unit, like one that is due, and never in the instance:
// asked through Hermes's own trigger route, `hermes serve` starts a gateway
// of its own inside the instance, which stays there, takes over the member's
// due tasks and records its first run as failed (#469). The round queues the
// task with Hermes's own "run on the next tick", which resumes a paused task
// and leaves the run it was waiting for where it was.
func (h *Handler) runTask(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if h.options.Tasks == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "tasks cannot be run from here on this device")
		return
	}
	// The task is one of the member's, and one a round can run: Hermes runs
	// no task whose runs are over, and one run of a task at a time.
	var job hermesJob
	if !h.taskCall(w, r, account, http.MethodGet, "/api/cron/jobs/"+id, &job) {
		return
	}
	switch found := job.view(); {
	case found.Running:
		httpjson.WriteError(w, http.StatusConflict, "task_running", "the task is already running")
		return
	case found.State == "completed" || found.State == "error":
		httpjson.WriteError(w, http.StatusConflict, "task_finished", "the task has no more runs")
		return
	}
	if err := h.options.Tasks.RunTask(r.Context(), id); err != nil {
		h.unavailable(w, account, err)
		return
	}
	h.logger.Info("assistant task asked to run now", "account", account, "task", id)
	httpjson.Write(w, http.StatusAccepted, map[string]any{"finished": false})
}

// taskCall is call with a task's errors: a missing task, or one already
// running.
func (h *Handler) taskCall(w http.ResponseWriter, r *http.Request, account, method, path string, into any) bool {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	status, data, err := h.rest(ctx, account, method, path, nil)
	if err != nil {
		h.unavailable(w, account, err)
		return false
	}
	if status >= 300 {
		h.relayTaskError(w, status, data)
		return false
	}
	if into == nil {
		return true
	}
	if err := json.Unmarshal(data, into); err != nil {
		h.logger.Warn("assistant reply undecodable", "account", account, "error", err)
		httpjson.WriteError(w, http.StatusBadGateway, "assistant_unavailable", "the assistant answered unexpectedly")
		return false
	}
	return true
}

func (h *Handler) relayTaskError(w http.ResponseWriter, status int, detail []byte) {
	switch status {
	case http.StatusNotFound:
		httpjson.WriteError(w, http.StatusNotFound, "task_not_found", "no such task")
	case http.StatusConflict:
		httpjson.WriteError(w, http.StatusConflict, "task_running", "the task is already running")
	default:
		h.relayError(w, status, detail)
	}
}

func taskID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !taskIDPattern.MatchString(id) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_task", "invalid task id")
		return "", false
	}
	return id, true
}

// truncateText keeps the first limit runes of text.
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
