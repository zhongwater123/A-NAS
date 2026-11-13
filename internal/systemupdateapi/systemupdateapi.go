// Package systemupdateapi serves Settings → 系统更新 (docs/specs/system-update.md)
// to administrators: the state of the system update service, which runs as
// root apart from the Product Service; the requests to check, download,
// install and go back; and the settings. It also installs by itself in the
// window the settings name, and writes the service's outcomes to the
// activity record.
package systemupdateapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
)

// Recorder writes to the activity record (accounts.Service.RecordSystemUpdate).
type Recorder interface {
	RecordSystemUpdate(ctx context.Context, actorID, action, resourceID, detail string) error
}

// Work names what an install waits for: "storage" while the data volume
// converts or a storage plan runs, "apps" while an app installs or
// uninstalls. Restarting the services would cut them off.
type Work func(ctx context.Context) []string

// API serves the system update; a nil updater answers update_unavailable.
type API struct {
	updater  systemupdate.Updater
	work     Work
	recorder Recorder
	logger   *slog.Logger
	now      func() time.Time
}

func New(updater systemupdate.Updater, work Work, recorder Recorder, logger *slog.Logger) *API {
	if work == nil {
		work = func(context.Context) []string { return nil }
	}
	return &API{updater: updater, work: work, recorder: recorder, logger: logger, now: time.Now}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/system/update", Posture: apiroute.Admin, Handler: a.ready(a.status)},
		{Method: http.MethodPost, Pattern: "/api/v1/system/update/actions", Posture: apiroute.Admin, Handler: a.ready(a.act)},
		{Method: http.MethodPut, Pattern: "/api/v1/system/update/settings", Posture: apiroute.Admin, Handler: a.ready(a.setSettings)},
	}
}

func (a *API) ready(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if a.updater == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "update_unavailable", "system update is unavailable")
			return
		}
		next(w, r, session)
	}
}

func (a *API) status(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	status, err := a.updater.SystemUpdate(r.Context())
	if err != nil {
		a.unavailable(w, r, err)
		return
	}
	a.mirror(r.Context(), status)
	httpjson.Write(w, http.StatusOK, a.document(r.Context(), status))
}

type actionRequest struct {
	Action string `json:"action"`
}

func (a *API) act(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := localorigin.CheckWrite(r); err != nil {
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "system updates must come from the local desktop")
		return
	}
	var request actionRequest
	if !httpjson.Read(w, r, 256, &request) {
		return
	}
	if !systemupdate.Requestable(request.Action) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "the action is check, download, install or revert")
		return
	}
	status, err := a.updater.SystemUpdate(r.Context())
	if err != nil {
		a.unavailable(w, r, err)
		return
	}
	if status.Running {
		httpjson.WriteError(w, http.StatusConflict, "update_busy", "a system update operation is running")
		return
	}
	if !actionReady(request.Action, status) {
		httpjson.WriteError(w, http.StatusConflict, "update_not_ready", "there is nothing to "+request.Action)
		return
	}
	if request.Action == systemupdate.RequestInstall || request.Action == systemupdate.RequestRevert {
		if waiting := a.work(r.Context()); len(waiting) > 0 {
			httpjson.WriteError(w, http.StatusConflict, "update_waiting", "an install waits for "+waiting[0])
			return
		}
	}
	status, err = a.updater.RequestSystemUpdate(r.Context(),
		systemupdate.Request{Kind: request.Action, By: session.User.Username})
	if errors.Is(err, systemupdate.ErrBusy) {
		httpjson.WriteError(w, http.StatusConflict, "update_busy", "a system update operation is running")
		return
	}
	if err != nil {
		a.unavailable(w, r, err)
		return
	}
	version := status.Candidate
	if request.Action == systemupdate.RequestRevert {
		version = status.Previous
	}
	// A check is not recorded: the page asks for one whenever the last is an
	// hour old, and what a check finds is recorded when it fails.
	if request.Action != systemupdate.RequestCheck {
		a.record(r.Context(), session.User.ID, "system.update."+request.Action, "", map[string]string{"version": version})
	}
	a.logger.InfoContext(r.Context(), "system update requested", "by", session.User.Username, "action", request.Action)
	httpjson.Write(w, http.StatusAccepted, a.document(r.Context(), status))
}

// actionReady reports whether the state has something the action acts on.
func actionReady(action string, status systemupdate.Status) bool {
	switch action {
	case systemupdate.RequestDownload:
		return status.Phase == systemupdate.PhaseAvailable
	case systemupdate.RequestInstall:
		return status.Phase == systemupdate.PhaseReady
	case systemupdate.RequestRevert:
		return status.Previous != ""
	}
	return true
}

