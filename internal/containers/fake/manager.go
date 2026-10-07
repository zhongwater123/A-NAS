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
		lines := []containers.LogLine{
			{Stream: "stdout", Time: observedAt.Add(-3 * time.Second), Text: fmt.Sprintf("[%s] starting %s", item.Name, item.Image)},
			{Stream: "stdout", Time: observedAt.Add(-2 * time.Second), Text: "listening on 0.0.0.0"},
			{Stream: "stderr", Time: observedAt.Add(-time.Second), Text: "warning: running with simulated data"},
		}
		if limit := containers.ClampTail(tail); len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
		return lines, nil
	}
	return nil, containers.ErrNotFound
}

func baseContainers() []containers.Container {
	return []containers.Container{
		{
			ID: ID("jellyfin"), Name: "jellyfin", Image: "jellyfin/jellyfin:10.11", State: containers.StateRunning,
			Status: "Up 3 hours", CreatedAt: observedAt.Add(-72 * time.Hour), Project: "media",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 8096, ContainerPort: 8096, Protocol: "tcp"}},
			Usage: &containers.Usage{CPUPercent: 12.4, MemoryBytes: 612 << 20, MemoryLimitBytes: 8 << 30},
		},
		{
			ID: ID("immich-server"), Name: "immich-server", Image: "ghcr.io/immich-app/immich-server:v2", State: containers.StateRunning,
			Status: "Up 2 days", CreatedAt: observedAt.Add(-96 * time.Hour), Project: "immich",
			Ports: []containers.Port{{HostIP: "0.0.0.0", HostPort: 2283, ContainerPort: 2283, Protocol: "tcp"}},
			Usage: &containers.Usage{CPUPercent: 3.1, MemoryBytes: 1_288 << 20, MemoryLimitBytes: 8 << 30},
		},
		{
			ID: ID("homeassistant"), Name: "homeassistant", Image: "ghcr.io/home-assistant/home-assistant:stable", State: containers.StateExited,
			Status: "Exited (0) 5 hours ago", CreatedAt: observedAt.Add(-240 * time.Hour),
		},
	}
}

func baseImages() []containers.Image {
	return []containers.Image{
		{ID: "sha256:" + ID("image-jellyfin"), Tags: []string{"jellyfin/jellyfin:10.11"}, SizeBytes: 1_243_000_000, CreatedAt: observedAt.Add(-720 * time.Hour)},
		{ID: "sha256:" + ID("image-immich"), Tags: []string{"ghcr.io/immich-app/immich-server:v2"}, SizeBytes: 1_780_000_000, CreatedAt: observedAt.Add(-480 * time.Hour)},
		{ID: "sha256:" + ID("image-ha"), Tags: []string{"ghcr.io/home-assistant/home-assistant:stable"}, SizeBytes: 1_960_000_000, CreatedAt: observedAt.Add(-360 * time.Hour)},
	}
}

var _ containers.Manager = (*Manager)(nil)
