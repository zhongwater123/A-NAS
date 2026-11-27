package linux

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
)

type activeHotspotRunner struct{ CommandRunner }

func (activeHotspotRunner) Output(context.Context, string, []string, string) ([]byte, error) {
	return []byte("active\n"), nil
}

type failedHotspotRunner struct{ CommandRunner }

func (failedHotspotRunner) Output(context.Context, string, []string, string) ([]byte, error) {
	return []byte("failed\n"), nil
}

func (failedHotspotRunner) Run(context.Context, string, []string, string) ([]byte, error) {
	return nil, nil
}

func TestPhoneHotspotReadsPythonConnectionFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Python worker uses Unix system accounts")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "run", "anas-phone-hotspot")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executor := &Executor{runner: activeHotspotRunner{}, systemRoot: root, stateOwner: os.Geteuid()}
	name, password, portal := "访客 Wi-Fi", "fixture # only", true
	settings, err := executor.SavePhoneHotspotSettings(t.Context(), phonehotspot.SettingsChange{
		SSID: &name, Password: &password, Portal: &portal,
	})
	if err != nil || settings != (phonehotspot.Settings{SSID: name, PasswordConfigured: true, Portal: true}) {
		t.Fatalf("save hotspot settings: %+v, %v", settings, err)
	}
	profile := executor.hotspotProfilePath()
	if info, err := os.Stat(profile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("the hotspot profile must be accessible to root alone")
	}
	worker := os.Getenv("ANAS_PHONE_HOTSPOT_TEST_WORKER")
	if worker == "" {
		worker = filepath.Join("..", "..", "..", "deploy", "network", "phone-hotspot.py")
	}
	produce := `
import runpy
import sys
from pathlib import Path
worker = runpy.run_path(sys.argv[1])
assert worker["PROFILE"] == Path(sys.argv[4]), "Go and Python use different installed settings paths"
publish = worker["publish_connection"]
publish.__globals__["RUNTIME"] = Path(sys.argv[2])
publish.__globals__["PROFILE"] = Path(sys.argv[3])
publish("fixture.example.test", *worker["hotspot_profile"]("02:00:00:00:00:01"))
stations = "02:00:00:00:00:02\nflags=[AUTH][ASSOC][AUTHORIZED]\n"
leases = "0 02:00:00:00:00:02 192.168.173.20 fixture-phone *\n"
worker["write"]("devices.json", worker["json"].dumps(worker["connected_devices"](stations, leases, 0)))
`
	installedPath := (&Executor{systemRoot: "/"}).hotspotProfilePath()
	if output, err := exec.CommandContext(t.Context(), "python3", "-B", "-c", produce, worker, directory, profile, installedPath).CombinedOutput(); err != nil {
		t.Fatalf("produce the connection document: %v\n%s", err, output)
	}
	state, err := executor.PhoneHotspot(t.Context())
	want := phonehotspot.Connection{SSID: name, Password: password, URL: "https://fixture.example.test:8443/", Portal: true}
	if err != nil || state.State != phonehotspot.On || state.Connection == nil || *state.Connection != want {
		t.Fatalf("the connection produced by the worker was not usable: state %q, error %v", state.State, err)
	}
	wantDevice := phonehotspot.Device{MAC: "02:00:00:00:00:02", IP: "192.168.173.20", Name: "fixture-phone"}
	if len(state.Devices) != 1 || state.Devices[0] != wantDevice {
		t.Fatalf("the worker's device list was not usable: %+v", state.Devices)
	}
	if err := os.WriteFile(filepath.Join(directory, "connection.json"), []byte(`{"ssid":"fixture","password":"fixture","url":"http://192.168.173.1/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.PhoneHotspot(t.Context()); err == nil {
		t.Fatal("an HTTP hotspot URL escaped to the settings API")
	}
}

func TestPhoneHotspotRepairsUnreadableSettingsWithoutHidingServiceStatus(t *testing.T) {
	for _, contents := range []string{`null`, `{"ssid":"previous","password":"short","portal":true}`, `{"ssid":"previous",`} {
		executor := &Executor{
			runner: failedHotspotRunner{}, systemRoot: t.TempDir(), stateOwner: os.Geteuid(),
			logger: slog.New(slog.DiscardHandler),
		}
		if err := ensureStateDirectory(filepath.Dir(executor.hotspotProfilePath()), executor.stateOwner); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(executor.hotspotProfilePath(), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		reason := executor.hotspotFile("run", "anas-phone-hotspot", "reason")
		if err := os.MkdirAll(filepath.Dir(reason), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reason, []byte("invalid_settings\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		state, err := executor.PhoneHotspot(t.Context())
		if err != nil || state.State != phonehotspot.Failed || state.Reason != "invalid_settings" ||
			!state.SettingsInvalid || state.Settings != nil {
			t.Fatalf("the hotspot state hid the corrupt settings: %+v, %v", state, err)
		}
		name := "Recovered"
		settings, err := executor.SavePhoneHotspotSettings(t.Context(), phonehotspot.SettingsChange{SSID: &name})
		if err != nil || settings != (phonehotspot.Settings{SSID: name}) {
			t.Fatalf("recover hotspot profile: %+v, %v", settings, err)
		}
		state, err = executor.SetPhoneHotspot(t.Context(), false)
		if err != nil || state.State != phonehotspot.Stopping || state.SettingsInvalid ||
			state.Settings == nil || *state.Settings != settings {
			t.Fatalf("the switch response dropped recovered settings: %+v, %v", state, err)
		}
	}
}
