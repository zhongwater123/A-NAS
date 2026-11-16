// Package systemupdate is the contract of the system update service
// (docs/specs/system-update.md, ADR 0034 step 4) that the Product Service and
// the root Host Agent share. The service itself, in
// internal/systemupdate/updater, runs as root in anas-update.service, apart
// from the services an install restarts: it takes the repository a
// development machine pushed into the staging directory into apt's local
// source, checks it for a newer A-NAS, downloads and installs it, runs
// a-nas-check, and goes back to the version before when the check fails. The
// Host Agent starts it with a request and reads the state it leaves; the
// Product Service shows that state in Settings. This package depends on
// nothing of either.
package systemupdate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Phase is where an update stands.
type Phase string

const (
	// PhaseUpToDate: the installed version is the newest the source has.
	PhaseUpToDate Phase = "up-to-date"
	// PhaseAvailable: a newer version is in the source, not yet downloaded.
	PhaseAvailable Phase = "available"
	// PhaseDownloading: its packages are being downloaded.
	PhaseDownloading Phase = "downloading"
	// PhaseReady: everything it needs is on the device; it can be installed.
	PhaseReady Phase = "ready"
	// PhaseInstalling: apt is installing it, or a-nas-check is checking it.
	PhaseInstalling Phase = "installing"
	// PhaseRollingBack: the check failed, or the administrator asked for
	// the version before; apt is installing that one.
	PhaseRollingBack Phase = "rolling-back"
	// PhaseRolledBack: the check of the new version failed and the device
	// is back on the version before it.
	PhaseRolledBack Phase = "rolled-back"
	// PhaseCheckFailed: the source could not be read, or its signature was
	// wrong.
	PhaseCheckFailed Phase = "check-failed"
)

// Busy reports whether an operation is running in this phase.
func (p Phase) Busy() bool {
	return p == PhaseDownloading || p == PhaseInstalling || p == PhaseRollingBack
}

// Problem says why the last check or operation failed: a stable code the
// page words, and the detail behind it.
type Problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// The codes of Problem.
const (
	ProblemUnreachable  = "unreachable"
	ProblemSignature    = "signature"
	ProblemDiskSpace    = "disk_space"
	ProblemDownload     = "download"
	ProblemInstall      = "install"
	ProblemCheck        = "check"
	ProblemRollback     = "rollback"
	ProblemStaging      = "staging"
	ProblemNotInstalled = "not_installed"
)

// Settings are the administrator's choices (docs/decisions/2026-11-12-system-update-defaults.md).
type Settings struct {
	AutoDownload bool `json:"autoDownload"`
	AutoInstall  bool `json:"autoInstall"`
	// WindowStart and WindowEnd are the local time of day, HH:MM, between
	// which an automatic install may start.
	WindowStart string `json:"windowStart"`
	WindowEnd   string `json:"windowEnd"`
}

// DefaultSettings: download by itself, install when an administrator says so.
func DefaultSettings() Settings {
	return Settings{AutoDownload: true, WindowStart: "03:00", WindowEnd: "05:00"}
}

// Validate refuses a window that is not two HH:MM times of day.
func (s Settings) Validate() error {
	for _, value := range []string{s.WindowStart, s.WindowEnd} {
		if _, err := time.Parse("15:04", value); err != nil || len(value) != 5 {
			return fmt.Errorf("the install window takes HH:MM times, not %q", value)
		}
	}
	if s.WindowStart == s.WindowEnd {
		return errors.New("the install window must not be empty")
	}
	return nil
}

// InWindow reports whether now, in its own location, falls in the window,
// which may run past midnight.
func (s Settings) InWindow(now time.Time) bool {
	start, err1 := time.Parse("15:04", s.WindowStart)
	end, err2 := time.Parse("15:04", s.WindowEnd)
	if err1 != nil || err2 != nil {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	from, to := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if from < to {
		return minute >= from && minute < to
	}
	return minute >= from || minute < to
}

// Record is one line of the page's history.
type Record struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Version string    `json:"version,omitempty"`
	From    string    `json:"from,omitempty"`
	By      string    `json:"by"`
	Problem *Problem  `json:"problem,omitempty"`
}

