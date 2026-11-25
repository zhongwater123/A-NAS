package systemupdate

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Memory is a system update service in memory, for the Product Service in
// development (ANAS_HOSTSTATE_MODE=fake): a newer version waits to be
// installed, and each operation takes a few seconds, so the page shows its
// states. An install of a version ending in "+broken" fails its check and
// goes back.
type Memory struct {
	mu    sync.Mutex
	state State
	until time.Time
	delay time.Duration
	// finish completes the operation in progress once until has passed.
	finish func(*State)
}

// NewMemory starts with current installed and candidate ready to install.
// With image, the device runs the A-NAS image: the candidate brings a new
// base, which waits for a restart once installed.
func NewMemory(current, candidate string, delay time.Duration, image bool) *Memory {
	now := time.Now()
	m := &Memory{delay: delay, state: State{
		Phase: PhaseReady, Current: current, Candidate: candidate, Previous: "", Suites: []string{"release", "dev"},
		CheckedAt: now.Add(-20 * time.Minute), InstalledAt: now.Add(-49 * time.Hour), Settings: DefaultSettings(),
		Records: []Record{{At: now.Add(-49 * time.Hour), Kind: RecordUpdated, Version: current, By: "owner"}},
	}}
	if image {
		m.state.Suites = nil
		m.state.Image = &Image{Base: "1001", Product: "1001", Candidate: "1002", Restart: true}
	}
	return m
}

// broken reports whether a version is one the memory service fails.
func broken(version string) bool { return strings.HasSuffix(version, "+broken") }

// settle completes an operation whose time has come.
func (m *Memory) settle() {
	if m.finish != nil && !time.Now().Before(m.until) {
		m.finish(&m.state)
		m.finish = nil
	}
}

func (m *Memory) SystemUpdate(context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settle()
	return Status{State: m.state, Running: m.finish != nil}, nil
}

func (m *Memory) RequestSystemUpdate(_ context.Context, request Request) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settle()
	if m.finish != nil {
		return Status{}, ErrBusy
	}
	now := time.Now()
	m.until = now.Add(m.delay)
	state := &m.state
	switch request.Kind {
	case RequestCheck:
		m.finish = func(s *State) { s.CheckedAt = time.Now() }
	case RequestDownload:
		state.Phase = PhaseDownloading
		m.finish = func(s *State) { s.Phase, s.DownloadedBytes = PhaseReady, s.DownloadBytes }
	case RequestInstall:
		state.Phase, state.Target, state.From, state.By, state.StartedAt = PhaseInstalling, state.Candidate, state.Current, request.By, now
		if state.Image != nil && state.Image.Restart {
			m.finish = func(s *State) { s.Phase = PhaseRestartPending }
			break
		}
		m.finish = func(s *State) {
			if broken(s.Target) {
				s.Reject(s.Target)
				s.Problem = &Problem{Code: ProblemCheck, Detail: "FAIL anas-api.service is failed; see journalctl -u anas-api.service"}
				s.Record(Record{At: time.Now(), Kind: RecordRolledBack, Version: s.Target, From: s.From, By: s.By, Problem: s.Problem})
				s.Phase = PhaseRolledBack
			} else {
				s.Record(Record{At: time.Now(), Kind: RecordUpdated, Version: s.Target, From: s.From, By: s.By})
				s.Previous, s.Current, s.InstalledAt, s.Phase, s.Problem = s.From, s.Target, time.Now(), PhaseUpToDate, nil
			}
			s.Candidate, s.Target, s.From, s.By = "", "", "", ""
		}
	case RequestRestart:
		if state.Phase != PhaseRestartPending {
			m.finish = nil
			break
		}
		// The device restarts; the page loses the service meanwhile.
		m.finish = func(s *State) {
			if broken(s.Target) {
				s.Reject(s.Target)
				s.Problem = &Problem{Code: ProblemBoot, Detail: "the base 1002 did not pass its boot check; systemd-boot went back to 1001"}
				s.Record(Record{At: time.Now(), Kind: RecordRolledBack, Version: s.Target, From: s.From, By: s.By, Problem: s.Problem})
				s.Phase = PhaseRolledBack
			} else {
				s.Record(Record{At: time.Now(), Kind: RecordUpdated, Version: s.Target, From: s.From, By: s.By})
				s.Previous, s.Current, s.InstalledAt, s.Phase, s.Problem = s.From, s.Target, time.Now(), PhaseUpToDate, nil
				s.Image.Base, s.Image.Product = s.Image.Candidate, s.Image.Candidate
			}
			s.Image.Candidate, s.Image.Restart = "", false
			s.Candidate, s.Target, s.From, s.By = "", "", "", ""
		}
	case RequestRevert:
		state.Phase, state.Target, state.From, state.By, state.StartedAt = PhaseRollingBack, state.Previous, state.Current, request.By, now
		m.finish = func(s *State) {
			s.Reject(s.From)
			s.Record(Record{At: time.Now(), Kind: RecordReverted, Version: s.Target, From: s.From, By: s.By})
			s.Current, s.Previous, s.InstalledAt, s.Phase = s.Target, "", time.Now(), PhaseUpToDate
			s.Target, s.From, s.By = "", "", ""
		}
	}
	return Status{State: m.state, Running: m.finish != nil}, nil
}

func (m *Memory) SetSystemUpdateSettings(_ context.Context, settings Settings) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settle()
	m.state.Settings = settings
	return Status{State: m.state, Running: m.finish != nil}, nil
}

var _ Updater = (*Memory)(nil)
