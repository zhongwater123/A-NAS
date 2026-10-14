// Package fake provides deterministic containers for local development and tests.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/containers"
)

var errUnavailable = fmt.Errorf("%w: fake engine is offline", containers.ErrUnavailable)

var observedAt = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)

// Manager keeps container state in memory; actions change it so the desktop
// can exercise the full start/stop/restart flow without a Docker Engine.
type Manager struct {
	mu         sync.Mutex
	containers []containers.Container
	images     []containers.Image
	err        error
}

func New() *Manager {
	return &Manager{containers: baseContainers(), images: baseImages()}
}

func NewUnavailable() *Manager {
	return &Manager{err: errUnavailable}
}

// ID derives the stable fake engine ID for a container name.
func ID(name string) string {
	sum := sha256.Sum256([]byte("a-nas-fake-container:" + name))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) Snapshot(ctx context.Context) (containers.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return containers.Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return containers.Snapshot{}, m.err
	}
	items := make([]containers.Container, len(m.containers))
	for i, item := range m.containers {
		items[i] = item
		items[i].Ports = append([]containers.Port(nil), item.Ports...)
		if item.Usage != nil {
			usage := *item.Usage
			items[i].Usage = &usage
		}
	}
	images := make([]containers.Image, len(m.images))
	for i, image := range m.images {
		images[i] = image
		images[i].Tags = append([]string(nil), image.Tags...)
	}
	return containers.Snapshot{
		ObservedAt: observedAt,
		Engine:     containers.Engine{Version: "29.1.3", APIVersion: "1.52"},
		Containers: items,
		Images:     images,
	}, nil
}

func (m *Manager) Act(ctx context.Context, id string, action containers.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !containers.ValidID(id) {
		return containers.ErrInvalidID
	}
	if !action.Valid() {
		return containers.ErrInvalidAction
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	for i := range m.containers {
		item := &m.containers[i]
		if item.ID != id {
			continue
		}
		switch action {
		case containers.ActionStart, containers.ActionRestart:
			item.State = containers.StateRunning
			item.Status = "Up Less than a second"
			item.Usage = &containers.Usage{CPUPercent: 1.2, MemoryBytes: 64 << 20, MemoryLimitBytes: 8 << 30}
		case containers.ActionStop:
			item.State = containers.StateExited
			item.Status = "Exited (0) Less than a second ago"
			item.Usage = nil
		}
		return nil
	}
	return containers.ErrNotFound
}

func (m *Manager) Logs(ctx context.Context, id string, tail int) ([]containers.LogLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !containers.ValidID(id) {
		return nil, containers.ErrInvalidID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	for _, item := range m.containers {
		if item.ID != id {
			continue
		}
		lines := fakeLogs(item)
		if limit := containers.ClampTail(tail); len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
		return lines, nil
	}
	return nil, containers.ErrNotFound
}

// fakeLogs is a plausible service start-up ending in a stderr warning, enough
// to exercise searching, stream colours and following in the log viewer.
func fakeLogs(item containers.Container) []containers.LogLine {
	texts := []struct{ stream, text string }{
		{"stdout", fmt.Sprintf("[%s] starting %s", item.Name, item.Image)},
		{"stdout", "loading configuration from /config"},
		{"stdout", "timezone Asia/Shanghai, running as uid 30002"},
		{"stdout", "database migrations: 0 pending"},
		{"stdout", "scheduled task \"Clean cache\" registered (daily 03:00)"},
		{"stderr", "warning: hardware acceleration is not available, falling back to software"},
		{"stdout", "scheduled task \"Refresh library\" registered (every 12h)"},
		{"stdout", "plugin manager: 4 plugins loaded"},
		{"stdout", "GET /health 200 2ms"},
		{"stdout", "GET /api/status 200 11ms"},
		{"stdout", "listening on 0.0.0.0"},
		{"stdout", "GET /health 200 1ms"},
		{"stderr", "warning: running with simulated data"},
	}
	lines := make([]containers.LogLine, len(texts))
	for i, entry := range texts {
		lines[i] = containers.LogLine{Stream: entry.stream, Time: observedAt.Add(time.Duration(i-len(texts)) * 7 * time.Second), Text: entry.text}
	}
	return lines
}

// baseContainers keeps jellyfin first and homeassistant third: tests address them by position.
func baseContainers() []containers.Container {
	const limit = 8 << 30
	return []containers.Container{
		{
			ID: ID("jellyfin"), Name: "jellyfin", Image: "jellyfin/jellyfin:10.11", ImageID: imageID("jellyfin"), State: containers.StateRunning,
			Status: "Up 3 hours (healthy)", CreatedAt: observedAt.Add(-72 * time.Hour), Project: "media",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 8096, ContainerPort: 8096, Protocol: "tcp"}},
			Usage: &containers.Usage{CPUPercent: 12.4, MemoryBytes: 612 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("immich-server"), Name: "immich-server", Image: "ghcr.io/immich-app/immich-server:v2.7.2", ImageID: imageID("immich"), State: containers.StateRunning,
			Status: "Up 2 days (healthy)", CreatedAt: observedAt.Add(-96 * time.Hour), Project: "a-nas-immich",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 2283, ContainerPort: 2283, Protocol: "tcp"}},
			Usage: &containers.Usage{CPUPercent: 3.1, MemoryBytes: 1_288 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("homeassistant"), Name: "homeassistant", Image: "ghcr.io/home-assistant/home-assistant:stable", ImageID: imageID("ha"), State: containers.StateExited,
			Status: "Exited (0) 5 hours ago", CreatedAt: observedAt.Add(-240 * time.Hour),
		},
		{
			ID: ID("immich-machine-learning"), Name: "immich-machine-learning", Image: "ghcr.io/immich-app/immich-machine-learning:v2.7.2", ImageID: imageID("immich-ml"),
			State: containers.StateRunning, Status: "Up 2 days (healthy)", CreatedAt: observedAt.Add(-96 * time.Hour), Project: "a-nas-immich",
			Usage: &containers.Usage{CPUPercent: 0.4, MemoryBytes: 2_150 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("immich-redis"), Name: "immich-redis", Image: "docker.io/valkey/valkey:9", ImageID: imageID("valkey"), State: containers.StateRunning,
			Status: "Up 2 days (healthy)", CreatedAt: observedAt.Add(-96 * time.Hour), Project: "a-nas-immich",
			Usage: &containers.Usage{CPUPercent: 0.3, MemoryBytes: 14 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("immich-postgres"), Name: "immich-postgres", Image: "ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0", ImageID: imageID("immich-pg"),
			State: containers.StateRunning, Status: "Up 2 days (healthy)", CreatedAt: observedAt.Add(-96 * time.Hour), Project: "a-nas-immich",
			Usage: &containers.Usage{CPUPercent: 0.8, MemoryBytes: 214 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("openlist"), Name: "openlist", Image: "openlistteam/openlist:v4.2.2", ImageID: imageID("openlist"), State: containers.StateRunning,
			Status: "Up 26 hours", CreatedAt: observedAt.Add(-26 * time.Hour), Project: "a-nas-openlist",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 5244, ContainerPort: 5244, Protocol: "tcp"}},
			Usage: &containers.Usage{CPUPercent: 0.2, MemoryBytes: 46 << 20, MemoryLimitBytes: limit},
		},
		{
			ID: ID("jellyseerr"), Name: "jellyseerr", Image: "fallenbagel/jellyseerr:2.7", ImageID: imageID("jellyseerr"), State: containers.StateExited,
			Status: "Exited (1) 2 days ago", CreatedAt: observedAt.Add(-120 * time.Hour), Project: "media",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 5055, ContainerPort: 5055, Protocol: "tcp"}},
		},
	}
}

