package docker

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"

	"github.com/zhongwater123/A-NAS/internal/containers"
)

var testID = strings.Repeat("ab", 32)

func frame(stream byte, payload string) []byte {
	header := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

func TestParseLogsKeepsMultiplexedStreamsInOrder(t *testing.T) {
	var input bytes.Buffer
	input.Write(frame(1, "2026-10-07T01:02:03.000000004Z server started\n"))
	input.Write(frame(2, "2026-10-07T01:02:04Z warn: disk "))
	input.Write(frame(2, "almost full\n"))
	input.Write(frame(1, "no timestamp line\n2026-10-07T01:02:05Z trailing"))

	lines, err := parseLogs(&input, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []containers.LogLine{
		{Stream: "stdout", Time: time.Date(2026, 10, 7, 1, 2, 3, 4, time.UTC), Text: "server started"},
		{Stream: "stderr", Time: time.Date(2026, 10, 7, 1, 2, 4, 0, time.UTC), Text: "warn: disk almost full"},
		{Stream: "stdout", Text: "no timestamp line"},
		{Stream: "stdout", Time: time.Date(2026, 10, 7, 1, 2, 5, 0, time.UTC), Text: "trailing"},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

func TestParseLogsReadsTTYOutputAsStdout(t *testing.T) {
	lines, err := parseLogs(strings.NewReader("2026-10-07T00:00:00Z hello\r\n2026-10-07T00:00:01Z bye\r\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Text != "hello" || lines[1].Stream != "stdout" || lines[1].Text != "bye" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestParseLogsStopsAtTruncatedFrame(t *testing.T) {
	data := append(frame(1, "2026-10-07T00:00:00Z complete\n"), frame(1, "cut off")[:10]...)
	lines, err := parseLogs(bytes.NewReader(data), false)
	if err != nil || len(lines) != 1 || lines[0].Text != "complete" {
		t.Fatalf("lines = %+v, err = %v", lines, err)
	}
}

func TestUsageFromStatsMatchesDockerCLI(t *testing.T) {
	var stats container.StatsResponse
	stats.CPUStats.CPUUsage.TotalUsage = 3_000_000
	stats.PreCPUStats.CPUUsage.TotalUsage = 1_000_000
	stats.CPUStats.SystemUsage = 40_000_000
	stats.PreCPUStats.SystemUsage = 20_000_000
	stats.CPUStats.OnlineCPUs = 4
	stats.MemoryStats.Usage = 300 << 20
	stats.MemoryStats.Limit = 8 << 30
	stats.MemoryStats.Stats = map[string]uint64{"inactive_file": 100 << 20}

	usage := usageFromStats(stats)
	// 2M of 20M host jiffies across 4 CPUs is 40%.
	if usage.CPUPercent != 40 || usage.MemoryBytes != 200<<20 || usage.MemoryLimitBytes != 8<<30 {
		t.Fatalf("usage = %+v", usage)
	}

	stats.PreCPUStats = container.CPUStats{}
	stats.CPUStats.SystemUsage = 0
	if usage := usageFromStats(stats); usage.CPUPercent != 0 {
		t.Fatalf("usage without previous sample = %+v", usage)
	}
}

func TestMapContainerNormalizesEngineSummary(t *testing.T) {
	summary := container.Summary{
		ID:      testID,
		Names:   []string{"/jellyfin"},
		Image:   "jellyfin/jellyfin:10.11",
		Created: 1_790_000_000,
		State:   container.StateRunning,
		Status:  "Up 3 hours",
		Labels:  map[string]string{"com.docker.compose.project": "media"},
		Ports: []container.PortSummary{
			{IP: netip.MustParseAddr("0.0.0.0"), PrivatePort: 8096, PublicPort: 8096, Type: "tcp"},
			{IP: netip.MustParseAddr("::"), PrivatePort: 8096, PublicPort: 8096, Type: "tcp"},
			{PrivatePort: 1900, Type: "udp"},
		},
	}

	item, ok := mapContainer(summary)
	if !ok || item.Name != "jellyfin" || item.Project != "media" || item.State != containers.StateRunning {
		t.Fatalf("item = %+v, ok = %v", item, ok)
	}
	if len(item.Ports) != 2 || item.Ports[0].ContainerPort != 1900 || item.Ports[1].HostPort != 8096 || item.Ports[1].HostIP != "0.0.0.0" {
		t.Fatalf("ports = %+v", item.Ports)
	}

	summary.ID = "short"
	if _, ok := mapContainer(summary); ok {
		t.Fatal("container with a short ID was accepted")
	}
	summary.ID = testID
	summary.State = "unknown"
	if _, ok := mapContainer(summary); ok {
		t.Fatal("container with an unknown state was accepted")
	}
}

func TestMapImageDropsUntaggedPlaceholders(t *testing.T) {
	img := mapImage(image.Summary{ID: "sha256:abc", RepoTags: []string{"<none>:<none>", "nginx:1.29"}, Size: 42, Created: 1_790_000_000})
	if len(img.Tags) != 1 || img.Tags[0] != "nginx:1.29" || img.SizeBytes != 42 {
		t.Fatalf("image = %+v", img)
	}
}
