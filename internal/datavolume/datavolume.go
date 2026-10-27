// Package datavolume is what the storage service and the root Host Agent
// share about the data volume: layouts, the volume and the operations that
// make or grow it, and the executor that runs them on the host (ADR 0025).
// Plans, their store and the service stay in internal/storage, which the Host
// Agent does not link (#221).
package datavolume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

var (
	ErrDiskNotEligible   = errors.New("disk is not eligible for the data volume")
	ErrPlanState         = errors.New("execution plan is not in the required state")
	ErrDiskChanged       = errors.New("disk identity changed after planning")
	ErrVolumeChanged     = errors.New("data volume changed after planning")
	ErrVolumeUnavailable = errors.New("the data volume is not mounted and writable")
	ErrNoConversion      = errors.New("the data volume is not converting")
	// ErrVolumeExists: the host shows a data volume, or a mount unit for
	// one, that the plan did not make.
	ErrVolumeExists = errors.New("a data volume this plan did not make is already on the host")
	// ErrOperationNotFound: the host has no operation for the plan; it
	// never received it.
	ErrOperationNotFound = errors.New("the host has no operation for the plan")
)

// PlanKind is what a plan does to the data volume.
type PlanKind string

const (
	// PlanKindCreateVolume formats the disks as a new data volume.
	PlanKindCreateVolume PlanKind = "create_volume"
	// PlanKindAddDisks erases the disks and adds them to the data volume.
	PlanKindAddDisks PlanKind = "add_disks"
)

type VolumeState string

const (
	VolumeStateCreating    VolumeState = "creating"
	VolumeStateAvailable   VolumeState = "available"
	VolumeStateUnavailable VolumeState = "unavailable"
	VolumeStateReadOnly    VolumeState = "read_only"
)

type Volume struct {
	ID string `json:"id"`
	// DiskIDs are the disks holding the volume: those observed, or the
	// planned ones while it is not mounted.
	DiskIDs        []string    `json:"diskIds"`
	FilesystemUUID string      `json:"filesystemUuid,omitempty"`
	Layout         Layout      `json:"layout"`
	CapacityBytes  uint64      `json:"capacityBytes,omitempty"`
	AvailableBytes *uint64     `json:"availableBytes,omitempty"`
	State          VolumeState `json:"state"`
	// Conversion is set while data or metadata are outside the layout's
	// Btrfs profiles, after disks were added with another layout.
	Conversion *Conversion `json:"conversion,omitempty"`
}

// UnmarshalJSON also reads volumes stored when a volume had one disk.
func (v *Volume) UnmarshalJSON(data []byte) error {
	type document Volume
	var decoded struct {
		document
		DiskID string `json:"diskId"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*v = Volume(decoded.document)
	if len(v.DiskIDs) == 0 && decoded.DiskID != "" {
		v.DiskIDs = []string{decoded.DiskID}
	}
	if v.Layout == "" {
		v.Layout = LayoutSingle
	}
	return nil
}

type ConversionState string

const (
	// ConversionRunning: Btrfs is converting the volume now.
	ConversionRunning ConversionState = "running"
	// ConversionWaiting: the conversion stopped, for example at shutdown;
	// the Host Agent continues it at startup and every 15 minutes.
	ConversionWaiting ConversionState = "waiting"
)

type Conversion struct {
	State          ConversionState `json:"state"`
	ConvertedBytes uint64          `json:"convertedBytes"`
	TotalBytes     uint64          `json:"totalBytes"`
}

// DiskTarget is a disk the Host Agent erases, with the fingerprint it must
// still have.
type DiskTarget struct {
	DiskID      string `json:"diskId"`
	Fingerprint string `json:"fingerprint"`
}

type CreateVolumeRequest struct {
	PlanID string
	Disks  []DiskTarget
	Layout Layout
}

// AddDisksRequest adds disks to the mounted data volume with the
// filesystem UUID and converts it to the layout.
type AddDisksRequest struct {
	PlanID         string
	FilesystemUUID string
	Disks          []DiskTarget
	Layout         Layout
}

// VolumeExecutor runs a plan's operation on the host. Starting reports the
// operation without waiting for it to end; for a plan the host already has
// an operation for, it starts nothing new: it reports one that runs or
// succeeded, and continues one that failed or was interrupted after the
// steps that are done.
type VolumeExecutor interface {
	StartCreateVolume(context.Context, CreateVolumeRequest) (VolumeOperation, error)
	StartAddDisks(context.Context, AddDisksRequest) (VolumeOperation, error)
	// VolumeOperation reports the plan's operation, or ErrOperationNotFound.
	VolumeOperation(context.Context, string) (VolumeOperation, error)
	// ContinueConversion starts converting the data volume to its layout
	// again now, instead of at the Host Agent's next check.
	ContinueConversion(context.Context) error
}

// OperationState is how far the host has come with the operation a plan
// runs there.
type OperationState string

const (
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	// OperationFailed: a step failed. Retrying the plan checks the disks
	// again and continues after the steps that are done.
	OperationFailed OperationState = "failed"
	// OperationInterrupted: the Host Agent stopped while the operation ran,
	// at a restart or a power cut. Retrying continues it the same way.
	OperationInterrupted OperationState = "interrupted"
)

// OperationStep is the step an operation is at, or the one it stopped in.
type OperationStep string

const (
	StepErase      OperationStep = "erase"
	StepFormat     OperationStep = "format"
	StepMount      OperationStep = "mount"
	StepPrepare    OperationStep = "prepare"
	StepAddDevices OperationStep = "add_devices"
)

// VolumeOperation is a plan's operation on the host. The Host Agent runs it
// apart from the request that started it, so that closing the page, losing
// the connection or restarting the product service does not kill a disk
// command halfway (issue #165).
type VolumeOperation struct {
	PlanID  string         `json:"planId"`
	State   OperationState `json:"state"`
	Step    OperationStep  `json:"step,omitempty"`
	Failure string         `json:"failure,omitempty"`
	Volume  *Volume        `json:"volume,omitempty"`
}

func FingerprintDisk(disk hoststate.Disk) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", disk.ID.String(), disk.Model, disk.CapacityBytes, disk.Transport)))
	return hex.EncodeToString(digest[:16])
}