// The kinds of Record.
const (
	RecordUpdated     = "updated"
	RecordRolledBack  = "rolled-back"
	RecordReverted    = "reverted"
	RecordCheckFailed = "check-failed"
	RecordDownloaded  = "downloaded"
	RecordFailed      = "failed"
)

// ByAutomatic names the service itself as the one who acted.
const ByAutomatic = "automatic"

// keptRecords is how many records the page shows.
const keptRecords = 10

// State is what the service leaves in its state directory.
type State struct {
	Phase Phase `json:"phase"`
	// Current is the installed version of a-nas; Previous the one before
	// the last install that passed its check, which "back to the version
	// before" installs.
	Current  string `json:"current,omitempty"`
	Previous string `json:"previous,omitempty"`
	// Candidate is the newest version the source offers that is newer than
	// Current and was not rolled back.
	Candidate string `json:"candidate,omitempty"`
	// DownloadBytes is what installing Candidate still has to download; 0
	// for the local source that a push fills.
	DownloadBytes int64 `json:"downloadBytes"`
	// DownloadedBytes is how far a download has come.
	DownloadedBytes int64 `json:"downloadedBytes"`
	// Suites are the distributions the device follows, such as release
	// and dev.
	Suites    []string  `json:"suites,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// InstalledAt is when Current was installed through this service.
	InstalledAt time.Time `json:"installedAt"`
	// Target is the version an operation in progress installs, From the
	// version it started from, By who asked for it.
	Target    string    `json:"target,omitempty"`
	From      string    `json:"from,omitempty"`
	By        string    `json:"by,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// BackupName is the database copy the a-nas package's preinst made
	// before the install in progress, <time>-<From>.
	BackupName string   `json:"backupName,omitempty"`
	Problem    *Problem `json:"problem,omitempty"`
	// Rejected are versions that failed their check or that an
	// administrator went back from: never installed automatically again.
	Rejected []string `json:"rejected,omitempty"`
	Records  []Record `json:"records,omitempty"`
	Settings Settings `json:"settings"`
}

// Record adds a line to the history, newest first, keeping the last ten.
func (s *State) Record(record Record) {
	s.Records = append([]Record{record}, s.Records...)
	if len(s.Records) > keptRecords {
		s.Records = s.Records[:keptRecords]
	}
}

// Reject marks a version as one never to install automatically.
func (s *State) Reject(version string) {
	if version != "" && !slices.Contains(s.Rejected, version) {
		s.Rejected = append(s.Rejected, version)
	}
}

// Request is what an administrator, or the Product Service's automatic
// install, asks the service to do.
type Request struct {
	Kind string `json:"kind"`
	// By is the administrator who asked, or ByAutomatic.
	By string `json:"by"`
}

// The kinds of Request.
const (
	// RequestTick is the hourly timer: take a push, check when due,
	// download when the settings say so.
	RequestTick = "tick"
	// RequestCheck takes a push and checks now.
	RequestCheck = "check"
	// RequestDownload downloads the candidate.
	RequestDownload = "download"
	// RequestInstall installs the candidate and checks it.
	RequestInstall = "install"
	// RequestRevert goes back to the version before.
	RequestRevert = "revert"
	// RequestBoot finishes an install that a power cut or reboot stopped.
	RequestBoot = "boot"
)

// Requestable reports whether the Product Service may ask for kind.
func Requestable(kind string) bool {
	return kind == RequestCheck || kind == RequestDownload || kind == RequestInstall || kind == RequestRevert
}

// Status is the state the service left and whether it runs now.
type Status struct {
	State
	Running bool `json:"running"`
}

// ErrBusy refuses a request while the service runs.
var ErrBusy = errors.New("a system update operation is running")

// Updater is the Host Agent's side, which the Product Service calls.
type Updater interface {
	SystemUpdate(ctx context.Context) (Status, error)
	RequestSystemUpdate(ctx context.Context, request Request) (Status, error)
	SetSystemUpdateSettings(ctx context.Context, settings Settings) (Status, error)
}
