package photoservice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

func TestGateRecordsWhatHeldAIWorkBack(t *testing.T) {
	slice := filepath.Join(t.TempDir(), "system.slice")
	write := func(member string, some float64) {
		t.Helper()
		path := filepath.Join(slice, member, "io.pressure")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "some avg10=" + formatFloat(some) + " avg60=0.00 avg300=0.00 total=1\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("", 12.3)
	write("anas-host-agent.service", 8.1)
	write("anas-photos.service", 4)
	write("smartmontools.service", 0)
	free := uint64(4 << 30)
	var logged bytes.Buffer
	gate := &Gate{
		now:       func() time.Time { return time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC) },
		watched:   []string{slice},
		pressure:  othersPressure([]string{slice}),
		available: func() (uint64, error) { return free, nil },
		logger:    slog.New(slog.NewTextHandler(&logged, nil)),
	}
	open := func() (bool, string) { return gate.Open(context.Background()) }

	// Each change is recorded once, however often the gate is asked.
	for range 3 {
		if ok, reason := open(); ok || reason != "io_pressure" {
			t.Fatalf("Open() = %v, %q; want closed for I/O pressure", ok, reason)
		}
	}
	write("", 3)
	if ok, reason := open(); !ok {
		t.Fatalf("Open() once the pressure halved = closed (%s)", reason)
	}
	free = MemoryReserve - 1<<20
	for range 2 {
		if ok, reason := open(); ok || reason != "memory_low" {
			t.Fatalf("Open() = %v, %q; want closed for low memory", ok, reason)
		}
	}
	free = 4 << 30
	if ok, reason := open(); !ok {
		t.Fatalf("Open() with memory again = closed (%s)", reason)
	}

	want := []string{
		`msg="AI work yields to pressure" resource=io avg10=12.3 limit=10 system.slice="12.30: anas-host-agent.service 8.10, anas-photos.service 4.00"`,
		`msg="AI work no longer yields to pressure" resource=io avg10=3 below=5`,
		`msg="AI work yields to low memory" available_mib=1535 reserve_mib=1536`,
		`msg="AI work no longer yields to low memory" available_mib=4096`,
	}
	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != len(want) {
		t.Fatalf("log =\n%s\nwant %d lines", logged.String(), len(want))
	}
	for i, line := range lines {
		if !strings.HasSuffix(line, want[i]) {
			t.Fatalf("log line %d = %s\nwant it to end with %s", i+1, line, want[i])
		}
	}
}

func TestGateStaysClosedWhileARequestIsServed(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	gate := &Gate{quiet: time.Minute, now: func() time.Time { return now }, pressure: func(string) (float64, error) { return 0, nil }}
	done := gate.Use()
	// A video played for longer than the quiet period is still use.
	now = now.Add(time.Hour)
	if ok, reason := gate.Open(context.Background()); ok || reason != "foreground" {
		t.Fatalf("Open() while a video plays = %v, %q", ok, reason)
	}
	done()
	now = now.Add(time.Minute - time.Second)
	if ok, _ := gate.Open(context.Background()); ok {
		t.Fatal("Open() within the quiet period after playing ended = open")
	}
	now = now.Add(time.Second)
	if ok, _ := gate.Open(context.Background()); !ok {
		t.Fatal("Open() a quiet period after playing ended = closed")
	}
}
