package systemupdate

import (
	"context"
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
func NewMemory(current, candidate string, delay time.Duration) *Memory {
	now := time.Now()
	return &Memory{delay: delay, state: State{
		Phase: PhaseReady, Current: current, Candidate: candidate, Previous: "", Suites: []string{"release", "dev"},
		CheckedAt: now.Add(-20 * time.Minute), InstalledAt: now.Add(-49 * time.Hour), Settings: DefaultSettings(),
		Records: []Record{{At: now.Add(-49 * time.Hour), Kind: RecordUpdated, Version: current, By: "owner"}},
	}}
}

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
		m.finish = func(s *State) {
			if len(s.Target) > 7 && s.Target[len(s.Target)-7:] == "+broken" {
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
