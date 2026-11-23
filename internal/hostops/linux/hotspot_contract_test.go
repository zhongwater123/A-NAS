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
	worker := filepath.Join("..", "..", "..", "deploy", "network", "phone-hotspot.py")
	produce := `
import runpy
import sys
from pathlib import Path
worker = runpy.run_path(sys.argv[1])
publish = worker["publish_connection"]
publish.__globals__["RUNTIME"] = Path(sys.argv[2])
publish("fixture.example.test", "A-NAS-TEST", "fixture-only")
`
	if output, err := exec.CommandContext(t.Context(), "python3", "-B", "-c", produce, worker, directory).CombinedOutput(); err != nil {
		t.Fatalf("produce the connection document: %v\n%s", err, output)
	}
	executor := &Executor{runner: activeHotspotRunner{}, systemRoot: root}
	state, err := executor.PhoneHotspot(t.Context())
	want := phonehotspot.Connection{SSID: "A-NAS-TEST", Password: "fixture-only", URL: "https://fixture.example.test:8443/"}
	if err != nil || state.State != phonehotspot.On || state.Connection == nil || *state.Connection != want {
		t.Fatalf("the connection produced by the worker was not usable: state %q, error %v", state.State, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "connection.json"), []byte(`{"ssid":"fixture","password":"fixture","url":"http://192.168.173.1/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.PhoneHotspot(t.Context()); err == nil {
		t.Fatal("an HTTP hotspot URL escaped to the settings API")
	}
}