func imageID(name string) string {
	return "sha256:" + ID("image-"+name)
}

func baseImages() []containers.Image {
	return []containers.Image{
		{ID: imageID("jellyfin"), Tags: []string{"jellyfin/jellyfin:10.11"}, SizeBytes: 1_243_000_000, CreatedAt: observedAt.Add(-720 * time.Hour)},
		{ID: imageID("immich"), Tags: []string{"ghcr.io/immich-app/immich-server:v2.7.2"}, SizeBytes: 1_780_000_000, CreatedAt: observedAt.Add(-480 * time.Hour)},
		{ID: imageID("ha"), Tags: []string{"ghcr.io/home-assistant/home-assistant:stable"}, SizeBytes: 1_960_000_000, CreatedAt: observedAt.Add(-360 * time.Hour)},
		{ID: imageID("immich-ml"), Tags: []string{"ghcr.io/immich-app/immich-machine-learning:v2.7.2"}, SizeBytes: 1_120_000_000, CreatedAt: observedAt.Add(-480 * time.Hour)},
		{ID: imageID("valkey"), Tags: []string{"valkey/valkey:9"}, SizeBytes: 118_000_000, CreatedAt: observedAt.Add(-900 * time.Hour)},
		{ID: imageID("immich-pg"), Tags: []string{"ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0"}, SizeBytes: 612_000_000, CreatedAt: observedAt.Add(-1_400 * time.Hour)},
		{ID: imageID("openlist"), Tags: []string{"openlistteam/openlist:v4.2.2"}, SizeBytes: 94_000_000, CreatedAt: observedAt.Add(-300 * time.Hour)},
		{ID: imageID("jellyseerr"), Tags: []string{"fallenbagel/jellyseerr:2.7"}, SizeBytes: 471_000_000, CreatedAt: observedAt.Add(-600 * time.Hour)},
		{ID: imageID("jellyfin-old"), Tags: []string{"jellyfin/jellyfin:10.10"}, SizeBytes: 1_198_000_000, CreatedAt: observedAt.Add(-2_000 * time.Hour)},
		{ID: imageID("dangling"), Tags: []string{}, SizeBytes: 352_000_000, CreatedAt: observedAt.Add(-1_100 * time.Hour)},
	}
}

var _ containers.Manager = (*Manager)(nil)
