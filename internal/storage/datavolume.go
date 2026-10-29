package storage

import (
	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// The data volume's values and the executor port live in
// internal/datavolume, which the root Host Agent links without this package
// (#221); these names keep this package's callers as they were.
type (
	Layout              = datavolume.Layout
	PlanKind            = datavolume.PlanKind
	VolumeState         = datavolume.VolumeState
	Volume              = datavolume.Volume
	ConversionState     = datavolume.ConversionState
	Conversion          = datavolume.Conversion
	DiskTarget          = datavolume.DiskTarget
	CreateVolumeRequest = datavolume.CreateVolumeRequest
	AddDisksRequest     = datavolume.AddDisksRequest
	VolumeExecutor      = datavolume.VolumeExecutor
	OperationState      = datavolume.OperationState
	OperationStep       = datavolume.OperationStep
	VolumeOperation     = datavolume.VolumeOperation
)

const (
	LayoutSingle           = datavolume.LayoutSingle
	LayoutMirror           = datavolume.LayoutMirror
	LayoutSpan             = datavolume.LayoutSpan
	LayoutStripe           = datavolume.LayoutStripe
	PlanKindCreateVolume   = datavolume.PlanKindCreateVolume
	PlanKindAddDisks       = datavolume.PlanKindAddDisks
	VolumeStateCreating    = datavolume.VolumeStateCreating
	VolumeStateAvailable   = datavolume.VolumeStateAvailable
	VolumeStateUnavailable = datavolume.VolumeStateUnavailable
	VolumeStateReadOnly    = datavolume.VolumeStateReadOnly
	ConversionRunning      = datavolume.ConversionRunning
	ConversionWaiting      = datavolume.ConversionWaiting
	OperationRunning       = datavolume.OperationRunning
	OperationSucceeded     = datavolume.OperationSucceeded
	OperationFailed        = datavolume.OperationFailed
	OperationInterrupted   = datavolume.OperationInterrupted
	StepErase              = datavolume.StepErase
	StepFormat             = datavolume.StepFormat
	StepMount              = datavolume.StepMount
	StepPrepare            = datavolume.StepPrepare
	StepAddDevices         = datavolume.StepAddDevices
)

var (
	ErrDiskNotEligible   = datavolume.ErrDiskNotEligible
	ErrPlanState         = datavolume.ErrPlanState
	ErrDiskChanged       = datavolume.ErrDiskChanged
	ErrVolumeChanged     = datavolume.ErrVolumeChanged
	ErrVolumeUnavailable = datavolume.ErrVolumeUnavailable
	ErrNoConversion      = datavolume.ErrNoConversion
	ErrVolumeExists      = datavolume.ErrVolumeExists
	ErrOperationNotFound = datavolume.ErrOperationNotFound
)

// UsableBytes is datavolume.UsableBytes.
func UsableBytes(layout Layout, sizes []uint64) uint64 { return datavolume.UsableBytes(layout, sizes) }

// OutsideLayout is datavolume.OutsideLayout.
func OutsideLayout(data, metadata []hoststate.ProfileUsage, layout Layout) (convertData, convertMetadata bool) {
	return datavolume.OutsideLayout(data, metadata, layout)
}

// FingerprintDisk is datavolume.FingerprintDisk.
func FingerprintDisk(disk hoststate.Disk) string { return datavolume.FingerprintDisk(disk) }
