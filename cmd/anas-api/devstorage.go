package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// simulatedConversion is how long a simulated conversion takes.
const simulatedConversion = 90 * time.Second

// simulatedStorage stands in for the Host Agent's data-volume operations in
// fake mode. Creating or growing the volume changes the simulated disks, and
// a conversion advances over simulatedConversion, so the storage settings
// can be tried end to end without a NAS. The volume holds 142 GiB from the
// start, so that a conversion has something to move.
type simulatedStorage struct {
	base hoststate.Observer
	now  func() time.Time

	mu     sync.Mutex
	volume *simulatedVolume
}

type simulatedVolume struct {
	diskIDs []string
	layout  storage.Layout
	from    storage.Layout
	started time.Time
}

const (
	simulatedUUID = "0f3c6a52-8d1e-4b7a-9c2d-5e6f7a8b9c0d"
	simulatedUsed = 142 << 30
)

func newSimulatedStorage(base hoststate.Observer) *simulatedStorage {
	return &simulatedStorage{base: base, now: time.Now}
}

func (s *simulatedStorage) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	return s.base.ReadMetrics(ctx)
}

func (s *simulatedStorage) Read(ctx context.Context) (hoststate.State, error) {
	state, err := s.base.Read(ctx)
	if err != nil {
		return state, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.volume == nil {
		return state, nil
	}
	volume := &hoststate.DataVolume{FilesystemUUID: simulatedUUID}
	var sizes []uint64
	first := -1
	for i, disk := range state.Disks {
		if !slices.Contains(s.volume.diskIDs, disk.ID.String()) {
			continue
		}
		state.Disks[i].Role, state.Disks[i].InUse = hoststate.DiskRoleData, true
		state.Disks[i].Filesystems = []string{"btrfs"}
		state.Disks[i].Signatures = []hoststate.Signature{{Type: "btrfs", Label: "ANAS_DATA", UUID: simulatedUUID}}
		volume.DiskIDs = append(volume.DiskIDs, disk.ID)
		sizes = append(sizes, disk.CapacityBytes)
		if first < 0 {
			first = i
		}
	}
	if first < 0 {
		return state, nil
	}
	total := storage.UsableBytes(s.volume.layout, sizes) * 99 / 100
	available := total - min(total, simulatedUsed)
	state.Disks[first].FilesystemCapacityBytes, state.Disks[first].FilesystemAvailableBytes = &total, &available

	done := 1.0
	if !s.volume.started.IsZero() {
		done = min(1, float64(s.now().Sub(s.volume.started))/float64(simulatedConversion))
	}
	split := func(used uint64, from, to hoststate.Profile) []hoststate.ProfileUsage {
		converted := uint64(float64(used) * done)
		if from == to || converted == used {
			return []hoststate.ProfileUsage{{Profile: to, UsedBytes: used}}
		}
		return []hoststate.ProfileUsage{{Profile: from, UsedBytes: used - converted}, {Profile: to, UsedBytes: converted}}
	}
	fromData, fromMetadata := s.volume.from.Profiles()
	toData, toMetadata := s.volume.layout.Profiles()
	volume.Data = split(simulatedUsed, fromData, toData)
	volume.Metadata = split(simulatedUsed/100, fromMetadata, toMetadata)
	if done < 1 {
		volume.Operation = hoststate.VolumeOperationBalance
	}
	state.DataVolume = volume
	return state, nil
}

func (s *simulatedStorage) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.volume != nil {
		return storage.Volume{}, errors.New("the simulated data volume exists")
	}
	volume := &simulatedVolume{layout: request.Layout, from: request.Layout}
	for _, disk := range request.Disks {
		volume.diskIDs = append(volume.diskIDs, disk.DiskID)
	}
	s.volume = volume
	return storage.Volume{
		ID: "volume:data", DiskIDs: append([]string(nil), volume.diskIDs...), FilesystemUUID: simulatedUUID,
		Layout: request.Layout, State: storage.VolumeStateAvailable,
	}, nil
}

func (s *simulatedStorage) AddDisks(_ context.Context, request storage.AddDisksRequest) (storage.Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.volume == nil || request.FilesystemUUID != simulatedUUID {
		return storage.Volume{}, storage.ErrVolumeChanged
	}
	for _, disk := range request.Disks {
		s.volume.diskIDs = append(s.volume.diskIDs, disk.DiskID)
	}
	s.volume.from, s.volume.layout, s.volume.started = s.volume.layout, request.Layout, s.now()
	return storage.Volume{FilesystemUUID: simulatedUUID, Layout: request.Layout, State: storage.VolumeStateAvailable}, nil
}

func (s *simulatedStorage) ContinueConversion(context.Context) error { return nil }
