package rootbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// rounds stands in for systemd: the rounds the broker asked to start.
type rounds struct {
	mu      sync.Mutex
	started []string
	fail    error
}

func (r *rounds) start(_ context.Context, username string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.started = append(r.started, username)
	return nil
}

func (r *rounds) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func runHarness(t *testing.T, asked *rounds) (*assistantHarness, string) {
	t.Helper()
	runs := filepath.Join(t.TempDir(), "assistant-runs")
	h := newAssistantHarness(t, func(config *Config) {
		config.AssistantRuns, config.StartAssistantRound = runs, asked.start
	})
	return h, runs
}

func waiting(t *testing.T, runs, username string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runs, username))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestATaskAskedToRunNowWaitsForARoundOfItsMembersTasks(t *testing.T) {
	asked := &rounds{}
	h, runs := runHarness(t, asked)
	member := accountid.WithSessionToken(context.Background(), "member-token")
	if err := h.client.RunAssistantTask(member, "a1b2c3d4e5f6"); err != nil {
		t.Fatalf("RunAssistantTask() error = %v", err)
	}
	// The request is the member's own, whoever the caller says it is for,
	// and nobody but root may see what waits.
	if got := waiting(t, runs, "alice"); len(got) != 1 || got[0] != "a1b2c3d4e5f6" {
		t.Fatalf("waiting for alice = %v", got)
	}
	if got := asked.asked(); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("rounds started = %v", got)
	}
	for _, path := range []string{runs, filepath.Join(runs, "alice")} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v, want a directory of root's alone", path, info, err)
		}
	}
	// Asked again before a round took it, it waits once.
	if err := h.client.RunAssistantTask(member, "a1b2c3d4e5f6"); err != nil {
		t.Fatalf("a second RunAssistantTask() error = %v", err)
	}
	if got := waiting(t, runs, "alice"); len(got) != 1 {
		t.Fatalf("waiting after asking twice = %v", got)
	}
	// A round took the requests, as run-requests.sh does: the next one waits
	// for the round after.
	if err := os.Rename(filepath.Join(runs, "alice"), filepath.Join(runs, "alice.taken")); err != nil {
		t.Fatal(err)
	}
	if err := h.client.RunAssistantTask(member, "0123456789ab"); err != nil {
		t.Fatalf("RunAssistantTask() after a round error = %v", err)
	}
	if got := waiting(t, runs, "alice"); len(got) != 1 || got[0] != "0123456789ab" {
		t.Fatalf("waiting after a round = %v", got)
	}
}

func TestOnlyALiveSessionsOwnTasksAreAskedFor(t *testing.T) {
	asked := &rounds{}
	h, runs := runHarness(t, asked)
	if err := h.client.RunAssistantTask(accountid.WithSessionToken(context.Background(), "forged"), "a1b2c3d4e5f6"); !errors.Is(err, filesystem.ErrForbidden) {
		t.Fatalf("forged RunAssistantTask() error = %v, want ErrForbidden", err)
	}
	// What names a task is Hermes's own id for it: never a path, never a
	// job's name.
	member := accountid.WithSessionToken(context.Background(), "member-token")
	for _, task := range []string{"", "../bob/a1b2c3d4e5f6", "A1B2C3D4E5F6", "a1b2c3d4e5f", "每日小结"} {
		var refused *wire.Error
		if err := h.client.RunAssistantTask(member, task); !errors.As(err, &refused) || refused.Code != wire.CodeInvalidRequest {
			t.Fatalf("RunAssistantTask(%q) error = %v, want an invalid request", task, err)
		}
	}
	if entries, err := os.ReadDir(runs); err == nil && len(entries) > 0 {
		t.Fatalf("something waits after refusals: %v", entries)
	}
	if got := asked.asked(); len(got) != 0 {
		t.Fatalf("rounds started = %v", got)
	}
}

func TestATaskNoRoundCanRunIsNotLeftWaiting(t *testing.T) {
	asked := &rounds{fail: errors.New("Unit anas-assistant-cron@alice.service is masked.")}
	h, runs := runHarness(t, asked)
	member := accountid.WithSessionToken(context.Background(), "member-token")
	if err := h.client.RunAssistantTask(member, "a1b2c3d4e5f6"); !errors.Is(err, wire.ErrUnavailable) {
		t.Fatalf("RunAssistantTask() error = %v, want ErrUnavailable", err)
	}
	if got := waiting(t, runs, "alice"); len(got) != 0 {
		t.Fatalf("waiting although no round starts = %v", got)
	}
}

func TestTheTasksWaitingForOneMemberAreBounded(t *testing.T) {
	asked := &rounds{}
	h, runs := runHarness(t, asked)
	member := accountid.WithSessionToken(context.Background(), "member-token")
	if err := os.MkdirAll(filepath.Join(runs, "alice"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxAssistantRuns {
		name := filepath.Join(runs, "alice", "0000000000"+string(rune('a'+i/6))+string(rune('a'+i%6)))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.client.RunAssistantTask(member, "a1b2c3d4e5f6"); !errors.Is(err, wire.ErrUnavailable) {
		t.Fatalf("RunAssistantTask() over the bound error = %v, want ErrUnavailable", err)
	}
	if got := asked.asked(); len(got) != 0 {
		t.Fatalf("rounds started = %v", got)
	}
}

func TestRunningATaskNowIsOffWithoutItsDirectory(t *testing.T) {
	asked := &rounds{}
	h := newAssistantHarness(t, func(config *Config) { config.StartAssistantRound = asked.start })
	member := accountid.WithSessionToken(context.Background(), "member-token")
	if err := h.client.RunAssistantTask(member, "a1b2c3d4e5f6"); !errors.Is(err, wire.ErrUnavailable) {
		t.Fatalf("RunAssistantTask() error = %v, want ErrUnavailable", err)
	}
	if got := asked.asked(); len(got) != 0 {
		t.Fatalf("rounds started = %v", got)
	}
}
