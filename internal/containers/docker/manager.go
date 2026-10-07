// Package docker implements containers.Manager against the Docker Engine API
// using the official Moby client. It runs only inside the container agent,
// the one A-NAS process allowed to reach the Docker socket.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/containers"
)

const (
	actionTimeoutSeconds = 10
	statsConcurrency     = 8
	statsBudget          = 3 * time.Second
	maxLogBytes          = 1 << 20
	composeProjectLabel  = "com.docker.compose.project"
)

type Manager struct {
	client *client.Client
	now    func() time.Time
}

// New connects using DOCKER_HOST (default unix:///var/run/docker.sock) and
// negotiates the API version with the daemon.
func New() (*Manager, error) {
	engine, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Manager{client: engine, now: time.Now}, nil
}

func (m *Manager) Close() error {
	return m.client.Close()
}

func (m *Manager) Snapshot(ctx context.Context) (containers.Snapshot, error) {
	version, err := m.client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return containers.Snapshot{}, unavailable(err)
	}
	list, err := m.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return containers.Snapshot{}, unavailable(err)
	}
	images, err := m.client.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return containers.Snapshot{}, unavailable(err)
	}

	items := make([]containers.Container, 0, len(list.Items))
	for _, summary := range list.Items {
		item, ok := mapContainer(summary)
		if ok {
			items = append(items, item)
		}
	}
	m.attachUsage(ctx, items)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	mappedImages := make([]containers.Image, 0, len(images.Items))
	for _, summary := range images.Items {
		mappedImages = append(mappedImages, mapImage(summary))
	}
	sort.Slice(mappedImages, func(i, j int) bool { return mappedImages[i].CreatedAt.After(mappedImages[j].CreatedAt) })

	return containers.Snapshot{
		ObservedAt: m.now().UTC(),
		Engine:     containers.Engine{Version: version.Version, APIVersion: version.APIVersion},
		Containers: items,
		Images:     mappedImages,
	}, nil
}

// attachUsage samples running containers concurrently within a fixed budget;
// containers whose sample misses the budget are listed without usage.
func (m *Manager) attachUsage(ctx context.Context, items []containers.Container) {
	ctx, cancel := context.WithTimeout(ctx, statsBudget)
	defer cancel()
	slots := make(chan struct{}, statsConcurrency)
	var wait sync.WaitGroup
	for i := range items {
		if items[i].State != containers.StateRunning {
			continue
		}
		wait.Add(1)
		go func(item *containers.Container) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			result, err := m.client.ContainerStats(ctx, item.ID, client.ContainerStatsOptions{IncludePreviousSample: true})
			if err != nil {
				return
			}
			defer result.Body.Close()
			var stats container.StatsResponse
			if json.NewDecoder(io.LimitReader(result.Body, 1<<20)).Decode(&stats) == nil {
				item.Usage = usageFromStats(stats)
			}
		}(&items[i])
	}
	wait.Wait()
}

func (m *Manager) Act(ctx context.Context, id string, action containers.Action) error {
	if !containers.ValidID(id) {
		return containers.ErrInvalidID
	}
	timeout := actionTimeoutSeconds
	var err error
	switch action {
	case containers.ActionStart:
		_, err = m.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	case containers.ActionStop:
		_, err = m.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	case containers.ActionRestart:
		_, err = m.client.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
	default:
		return containers.ErrInvalidAction
	}
	return engineError(err)
}

func (m *Manager) Logs(ctx context.Context, id string, tail int) ([]containers.LogLine, error) {
	if !containers.ValidID(id) {
		return nil, containers.ErrInvalidID
	}
	inspect, err := m.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, engineError(err)
	}
	stream, err := m.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(containers.ClampTail(tail)),
	})
	if err != nil {
		return nil, engineError(err)
	}
	defer stream.Close()
	tty := inspect.Container.Config != nil && inspect.Container.Config.Tty
	lines, err := parseLogs(io.LimitReader(stream, maxLogBytes), tty)
	if err != nil {
		return nil, fmt.Errorf("read container logs: %w", err)
	}
	if limit := containers.ClampTail(tail); len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

