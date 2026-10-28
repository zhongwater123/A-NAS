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
	state.Disks[1].TemperatureCelsius = new(55)
	return newReader(state)
}

// NewWithSpareDisk adds a second data disk that another NAS left an md
// array signature on, so a volume can be created on two disks or grown.
func NewWithSpareDisk() *Reader {
	state := baseState()
	state.Disks = append(state.Disks, hoststate.Disk{
		ID:                 mustResourceID("disk:fake-data-02"),
		Model:              "A-NAS Fake HDD",
		Transport:          hoststate.TransportSATA,
		CapacityBytes:      512_000_000_000,
		Rotational:         true,
		Role:               hoststate.DiskRoleUnassigned,
		Health:             hoststate.HealthHealthy,
		SMARTStatus:        hoststate.HealthHealthy,
		TemperatureCelsius: new(32),
		Filesystems:        []string{"linux_raid_member"},
		Signatures:         []hoststate.Signature{{Type: "linux_raid_member", Label: "OrangePi3B:0", UUID: "2a5a4bd2-c45f-586f-20c3-f2657f46b405"}},
	})
	return newReader(state)
}

func NewUnavailable() *Reader {
	return &Reader{err: errUnavailable}
}

// NewWithUnusableDisks keeps a usable disk alongside an empty card reader
// and two disks reporting the same hardware identity.
func NewWithUnusableDisks() *Reader {
	state := baseState()
	state.Disks = append(state.Disks, hoststate.Disk{
		ID: mustResourceID("observation:empty-reader"), Transport: hoststate.TransportUSB,
		Role: hoststate.DiskRoleUnassigned, Health: hoststate.HealthUnknown, SMARTStatus: hoststate.HealthUnknown,
		ObservationIssues: []hoststate.DiskObservationIssue{hoststate.DiskMissingModel, hoststate.DiskMissingCapacity, hoststate.DiskMissingIdentity},
	})
	for _, id := range []string{"observation:duplicate-1", "observation:duplicate-2"} {
		state.Disks = append(state.Disks, hoststate.Disk{
			ID: mustResourceID(id), Model: "Duplicate bridge", CapacityBytes: 512_000_000_000, Transport: hoststate.TransportSATA,
			Role: hoststate.DiskRoleUnassigned, Health: hoststate.HealthUnknown, SMARTStatus: hoststate.HealthUnknown,
			ObservationIssues: []hoststate.DiskObservationIssue{hoststate.DiskDuplicateIdentity},
		})
	}
	return newReader(state)
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
				TemperatureCelsius: new(36),
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
				TemperatureCelsius: new(31),
			},
		},
	}
}

func cloneState(state hoststate.State) hoststate.State {
	cloned := state
	if state.System.Storage != nil {
		cloned.System.Storage = new(*state.System.Storage)
	}
	cloned.Disks = make([]hoststate.Disk, len(state.Disks))
	for i, disk := range state.Disks {
		cloned.Disks[i] = disk
		if disk.TemperatureCelsius != nil {
			cloned.Disks[i].TemperatureCelsius = new(*disk.TemperatureCelsius)
		}
		if disk.FilesystemCapacityBytes != nil {
			value := *disk.FilesystemCapacityBytes
			cloned.Disks[i].FilesystemCapacityBytes = &value
		}
		if disk.FilesystemAvailableBytes != nil {
			value := *disk.FilesystemAvailableBytes
			cloned.Disks[i].FilesystemAvailableBytes = &value
		}
		if disk.FilesystemErrors != nil {
			value := *disk.FilesystemErrors
			cloned.Disks[i].FilesystemErrors = &value
		}
		cloned.Disks[i].Filesystems = append([]string(nil), disk.Filesystems...)
		cloned.Disks[i].Signatures = append([]hoststate.Signature(nil), disk.Signatures...)
		cloned.Disks[i].ObservationIssues = append([]hoststate.DiskObservationIssue(nil), disk.ObservationIssues...)
	}
	if state.DataVolume != nil {
		volume := *state.DataVolume
		volume.DiskIDs = append([]hoststate.ResourceID(nil), volume.DiskIDs...)
		volume.Data = append([]hoststate.ProfileUsage(nil), volume.Data...)
		volume.Metadata = append([]hoststate.ProfileUsage(nil), volume.Metadata...)
		cloned.DataVolume = &volume
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

var _ hoststate.Observer = (*Reader)(nil)
