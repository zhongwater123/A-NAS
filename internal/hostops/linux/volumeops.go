package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/storage"
)

// volumeOperation is one plan's data-volume operation in the journal at
// /var/lib/a-nas/volume-operations.json: what the plan asked for, the step
// it reached and how it ended. The journal outlives the Host Agent, so an
// operation a restart cut short reads as interrupted and its plan can be
// retried from where it stopped (issue #165).
type volumeOperation struct {
	PlanID string               `json:"planId"`
	Kind   storage.PlanKind     `json:"kind"`
	Disks  []storage.DiskTarget `json:"disks"`
	Layout storage.Layout       `json:"layout"`
	// FilesystemUUID is the data volume disks are added to.
	FilesystemUUID string `json:"filesystemUuid,omitempty"`
	// Formatted are the filesystems a volume creation made, in order: a
	// retry before the volume mounted formats the disks again.
	Formatted []string               `json:"formatted,omitempty"`
	State     storage.OperationState `json:"state"`
	Step      storage.OperationStep  `json:"step,omitempty"`
	Failure   string                 `json:"failure,omitempty"`
	Volume    *storage.Volume        `json:"volume,omitempty"`
	StartedAt time.Time              `json:"startedAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

type operationsDocument struct {
	Operations []*volumeOperation `json:"operations"`
}

// StartCreateVolume starts erasing the planned disks and formatting them as
// the data volume in the background.
func (e *Executor) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	if e.resolver == nil || strings.TrimSpace(request.PlanID) == "" || len(request.Disks) == 0 || !request.Layout.Fits(len(request.Disks)) {
		return storage.VolumeOperation{}, errors.New("invalid create-volume request")
	}
	return e.startOperation(volumeOperation{
		PlanID: request.PlanID, Kind: storage.PlanKindCreateVolume, Disks: request.Disks, Layout: request.Layout,
	}, e.createVolume)
}

// StartAddDisks starts erasing the planned disks and adding them to the
// mounted data volume in the background.
func (e *Executor) StartAddDisks(_ context.Context, request storage.AddDisksRequest) (storage.VolumeOperation, error) {
	if e.resolver == nil || strings.TrimSpace(request.PlanID) == "" || len(request.Disks) == 0 || request.Layout == storage.LayoutSingle ||
		!request.Layout.Fits(len(request.Disks)+1) || !filesystemUUIDPattern.MatchString(request.FilesystemUUID) {
		return storage.VolumeOperation{}, errors.New("invalid add-disks request")
	}
	return e.startOperation(volumeOperation{
		PlanID: request.PlanID, Kind: storage.PlanKindAddDisks, Disks: request.Disks, Layout: request.Layout, FilesystemUUID: request.FilesystemUUID,
	}, e.addDisks)
}

// VolumeOperation reports the plan's operation from the journal.
func (e *Executor) VolumeOperation(_ context.Context, planID string) (storage.VolumeOperation, error) {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	if e.operationsError != nil {
		return storage.VolumeOperation{}, e.operationsError
	}
	operation, ok := e.operations[planID]
	if !ok {
		return storage.VolumeOperation{}, storage.ErrOperationNotFound
	}
	return operation.report(), nil
}

// startOperation runs the plan's operation unless it runs or succeeded
// already; one that failed or was interrupted runs again and continues
// after the steps that are done. Another plan's operation must not run.
func (e *Executor) startOperation(requested volumeOperation, run func(context.Context, *volumeOperation) (storage.Volume, error)) (storage.VolumeOperation, error) {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	if e.operationsError != nil {
		return storage.VolumeOperation{}, e.operationsError
	}
	operation, known := e.operations[requested.PlanID]
	if known {
		if !operation.asks(requested) {
			return storage.VolumeOperation{}, fmt.Errorf("%w: the plan asked for another operation before", storage.ErrPlanState)
		}
		if operation.State == storage.OperationRunning || operation.State == storage.OperationSucceeded {
			return operation.report(), nil
		}
	}
	if e.operating != "" {
		return storage.VolumeOperation{}, fmt.Errorf("%w: another data-volume operation is running", storage.ErrPlanState)
	}
	now := e.now().UTC()
	if !known {
		operation = &requested
		operation.StartedAt = now
		e.operations[operation.PlanID] = operation
	}
	previous := *operation
	operation.State, operation.Failure, operation.UpdatedAt = storage.OperationRunning, "", now
	if err := e.saveOperationsLocked(); err != nil {
		if known {
			*operation = previous
		} else {
			delete(e.operations, operation.PlanID)
		}
		return storage.VolumeOperation{}, fmt.Errorf("record the data-volume operation: %w", err)
	}
	e.operating = operation.PlanID
	go e.runOperation(operation, run)
	return operation.report(), nil
}

// runOperation runs the steps apart from the request that started them, so
// that the caller going away does not kill a disk command halfway. Stopping
// the Host Agent still stops them; the journal then reads interrupted.
func (e *Executor) runOperation(operation *volumeOperation, run func(context.Context, *volumeOperation) (storage.Volume, error)) {
	volume, err := run(context.Background(), operation)
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	e.operating = ""
	if err != nil {
		operation.State, operation.Failure = storage.OperationFailed, publicFailure(operation.Step, err)
		e.logger.Error("data-volume operation failed", "plan", operation.PlanID, "kind", operation.Kind, "step", operation.Step, "error", err)
	} else {
		operation.State, operation.Step, operation.Volume = storage.OperationSucceeded, "", &volume
		e.logger.Info("data-volume operation succeeded", "plan", operation.PlanID, "kind", operation.Kind)
	}
	operation.UpdatedAt = e.now().UTC()
	if err := e.saveOperationsLocked(); err != nil {
		e.logger.Error("record the end of a data-volume operation", "plan", operation.PlanID, "error", err)
	}
}

// publicFailure is what the product may show of a failure: command output
// names device paths, which stay in the Host Agent's log.
func publicFailure(step storage.OperationStep, err error) string {
	for _, known := range []error{
		storage.ErrDiskChanged, storage.ErrDiskNotEligible, storage.ErrVolumeExists, storage.ErrVolumeChanged, storage.ErrVolumeUnavailable,
	} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if step == "" {
		return "the operation failed before changing any disk; the Host Agent's system log has the details"
	}
	return fmt.Sprintf("the %s step failed; the Host Agent's system log has the details", step)
}

// enter records that the operation starts the step.
func (e *Executor) enter(operation *volumeOperation, step storage.OperationStep) {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	operation.Step, operation.UpdatedAt = step, e.now().UTC()
	if err := e.saveOperationsLocked(); err != nil {
		e.logger.Warn("record the step of a data-volume operation", "plan", operation.PlanID, "step", step, "error", err)
	}
}

// recordFormatted records the filesystem the operation formatted before a
// mount unit names it: the unit may be replaced later only because of it.
func (e *Executor) recordFormatted(operation *volumeOperation, uuid string) error {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	operation.Formatted = append(operation.Formatted, uuid)
	operation.UpdatedAt = e.now().UTC()
	return e.saveOperationsLocked()
}

// formattedByUnfinishedCreation reports whether a volume creation that never
// succeeded formatted the filesystem: nothing was ever stored on it.
func (e *Executor) formattedByUnfinishedCreation(uuid string) bool {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	for _, operation := range e.operations {
		if operation.Kind == storage.PlanKindCreateVolume && operation.State != storage.OperationSucceeded &&
			slices.ContainsFunc(operation.Formatted, func(formatted string) bool { return strings.EqualFold(formatted, uuid) }) {
			return true
		}
	}
	return false
}

// formattedBy reports whether the operation formatted the filesystem.
func (e *Executor) formattedBy(operation *volumeOperation, uuid string) bool {
	e.operationsMu.Lock()
	defer e.operationsMu.Unlock()
	return slices.ContainsFunc(operation.Formatted, func(formatted string) bool { return strings.EqualFold(formatted, uuid) })
}

// asks reports whether a request for the plan asks for this operation.
func (o *volumeOperation) asks(request volumeOperation) bool {
	return o.Kind == request.Kind && o.Layout == request.Layout && strings.EqualFold(o.FilesystemUUID, request.FilesystemUUID) &&
		slices.Equal(o.Disks, request.Disks)
}

func (o *volumeOperation) report() storage.VolumeOperation {
	report := storage.VolumeOperation{PlanID: o.PlanID, State: o.State, Step: o.Step, Failure: o.Failure}
	if o.Volume != nil {
		volume := *o.Volume
		volume.DiskIDs = append([]string(nil), volume.DiskIDs...)
		report.Volume = &volume
	}
	return report
}

// loadOperations reads the journal. An operation it lists as running was
// cut short when the Host Agent last stopped.
func (e *Executor) loadOperations() error {
	e.operations = make(map[string]*volumeOperation)
	contents, err := os.ReadFile(e.operationsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var document operationsDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("decode the data-volume operation journal: %w", err)
	}
	interrupted := false
	for _, operation := range document.Operations {
		if operation == nil || strings.TrimSpace(operation.PlanID) == "" {
			return errors.New("the data-volume operation journal names an operation without a plan")
		}
		if operation.State == storage.OperationRunning {
			operation.State, interrupted = storage.OperationInterrupted, true
			operation.Failure = "the Host Agent stopped before the operation changed any disk; retrying starts it again"
			if operation.Step != "" {
				operation.Failure = "the Host Agent stopped during the " + string(operation.Step) + " step; retrying continues after the steps that are done"
			}
		}
		e.operations[operation.PlanID] = operation
	}
	if interrupted {
		return e.saveOperationsLocked()
	}
	return nil
}

func (e *Executor) saveOperationsLocked() error {
	document := operationsDocument{Operations: make([]*volumeOperation, 0, len(e.operations))}
	for _, operation := range e.operations {
		document.Operations = append(document.Operations, operation)
	}
	sort.Slice(document.Operations, func(i, j int) bool {
		if !document.Operations[i].StartedAt.Equal(document.Operations[j].StartedAt) {
			return document.Operations[i].StartedAt.Before(document.Operations[j].StartedAt)
		}
		return document.Operations[i].PlanID < document.Operations[j].PlanID
	})
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.operationsPath, append(contents, '\n'), 0o600)
}
