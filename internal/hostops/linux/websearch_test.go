package linux_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/websearch"
)

// systemdRunner answers systemctl show with the unit's state and records
// the other commands.
type systemdRunner struct {
	activeState string
	commands    []string
}

func (r *systemdRunner) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

func (r *systemdRunner) Output(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte(r.activeState + "\n"), nil
	}
	return r.Run(context.Background(), name, args, "")
}

func TestTheWebSearchSwitchIsAFileTheUnitWaitsOn(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var", "lib", "a-nas-host"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "var", "lib", "a-nas-host", "web-search-disabled")
	runner := &systemdRunner{activeState: "active"}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	ctx := context.Background()

	state, err := executor.WebSearch(ctx)
	if err != nil || state != (websearch.ServiceState{Enabled: true, Running: websearch.StateRunning}) {
		t.Fatalf("state = %+v, %v; want on and running", state, err)
	}
	runner.activeState = "inactive"
	state, err = executor.SetWebSearch(ctx, false)
	if err != nil || state != (websearch.ServiceState{Enabled: false, Running: websearch.StateStopped}) {
		t.Fatalf("after turning off = %+v, %v", state, err)
	}
	if info, err := os.Stat(marker); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker = %v, %v", info, err)
	}
	runner.activeState = "activating"
	state, err = executor.SetWebSearch(ctx, true)
	if err != nil || state != (websearch.ServiceState{Enabled: true, Running: websearch.StateStarting}) {
		t.Fatalf("after turning on = %+v, %v", state, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker after turning on: %v", err)
	}
	// Turning on twice is not an error.
	if _, err := executor.SetWebSearch(ctx, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl stop --no-block anas-search.service", "systemctl start --no-block anas-search.service",
		"systemctl start --no-block anas-search.service"}
	if strings.Join(runner.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", runner.commands, want)
	}
}
