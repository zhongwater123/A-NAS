package linux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
	"github.com/zhongwater123/A-NAS/internal/systemupdate/updater"
)

func TestTheSystemUpdateIsARequestFileAndAUnitStart(t *testing.T) {
	root := t.TempDir()
	runner := &systemdRunner{activeState: "inactive"}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	store := updater.Store{Dir: filepath.Join(root, "var", "lib", "a-nas-update")}
	ctx := context.Background()

	status, err := executor.SystemUpdate(ctx)
	if err != nil || status.Running || status.Phase != systemupdate.PhaseUpToDate || status.Settings != systemupdate.DefaultSettings() {
		t.Fatalf("a device that never ran an update = %+v, %v", status, err)
	}
	status, err = executor.RequestSystemUpdate(ctx, systemupdate.Request{Kind: systemupdate.RequestInstall, By: "owner"})
	if err != nil || !status.Running {
		t.Fatalf("RequestSystemUpdate() = %+v, %v", status, err)
	}
	request, err := store.TakeRequest()
	if err != nil || request != (systemupdate.Request{Kind: systemupdate.RequestInstall, By: "owner"}) {
		t.Fatalf("the request left for the service = %+v, %v", request, err)
	}
	if !slices.Contains(runner.commands, "systemctl start --no-block anas-update.service") {
		t.Fatalf("the service was not started: %v", runner.commands)
	}
	info, err := os.Stat(store.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the state directory = %v, %v; want root's alone", info, err)
	}

	runner.activeState = "activating"
	if _, err := executor.RequestSystemUpdate(ctx, systemupdate.Request{Kind: systemupdate.RequestCheck, By: "owner"}); !errors.Is(err, systemupdate.ErrBusy) {
		t.Fatalf("a request while the service runs = %v, want ErrBusy", err)
	}

	// A run that stopped in the middle of an install is finished by
	// starting the service again.
	runner.activeState, runner.commands = "inactive", nil
	if err := store.Save(systemupdate.State{Phase: systemupdate.PhaseInstalling, Target: "2", From: "1"}); err != nil {
		t.Fatal(err)
	}
	status, err = executor.SystemUpdate(ctx)
	if err != nil || !status.Running || !slices.Contains(runner.commands, "systemctl start --no-block anas-update.service") {
		t.Fatalf("a stopped install = %+v, %v; commands %v", status, err, runner.commands)
	}

	settings := systemupdate.Settings{AutoInstall: true, WindowStart: "02:00", WindowEnd: "04:30"}
	status, err = executor.SetSystemUpdateSettings(ctx, settings)
	if err != nil || status.Settings != settings {
		t.Fatalf("SetSystemUpdateSettings() = %+v, %v", status, err)
	}
	if _, err := executor.SetSystemUpdateSettings(ctx, systemupdate.Settings{WindowStart: "02:00", WindowEnd: "02:00"}); err == nil {
		t.Fatal("an empty install window was saved")
	}
}
