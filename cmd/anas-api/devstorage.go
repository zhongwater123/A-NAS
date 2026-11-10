package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

// simulatedConversion is how long a simulated conversion takes.
const simulatedConversion = 90 * time.Second

// simulatedRebuild is how long rebuilding a mirror onto a new disk takes,
// and simulatedRestore how long the volume then restores its redundancy, so
// that the settings page shows the rebuild's progress and then the volume
// restoring.
const (
	simulatedRebuild = 20 * time.Second
	simulatedRestore = time.Minute
)

// fakeStorageVariable picks the simulated host in fake mode; empty is the
// default host with one spare disk.
const fakeStorageVariable = "ANAS_FAKE_STORAGE"

// fakeStorageDegraded is a host with disks to replace a missing one, where a
// volume created on two disks or more loses its last disk as soon as it is
// created: a mirror then runs without it and takes a new disk in its place,
// a span or stripe goes offline.
const fakeStorageDegraded = "degraded"

// developmentStorage is the simulated storage of the host that mode names.
func developmentStorage(mode string) (*simulatedStorage, error) {
	switch mode {
	case "":
		return newSimulatedStorage(fake.NewWithSpareDisk()), nil
	case fakeStorageDegraded:
		simulated := newSimulatedStorage(fake.NewWithReplacementDisks())
		simulated.pullDisk = true
		return simulated, nil
	default:
		return nil, fmt.Errorf("%s must be empty or %s", fakeStorageVariable, fakeStorageDegraded)
	}
}

// simulatedStorage stands in for the Host Agent's data-volume operations in
// fake mode. Creating or growing the volume changes the simulated disks, and
// a conversion advances over simulatedConversion, so the storage settings
// can be tried end to end without a NAS. The volume holds 142 GiB from the
// start, so that a conversion has something to move.
type simulatedStorage struct {
	base hoststate.Observer
	now  func() time.Time
	// pullDisk takes the last disk out of a volume created on two disks or
	// more, as fakeStorageDegraded describes.
	pullDisk bool

	mu         sync.Mutex
	volume     *simulatedVolume
	operations map[string]*simulatedOperation
}

type simulatedVolume struct {
	diskIDs []string
	layout  storage.Layout
	from    storage.Layout
	started time.Time
	// pulled is the disk that was pulled out, which stays out of the host;
	// restored is when a new disk took its place.
	pulled   string
	restored time.Time
}

// missing reports whether the volume runs without its pulled disk.
func (v *simulatedVolume) missing() bool {
	return v.pulled != "" && v.restored.IsZero()
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

// The task manager observes the simulated host as the Fake Adapter does;
// without these the wrapper hid it and the page was always unavailable.
func (s *simulatedStorage) ReadDeviceStatus(ctx context.Context) (taskmanager.DeviceSnapshot, error) {
	tasks, ok := s.base.(taskmanager.Reader)
	if !ok {
		return taskmanager.DeviceSnapshot{}, errors.ErrUnsupported
	}
	return tasks.ReadDeviceStatus(ctx)
}

func (s *simulatedStorage) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	tasks, ok := s.base.(taskmanager.Reader)
	if !ok {
		return taskmanager.TaskSnapshot{}, errors.ErrUnsupported
	}
	return tasks.ReadTasks(ctx)
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
	// A pulled disk is gone from the host. Without it a mirror is mounted
	// degraded; a span or stripe is not mounted, and its other disks only
	// carry its signature.
	offline := s.volume.missing() && s.volume.layout != storage.LayoutMirror
	volume := &hoststate.DataVolume{FilesystemUUID: simulatedUUID}
	var sizes []uint64
	disks := make([]hoststate.Disk, 0, len(state.Disks))
	first := -1
	for _, disk := range state.Disks {
		member := slices.Contains(s.volume.diskIDs, disk.ID.String())
		if member {
			// Btrfs counts a missing disk in the volume's size.
			sizes = append(sizes, disk.CapacityBytes)
		}
		if disk.ID.String() == s.volume.pulled {
			continue
		}
		if !member {
			disks = append(disks, disk)
			continue
		}
		disk.Filesystems = []string{"btrfs"}
		disk.Signatures = []hoststate.Signature{{Type: "btrfs", Label: "ANAS_DATA", UUID: simulatedUUID}}
		if !offline {
			disk.Role, disk.InUse = hoststate.DiskRoleData, true
			volume.DiskIDs = append(volume.DiskIDs, disk.ID)
			if first < 0 {
				first = len(disks)
			}
		}
		disks = append(disks, disk)
	}
	state.Disks = disks
	if first < 0 {
		return state, nil
	}
	total := storage.UsableBytes(s.volume.layout, sizes) * 99 / 100
	available := total - min(total, simulatedUsed)
	state.Disks[first].FilesystemCapacityBytes, state.Disks[first].FilesystemAvailableBytes = &total, &available
	if s.volume.missing() {
		volume.MissingDevices = 1
	}
	volume.Restoring = !s.volume.restored.IsZero() && s.now().Sub(s.volume.restored) < simulatedRestore

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

// simulatedStep is how long each step of a simulated operation takes, so
// that the settings page shows the steps as it follows the operation.
const simulatedStep = 1500 * time.Millisecond

// simulatedOperation is a plan's operation; it takes effect when its last
// step ends.
type simulatedOperation struct {
	asked   simulatedRequest
	started time.Time
	steps   []storage.OperationStep
	apply   func()
	done    bool
}

// simulatedRequest is what a plan asked the Host Agent for; a plan that
// asks again must ask for the same, as the Host Agent's journal checks
// (hostops/linux volumeOperation.asks, #194).
type simulatedRequest struct {
	kind           storage.PlanKind
	disks          []storage.DiskTarget
	layout         storage.Layout
	filesystemUUID string
}

func (r simulatedRequest) same(other simulatedRequest) bool {
	return r.kind == other.kind && r.layout == other.layout && strings.EqualFold(r.filesystemUUID, other.filesystemUUID) &&
		slices.Equal(r.disks, other.disks)
}

func (s *simulatedStorage) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asked := simulatedRequest{kind: storage.PlanKindCreateVolume, disks: request.Disks, layout: request.Layout}
	if operation, known, err := s.knownLocked(request.PlanID, asked); known || err != nil {
		return operation, err
	}
	if s.volume != nil {
		return storage.VolumeOperation{}, storage.ErrVolumeExists
	}
	volume := &simulatedVolume{layout: request.Layout, from: request.Layout}
	for _, disk := range request.Disks {
		volume.diskIDs = append(volume.diskIDs, disk.DiskID)
	}
	return s.startLocked(request.PlanID, asked, []storage.OperationStep{storage.StepErase, storage.StepFormat, storage.StepMount, storage.StepPrepare}, func() {
		s.volume = volume
		if s.pullDisk && len(volume.diskIDs) > 1 {
			volume.pulled = volume.diskIDs[len(volume.diskIDs)-1]
		}
	}), nil
}

