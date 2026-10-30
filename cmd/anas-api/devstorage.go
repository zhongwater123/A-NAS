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
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
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

	mu         sync.Mutex
	volume     *simulatedVolume
	operations map[string]*simulatedOperation
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
	step := int(s.now().Sub(operation.started) / simulatedStep)
	if step < len(operation.steps) {
		report.Step = operation.steps[step]
		return report
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
