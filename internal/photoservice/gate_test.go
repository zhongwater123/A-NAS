package photoservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestGateWaitsForPressureToFallWellBelowItsLimit(t *testing.T) {
	cpu := 15.0
	gate := &Gate{
		quiet: time.Minute,
		now:   func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC) },
		pressure: func(resource string) (float64, error) {
			if resource == "cpu" {
				return cpu, nil
			}
			return 0, nil
		},
	}
	open := func() bool { ok, _ := gate.Open(context.Background()); return ok }
	if open() {
		t.Fatal("Open() with others waiting 15% of the time = open")
	}
	// Hovering just under the limit keeps work stopped, so it does not
	// start and stop every few seconds.
	cpu = 8
	if open() {
		t.Fatal("Open() just under the limit after being over it = open")
	}
	cpu = 4
	if !open() {
		t.Fatal("Open() well under the limit = closed")
	}
	cpu = 8
	if !open() {
		t.Fatal("Open() under the limit while open = closed")
	}
}

func TestPressureIsTheDeviceWorkOutsideTheAIWorker(t *testing.T) {
	mount := t.TempDir()
	root := "/system.slice/docker-1.scope" // as in the system test's container
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	psi := func(some float64) string {
		return "some avg10=" + formatFloat(some) + " avg60=0.00 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
	}
	write(filepath.Join(mount, root, "system.slice/cpu.pressure"), psi(3.5))
	write(filepath.Join(mount, root, "user.slice/cpu.pressure"), psi(12.25))
	write(filepath.Join(mount, root, "anas-ai.slice/cpu.pressure"), psi(90)) // the Worker's own waiting
	self := filepath.Join(t.TempDir(), "cgroup")
	write(self, "0::"+root+"/system.slice/anas-photos.service\n")

	found := systemSlices(self, mount)
	want := []string{filepath.Join(mount, root, "system.slice"), filepath.Join(mount, root, "user.slice")}
	if !slices.Equal(found, want) {
		t.Fatalf("systemSlices() = %v, want %v", found, want)
	}
	if value, err := othersPressure(found)("cpu"); err != nil || value != 12.25 {
		t.Fatalf("cpu pressure = %v, %v; want the user slice's 12.25 and never the Worker's", value, err)
	}
	if _, err := othersPressure(found)("io"); err == nil {
		t.Fatal("io pressure without any io.pressure file has no error")
	}

	write(self, "0::/user.slice/user-1000.slice/session-2.scope\n")
	if found := systemSlices(self, mount); found != nil {
		t.Fatalf("systemSlices() outside a service = %v, want none", found)
	}
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', 2, 64) }

func TestGateWaitsForQuietAndLowPressure(t *testing.T) {
	now := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	pressure := map[string]float64{"cpu": 2, "io": 1, "memory": 0}
	gate := &Gate{
		quiet: 5 * time.Minute,
		now:   func() time.Time { return now },
		pressure: func(resource string) (float64, error) {
			value, ok := pressure[resource]
			if !ok {
				return 0, errors.New("no pressure information")
			}
			return value, nil
		},
	}
	open := func() (bool, string) { return gate.Open(context.Background()) }

	gate.Touch()
	if ok, reason := open(); ok || reason != "foreground" {
		t.Fatalf("Open() right after use = %v, %q; want closed for foreground", ok, reason)
	}
	now = now.Add(5 * time.Minute)
	if ok, reason := open(); !ok {
		t.Fatalf("Open() after the quiet period = closed (%s)", reason)
	}

	pressure["io"] = 30
	if ok, reason := open(); ok || reason != "io_pressure" {
		t.Fatalf("Open() under I/O pressure = %v, %q", ok, reason)
	}
	// A kernel without pressure information leaves only the activity check.
	delete(pressure, "io")
	if ok, _ := open(); !ok {
		t.Fatal("Open() without I/O pressure information = closed")
	}

	// AI leaves a reserve of memory for the user.
	free := uint64(4 << 30)
	gate.available = func() (uint64, error) { return free, nil }
	if ok, _ := open(); !ok {
		t.Fatal("Open() with memory to spare = closed")
	}
	free = MemoryReserve - 1
	if ok, reason := open(); ok || reason != "memory_low" {
		t.Fatalf("Open() below the memory reserve = %v, %q", ok, reason)
	}
}