func (s *simulatedStorage) StartAddDisks(_ context.Context, request storage.AddDisksRequest) (storage.VolumeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asked := simulatedRequest{kind: storage.PlanKindAddDisks, disks: request.Disks, layout: request.Layout, filesystemUUID: request.FilesystemUUID}
	if operation, known, err := s.knownLocked(request.PlanID, asked); known || err != nil {
		return operation, err
	}
	if s.volume == nil || request.FilesystemUUID != simulatedUUID {
		return storage.VolumeOperation{}, storage.ErrVolumeChanged
	}
	return s.startLocked(request.PlanID, asked, []storage.OperationStep{storage.StepErase, storage.StepAddDevices}, func() {
		for _, disk := range request.Disks {
			s.volume.diskIDs = append(s.volume.diskIDs, disk.DiskID)
		}
		s.volume.from, s.volume.layout, s.volume.started = s.volume.layout, request.Layout, s.now()
	}), nil
}

func (s *simulatedStorage) StartReplaceDisk(_ context.Context, request storage.ReplaceDiskRequest) (storage.VolumeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asked := simulatedRequest{kind: storage.PlanKindReplaceDisk, disks: []storage.DiskTarget{request.Disk}, layout: storage.LayoutMirror, filesystemUUID: request.FilesystemUUID}
	if operation, known, err := s.knownLocked(request.PlanID, asked); known || err != nil {
		return operation, err
	}
	if s.volume == nil || request.FilesystemUUID != simulatedUUID || !s.volume.missing() {
		return storage.VolumeOperation{}, storage.ErrVolumeChanged
	}
	return s.startLocked(request.PlanID, asked, []storage.OperationStep{storage.StepErase, storage.StepReplace}, func() {
		pulled := s.volume.pulled
		s.volume.diskIDs = append(slices.DeleteFunc(s.volume.diskIDs, func(id string) bool { return id == pulled }), request.Disk.DiskID)
		s.volume.restored = s.now()
	}), nil
}

func (s *simulatedStorage) VolumeOperation(_ context.Context, planID string) (storage.VolumeOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, ok := s.operations[planID]
	if !ok {
		return storage.VolumeOperation{}, storage.ErrOperationNotFound
	}
	return s.reportLocked(planID, operation), nil
}

// knownLocked reports the plan's operation when the plan asked before, and
// refuses it when it asked for another operation then, or when another
// plan's operation is still running, as the Host Agent does.
func (s *simulatedStorage) knownLocked(planID string, asked simulatedRequest) (storage.VolumeOperation, bool, error) {
	if operation, ok := s.operations[planID]; ok {
		if !operation.asked.same(asked) {
			return storage.VolumeOperation{}, false, fmt.Errorf("%w: the plan asked for another operation before", storage.ErrPlanState)
		}
		return s.reportLocked(planID, operation), true, nil
	}
	for other, operation := range s.operations {
		if s.reportLocked(other, operation).State == storage.OperationRunning {
			return storage.VolumeOperation{}, false, fmt.Errorf("%w: another data-volume operation is running", storage.ErrPlanState)
		}
	}
	return storage.VolumeOperation{}, false, nil
}

func (s *simulatedStorage) startLocked(planID string, asked simulatedRequest, steps []storage.OperationStep, apply func()) storage.VolumeOperation {
	if s.operations == nil {
		s.operations = make(map[string]*simulatedOperation)
	}
	operation := &simulatedOperation{asked: asked, started: s.now(), steps: steps, apply: apply}
	s.operations[planID] = operation
	return s.reportLocked(planID, operation)
}

func (s *simulatedStorage) reportLocked(planID string, operation *simulatedOperation) storage.VolumeOperation {
	report := storage.VolumeOperation{PlanID: planID, State: storage.OperationRunning}
	elapsed := s.now().Sub(operation.started)
	for _, step := range operation.steps {
		length := simulatedStep
		if step == storage.StepReplace {
			length = simulatedRebuild
		}
		if elapsed < length {
			report.Step = step
			if step == storage.StepReplace {
				report.Progress = int(100 * elapsed / length)
			}
			return report
		}
		elapsed -= length
	}
	if !operation.done {
		operation.apply()
		operation.done = true
	}
	report.State = storage.OperationSucceeded
	report.Volume = &storage.Volume{ID: "volume:data", FilesystemUUID: simulatedUUID, State: storage.VolumeStateAvailable}
	return report
}

func (s *simulatedStorage) ContinueConversion(context.Context) error { return nil }