func (a *API) setSettings(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := localorigin.CheckWrite(r); err != nil {
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "system updates must come from the local desktop")
		return
	}
	var settings systemupdate.Settings
	if !httpjson.Read(w, r, 512, &settings) {
		return
	}
	if err := settings.Validate(); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status, err := a.updater.SetSystemUpdateSettings(r.Context(), settings)
	if err != nil {
		a.unavailable(w, r, err)
		return
	}
	a.record(r.Context(), session.User.ID, "system.update.settings", "", settings)
	httpjson.Write(w, http.StatusOK, a.document(r.Context(), status))
}

func (a *API) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.ErrorContext(r.Context(), "system update unavailable", "error", err)
	httpjson.WriteError(w, http.StatusServiceUnavailable, "update_unavailable", "system update is unavailable")
}

// record writes one event to the activity record; a failure is logged, the
// request itself has happened.
func (a *API) record(ctx context.Context, actorID, action, resourceID string, detail any) {
	if a.recorder == nil {
		return
	}
	data, err := json.Marshal(detail)
	if err == nil {
		err = a.recorder.RecordSystemUpdate(ctx, actorID, action, resourceID, string(data))
	}
	if err != nil {
		a.logger.ErrorContext(ctx, "system update not written to the activity record", "action", action, "error", err)
	}
}

// systemActor is the actor the activity log shows as 系统 and filters as 系统任务
// (internal/activityapi); who started an operation is in the detail.
const systemActor = "system"

// mirror writes the outcomes the service recorded to the activity record;
// each record's time names it, so writing it again changes nothing.
func (a *API) mirror(ctx context.Context, status systemupdate.Status) {
	for _, record := range slices.Backward(status.Records) {
		detail := map[string]string{"version": record.Version, "from": record.From, "by": record.By}
		if record.Problem != nil {
			detail["problem"] = record.Problem.Code
		}
		a.record(ctx, systemActor, "system.update.result."+record.Kind,
			record.At.UTC().Format(time.RFC3339Nano)+" "+record.Kind, detail)
	}
}

// Run installs by itself while the settings allow it: every few minutes,
// within the window, a downloaded version is installed once nothing waits
// on the services. It also writes the outcomes to the activity record. It
// returns when ctx ends.
func (a *API) Run(ctx context.Context, every time.Duration) {
	if a.updater == nil {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.automatic(ctx)
	}
}

func (a *API) automatic(ctx context.Context) {
	status, err := a.updater.SystemUpdate(ctx)
	if err != nil {
		return
	}
	a.mirror(ctx, status)
	if status.Running || status.Phase != systemupdate.PhaseReady || !status.Settings.AutoInstall ||
		!status.Settings.InWindow(a.now()) || len(a.work(ctx)) > 0 {
		return
	}
	request := systemupdate.Request{Kind: systemupdate.RequestInstall, By: systemupdate.ByAutomatic}
	if _, err := a.updater.RequestSystemUpdate(ctx, request); err != nil {
		a.logger.ErrorContext(ctx, "automatic install not started", "error", err)
		return
	}
	a.record(ctx, "", "system.update.install", "", map[string]string{"version": status.Candidate, "by": systemupdate.ByAutomatic})
	a.logger.InfoContext(ctx, "automatic install started", "version", status.Candidate)
}

// Document is the page's view of the service.
type Document struct {
	Phase           systemupdate.Phase    `json:"phase"`
	Running         bool                  `json:"running"`
	Current         string                `json:"current,omitempty"`
	Previous        string                `json:"previous,omitempty"`
	Candidate       string                `json:"candidate,omitempty"`
	DownloadBytes   int64                 `json:"downloadBytes"`
	DownloadedBytes int64                 `json:"downloadedBytes"`
	Suites          []string              `json:"suites"`
	CheckedAt       *time.Time            `json:"checkedAt,omitempty"`
	InstalledAt     *time.Time            `json:"installedAt,omitempty"`
	Target          string                `json:"target,omitempty"`
	StartedAt       *time.Time            `json:"startedAt,omitempty"`
	Problem         *systemupdate.Problem `json:"problem,omitempty"`
	Records         []systemupdate.Record `json:"records"`
	Settings        systemupdate.Settings `json:"settings"`
	// Waiting names what an install waits for now.
	Waiting []string `json:"waiting"`
}

func (a *API) document(ctx context.Context, status systemupdate.Status) Document {
	document := Document{
		Phase: status.Phase, Running: status.Running, Current: status.Current, Previous: status.Previous,
		Candidate: status.Candidate, DownloadBytes: status.DownloadBytes, DownloadedBytes: status.DownloadedBytes,
		Suites: status.Suites, CheckedAt: timePointer(status.CheckedAt), InstalledAt: timePointer(status.InstalledAt),
		Target: status.Target, Problem: status.Problem, Records: status.Records,
		Settings: status.Settings, Waiting: a.work(ctx),
	}
	if status.Running || status.Phase.Busy() {
		document.StartedAt = timePointer(status.StartedAt)
	}
	if document.Suites == nil {
		document.Suites = []string{}
	}
	if document.Records == nil {
		document.Records = []systemupdate.Record{}
	}
	if document.Waiting == nil {
		document.Waiting = []string{}
	}
	return document
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