func mapContainer(summary container.Summary) (containers.Container, bool) {
	state := containers.State(summary.State)
	if !containers.ValidID(summary.ID) || !state.Valid() {
		return containers.Container{}, false
	}
	name := summary.ID[:12]
	if len(summary.Names) > 0 {
		name = strings.TrimPrefix(summary.Names[0], "/")
	}
	ports := make([]containers.Port, 0, len(summary.Ports))
	seen := make(map[string]struct{})
	for _, port := range summary.Ports {
		hostIP := ""
		if port.IP.IsValid() {
			hostIP = port.IP.String()
		}
		// Docker lists IPv4 and IPv6 bindings of one mapping separately.
		key := fmt.Sprintf("%d/%d/%s", port.PublicPort, port.PrivatePort, port.Type)
		if _, duplicate := seen[key]; duplicate && port.PublicPort != 0 {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, containers.Port{HostIP: hostIP, HostPort: port.PublicPort, ContainerPort: port.PrivatePort, Protocol: string(port.Type)})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].ContainerPort != ports[j].ContainerPort {
			return ports[i].ContainerPort < ports[j].ContainerPort
		}
		return ports[i].Protocol < ports[j].Protocol
	})
	return containers.Container{
		ID:        summary.ID,
		Name:      name,
		Image:     summary.Image,
		State:     state,
		Status:    summary.Status,
		CreatedAt: time.Unix(summary.Created, 0).UTC(),
		Ports:     ports,
		Project:   summary.Labels[composeProjectLabel],
	}, true
}

func mapImage(summary image.Summary) containers.Image {
	tags := make([]string, 0, len(summary.RepoTags))
	for _, tag := range summary.RepoTags {
		if tag != "<none>:<none>" {
			tags = append(tags, tag)
		}
	}
	return containers.Image{
		ID:        summary.ID,
		Tags:      tags,
		SizeBytes: summary.Size,
		CreatedAt: time.Unix(summary.Created, 0).UTC(),
	}
}

// usageFromStats follows the docker CLI: CPU is the container's share of host
// CPU time between the two samples scaled by online CPUs, and memory excludes
// reclaimable page cache.
func usageFromStats(stats container.StatsResponse) *containers.Usage {
	usage := &containers.Usage{MemoryLimitBytes: stats.MemoryStats.Limit}
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
	online := float64(stats.CPUStats.OnlineCPUs)
	if online == 0 {
		online = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta > 0 && systemDelta > 0 && online > 0 {
		usage.CPUPercent = math.Round(cpuDelta/systemDelta*online*1000) / 10
	}
	cache := stats.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = stats.MemoryStats.Stats["total_inactive_file"]
	}
	if stats.MemoryStats.Usage > cache {
		usage.MemoryBytes = stats.MemoryStats.Usage - cache
	}
	return usage
}

// parseLogs decodes timestamped log output. Without a TTY the engine
// multiplexes stdout and stderr into frames of an 8-byte header (stream type,
// three zero bytes, big-endian length) followed by the payload; parsing the
// frames in order keeps the two streams interleaved as they were written.
func parseLogs(r io.Reader, tty bool) ([]containers.LogLine, error) {
	var lines []containers.LogLine
	pending := map[string]*bytes.Buffer{}
	emit := func(stream string, chunk []byte) {
		buffer := pending[stream]
		if buffer == nil {
			buffer = &bytes.Buffer{}
			pending[stream] = buffer
		}
		buffer.Write(chunk)
		for {
			line, err := buffer.ReadBytes('\n')
			if err != nil {
				buffer.Reset()
				buffer.Write(line)
				return
			}
			lines = append(lines, parseLogLine(stream, strings.TrimRight(string(line), "\r\n")))
		}
	}

	if tty {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		emit("stdout", data)
	} else {
		reader := bufio.NewReader(r)
		header := make([]byte, 8)
		for {
			if _, err := io.ReadFull(reader, header); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					break
				}
				return nil, err
			}
			stream := "stdout"
			if header[0] == 2 {
				stream = "stderr"
			}
			payload := make([]byte, binary.BigEndian.Uint32(header[4:]))
			if _, err := io.ReadFull(reader, payload); err != nil {
				// The byte limit can cut the final frame; keep what was complete.
				break
			}
			emit(stream, payload)
		}
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if buffer := pending[stream]; buffer != nil && buffer.Len() > 0 {
			lines = append(lines, parseLogLine(stream, strings.TrimRight(buffer.String(), "\r\n")))
		}
	}
	return lines, nil
}

func parseLogLine(stream, raw string) containers.LogLine {
	line := containers.LogLine{Stream: stream, Text: raw}
	if stamp, text, ok := strings.Cut(raw, " "); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			line.Time = parsed.UTC()
			line.Text = text
		}
	}
	return line
}

func engineError(err error) error {
	switch {
	case err == nil:
		return nil
	case cerrdefs.IsNotFound(err):
		return containers.ErrNotFound
	case client.IsErrConnectionFailed(err):
		return unavailable(err)
	default:
		return err
	}
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %v", containers.ErrUnavailable, err)
}

var _ containers.Manager = (*Manager)(nil)
