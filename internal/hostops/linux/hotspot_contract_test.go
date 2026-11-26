package linux

import (
	"context"
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
	profile := filepath.Join(root, "var", "lib", stateDirectoryName, "phone-hotspot.json")
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
publish = worker["publish_connection"]
publish.__globals__["RUNTIME"] = Path(sys.argv[2])
publish.__globals__["PROFILE"] = Path(sys.argv[3])
publish("fixture.example.test", *worker["hotspot_profile"]("02:00:00:00:00:01"))
stations = "02:00:00:00:00:02\nflags=[AUTH][ASSOC][AUTHORIZED]\n"
leases = "0 02:00:00:00:00:02 192.168.173.20 fixture-phone *\n"
worker["write"]("devices.json", worker["json"].dumps(worker["connected_devices"](stations, leases, 0)))
`
	if output, err := exec.CommandContext(t.Context(), "python3", "-B", "-c", produce, worker, directory, profile).CombinedOutput(); err != nil {
		t.Fatalf("produce the connection document: %v\n%s", err, output)
	}
	state, err := executor.PhoneHotspot(t.Context())
	want := phonehotspot.Connection{SSID: name, Password: password, URL: "https://fixture.example.test:8443/", Portal: true}
	if err != nil || state.State != phonehotspot.On || state.Connection == nil || *state.Connection != want {
		t.Fatalf("the connection produced by the worker was not usable: state %q, error %v", state.State, err)
	}
	wantDevice := phonehotspot.Device{MAC: "02:00:00:00:00:02", IP: "192.168.173.20", Name: "fixture-phone"}
	if state.Devices == nil || len(*state.Devices) != 1 || (*state.Devices)[0] != wantDevice {
		t.Fatalf("the worker's device list was not usable: %+v", state.Devices)
	}
	if err := os.WriteFile(filepath.Join(directory, "connection.json"), []byte(`{"ssid":"fixture","password":"fixture","url":"http://192.168.173.1/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.PhoneHotspot(t.Context()); err == nil {
		t.Fatal("an HTTP hotspot URL escaped to the settings API")
	}
}
