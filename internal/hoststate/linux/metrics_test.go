package linux

import (
	"context"
	"io/fs"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	firstStat  = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\nintr 1\n"
	secondStat = "cpu  150 0 150 1000 100 0 0 0 0 0\ncpu0 75 0 75 500 50 0 0 0 0 0\ncpu1 75 0 75 500 50 0 0 0 0 0\nintr 2\n"
	thirdStat  = "cpu  450 0 450 1100 100 0 0 0 0 0\ncpu0 225 0 225 550 50 0 0 0 0 0\ncpu1 225 0 225 550 50 0 0 0 0 0\nintr 3\n"
	meminfo    = "MemTotal:        8000000 kB\nMemFree:          500000 kB\nMemAvailable:    6000000 kB\nCached:          4000000 kB\n"
	netHeader  = "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"
)

func netDev(lo, physicalRX, physicalTX, bridge uint64) string {
	return netHeader +
		line("lo", lo, lo) +
		line("enp1s0", physicalRX, physicalTX) +
		line("docker0", bridge, bridge)
}

func line(name string, rx, tx uint64) string {
	return "  " + name + ": " + itoa(rx) + " 10 0 0 0 0 0 0 " + itoa(tx) + " 10 0 0 0 0 0 0\n"
}

func itoa(value uint64) string {
	return strconv.FormatUint(value, 10)
}

type metricsHarness struct {
	root   fstest.MapFS
	now    time.Time
	sleeps int
	onWait func()
}

func newMetricsHarness() *metricsHarness {
	return &metricsHarness{
		root: fstest.MapFS{
			"proc/stat":                   &fstest.MapFile{Data: []byte(firstStat)},
			"proc/meminfo":                &fstest.MapFile{Data: []byte(meminfo)},
			"proc/net/dev":                &fstest.MapFile{Data: []byte(netDev(9_000, 1_000, 2_000, 50_000))},
			"sys/class/net/enp1s0/device": &fstest.MapFile{Mode: fs.ModeDir},
		},
		now: time.Date(2026, time.October, 7, 1, 0, 0, 0, time.UTC),
	}
}

func (h *metricsHarness) reader() *Reader {
	return newReader(dependencies{
		root: h.root,
		now:  func() time.Time { return h.now },
		sleep: func(_ context.Context, duration time.Duration) error {
			h.sleeps++
			h.now = h.now.Add(duration)
			if h.onWait != nil {
				h.onWait()
			}
			return nil
		},
	})
}

func (h *metricsHarness) set(path, contents string) {
	h.root[path] = &fstest.MapFile{Data: []byte(contents)}
}

func TestReadMetricsSamplesBaselineWindowOnFirstCall(t *testing.T) {
	h := newMetricsHarness()
	h.onWait = func() {
		h.set("proc/stat", secondStat)
		h.set("proc/net/dev", netDev(99_000, 1_500, 2_250, 950_000))
	}

	metrics, err := h.reader().ReadMetrics(context.Background())
	if err != nil {
		t.Fatalf("ReadMetrics() error = %v", err)
	}
	want := hoststate.Metrics{
		ObservedAt: time.Date(2026, time.October, 7, 1, 0, 0, int(baselineWindow), time.UTC),
		CPU:        hoststate.CPUMetrics{UsagePercent: 25, LogicalCores: 2},
		Memory:     hoststate.MemoryMetrics{TotalBytes: 8_000_000 * 1024, UsedBytes: 2_000_000 * 1024},
		// Loopback and the bridge are excluded; 500 B and 250 B over half a second.
		Network: hoststate.NetworkMetrics{ReceiveBytesPerSecond: 1_000, TransmitBytesPerSecond: 500},
	}
	if metrics != want {
		t.Fatalf("metrics = %+v, want %+v", metrics, want)
	}
	if h.sleeps != 1 {
		t.Fatalf("baseline sleeps = %d, want 1", h.sleeps)
	}
}

func TestReadMetricsReusesPreviousSampleAndCachesRapidPolls(t *testing.T) {
	h := newMetricsHarness()
	h.onWait = func() { h.set("proc/stat", secondStat) }
	reader := h.reader()
	if _, err := reader.ReadMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.onWait = func() { t.Fatal("unexpected baseline sample") }

	h.now = h.now.Add(2 * time.Second)
	h.set("proc/stat", thirdStat)
	h.set("proc/net/dev", netDev(0, 5_000, 3_000, 0))
	second, err := reader.ReadMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 600 of 700 jiffies busy; enp1s0 grew by 4000 and 1000 bytes in two seconds.
	if second.CPU.UsagePercent != 85.7 || second.Network.ReceiveBytesPerSecond != 2_000 || second.Network.TransmitBytesPerSecond != 500 {
		t.Fatalf("second metrics = %+v", second)
	}

	h.now = h.now.Add(100 * time.Millisecond)
	h.set("proc/stat", firstStat)
	third, err := reader.ReadMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third != second {
		t.Fatalf("rapid poll = %+v, want cached %+v", third, second)
	}
}

func TestReadMetricsTreatsCounterResetAsZero(t *testing.T) {
	h := newMetricsHarness()
	h.onWait = func() { h.set("proc/net/dev", netDev(0, 10, 20, 0)) }

	metrics, err := h.reader().ReadMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Network != (hoststate.NetworkMetrics{}) || metrics.CPU.UsagePercent != 0 {
		t.Fatalf("metrics after reset = %+v", metrics)
	}
}

func TestReadNetworkFallsBackToNonLoopbackInterfaces(t *testing.T) {
	root := fstest.MapFS{"proc/net/dev": &fstest.MapFile{Data: []byte(netDev(1, 2, 3, 4))}}

	interfaces, err := readNetwork(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) != 2 || interfaces["enp1s0"].received != 2 || interfaces["docker0"].transmitted != 4 {
		t.Fatalf("interfaces = %+v", interfaces)
	}
}

func TestReadMetricsRejectsMalformedProcFiles(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"missing meminfo":  {"proc/stat": {Data: []byte(firstStat)}, "proc/net/dev": {Data: []byte(netHeader)}},
		"short cpu line":   {"proc/stat": {Data: []byte("cpu 1 2 3\ncpu0 1 2 3\n")}, "proc/meminfo": {Data: []byte(meminfo)}, "proc/net/dev": {Data: []byte(netHeader)}},
		"bad net counters": {"proc/stat": {Data: []byte(firstStat)}, "proc/meminfo": {Data: []byte(meminfo)}, "proc/net/dev": {Data: []byte(netHeader + "  eth0: x 1\n")}},
	}
	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			h := &metricsHarness{root: root, now: time.Unix(0, 0)}
			if _, err := h.reader().ReadMetrics(context.Background()); err == nil {
				t.Fatal("ReadMetrics() error = nil, want parse error")
			}
		})
	}
}
