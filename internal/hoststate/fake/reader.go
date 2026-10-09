// Package fake provides deterministic host state for local development and tests.
package fake

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

var errUnavailable = errors.New("fake host state is unavailable")

type Reader struct {
	state   hoststate.State
	metrics hoststate.Metrics
	err     error
}

func NewHealthy() *Reader {
	return newReader(baseState())
}

func NewWarning() *Reader {
	state := baseState()
	state.System.Health = hoststate.HealthWarning
	state.Disks[1].Health = hoststate.HealthWarning
	state.Disks[1].TemperatureCelsius = intPointer(55)
	return newReader(state)
}

func NewUnavailable() *Reader {
	return &Reader{err: errUnavailable}
}

func newReader(state hoststate.State) *Reader {
	sort.Slice(state.Disks, func(i, j int) bool {
		return state.Disks[i].ID.String() < state.Disks[j].ID.String()
	})
	return &Reader{state: cloneState(state), metrics: baseMetrics()}
}

func (r *Reader) Read(ctx context.Context) (hoststate.State, error) {
	if err := ctx.Err(); err != nil {
		return hoststate.State{}, err
	}
	if r.err != nil {
		return hoststate.State{}, r.err
	}
	return cloneState(r.state), nil
}

// ReadMetrics returns fixed utilisation so screenshots and tests are stable.
func (r *Reader) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	if err := ctx.Err(); err != nil {
		return hoststate.Metrics{}, err
	}
	if r.err != nil {
		return hoststate.Metrics{}, r.err
	}
	return r.metrics, nil
}

func baseMetrics() hoststate.Metrics {
	return hoststate.Metrics{
		ObservedAt: time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC),
		CPU:        hoststate.CPUMetrics{UsagePercent: 23.5, LogicalCores: 4},
		Memory:     hoststate.MemoryMetrics{TotalBytes: 8 << 30, UsedBytes: 3_435_973_837},
		Network:    hoststate.NetworkMetrics{ReceiveBytesPerSecond: 2_516_582, TransmitBytesPerSecond: 327_680},
	}
}

func baseState() hoststate.State {
	return hoststate.State{
		ObservedAt: time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC),
		System: hoststate.System{
			ID:              mustResourceID("host:fake-01"),
			Hostname:        "anas-fake",
			OperatingSystem: hoststate.OperatingSystem{Name: "Debian", Version: "13"},
			Architecture:    "amd64",
			UptimeSeconds:   3600,
			Health:          hoststate.HealthHealthy,
		},
		Disks: []hoststate.Disk{
			{
				ID:                 mustResourceID("disk:fake-system-01"),
				Model:              "A-NAS Fake SSD",
				Transport:          hoststate.TransportNVMe,
				CapacityBytes:      125_000_000_000,
				Rotational:         false,
				Role:               hoststate.DiskRoleSystem,
				Health:             hoststate.HealthHealthy,
				SMARTStatus:        hoststate.HealthHealthy,
				TemperatureCelsius: intPointer(36),
				InUse:              true,
				Filesystems:        []string{"ext4"},
			},
			{
				ID:                 mustResourceID("disk:fake-data-01"),
				Model:              "A-NAS Fake HDD",
				Transport:          hoststate.TransportSATA,
				CapacityBytes:      512_000_000_000,
				Rotational:         true,
				Role:               hoststate.DiskRoleUnassigned,
				Health:             hoststate.HealthHealthy,
				SMARTStatus:        hoststate.HealthHealthy,
				TemperatureCelsius: intPointer(31),
			},
		},
	}
}

func cloneState(state hoststate.State) hoststate.State {
	cloned := state
	cloned.Disks = make([]hoststate.Disk, len(state.Disks))
	for i, disk := range state.Disks {
		cloned.Disks[i] = disk
		if disk.TemperatureCelsius != nil {
			cloned.Disks[i].TemperatureCelsius = intPointer(*disk.TemperatureCelsius)
		}
		if disk.FilesystemCapacityBytes != nil {
			value := *disk.FilesystemCapacityBytes
			cloned.Disks[i].FilesystemCapacityBytes = &value
		}
		if disk.FilesystemAvailableBytes != nil {
			value := *disk.FilesystemAvailableBytes
			cloned.Disks[i].FilesystemAvailableBytes = &value
		}
		cloned.Disks[i].Filesystems = append([]string(nil), disk.Filesystems...)
	}
	return cloned
}

func mustResourceID(value string) hoststate.ResourceID {
	id, err := hoststate.NewResourceID(value)
	if err != nil {
		panic(err)
	}
	return id
}

func intPointer(value int) *int {
	return &value
}

var _ hoststate.Observer = (*Reader)(nil)
