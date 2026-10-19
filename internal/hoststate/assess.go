package hoststate

import (
	"slices"

	"github.com/zhongwater123/A-NAS/internal/capacity"
)

// A system is as healthy as the worst fact observed about it
// (docs/specs/system-health.md). Each Finding is one such fact, worded for
// the device owner by the web desktop.

// FindingCode names one reason a system is not plainly healthy.
type FindingCode string

const (
	// FindingDiskFailing: a disk's SMART self-assessment failed.
	FindingDiskFailing FindingCode = "disk_failing"
	// FindingDiskWarning: a disk reports a problem short of failing.
	FindingDiskWarning FindingCode = "disk_warning"
	// FindingDiskHealthUnknown: no SMART verdict for the system or data disk.
	FindingDiskHealthUnknown FindingCode = "disk_health_unknown"
	// FindingDiskHot: a disk is at or above its temperature limit; Value is
	// the temperature in °C.
	FindingDiskHot FindingCode = "disk_hot"
	// FindingVolumeOffline: a data volume was created but no data disk is
	// mounted.
	FindingVolumeOffline FindingCode = "volume_offline"
	// FindingVolumeReadOnly: the data volume is mounted read-only.
	FindingVolumeReadOnly FindingCode = "volume_read_only"
	// FindingVolumeFull: the data volume is inside the reserve writers keep
	// free, so uploads and new files are refused; Value is the percentage used.
	FindingVolumeFull FindingCode = "volume_full"
	// FindingVolumeNearlyFull: the data volume is 90% used or more; Value is
	// the percentage used.
	FindingVolumeNearlyFull FindingCode = "volume_nearly_full"
	// FindingVolumeErrors: Btrfs has counted errors on the data volume since
	// it was created; Value is their number.
	FindingVolumeErrors FindingCode = "volume_errors"
)

const (
	// hotRotationalCelsius and hotSolidStateCelsius are where a hard disk
	// and an SSD run hotter than they are made to: desktop hard disks are
	// rated to about 60 °C, NVMe drives start throttling near 70 °C.
	hotRotationalCelsius = 55
	hotSolidStateCelsius = 70
	// nearlyFullPercent is where the data volume needs the owner's attention.
	nearlyFullPercent = 90
)

// Finding is one fact that keeps a system from being plainly healthy.
type Finding struct {
	Code     FindingCode
	Severity Health
	// DiskID is the disk the finding is about; zero for the system.
	DiskID ResourceID
	// Value is the temperature, percentage or count the code describes.
	Value *int64
}

// Expectations are what the host state alone cannot tell.
type Expectations struct {
	// DataVolume is set once a data volume was created, so a data disk
	// should be mounted.
	DataVolume bool
}

// Assess returns how healthy the observed system is, with the reasons,
// most severe first. It is unknown only when no disk was observed.
func Assess(state State, expected Expectations) (Health, []Finding) {
	if len(state.Disks) == 0 {
		return HealthUnknown, nil
	}
	var findings []Finding
	add := func(code FindingCode, severity Health, disk ResourceID, value *int64) {
		findings = append(findings, Finding{Code: code, Severity: severity, DiskID: disk, Value: value})
	}
	dataDisk := false
	for _, disk := range state.Disks {
		inService := disk.Role == DiskRoleSystem || disk.Role == DiskRoleData
		switch disk.Health {
		case HealthCritical:
			add(FindingDiskFailing, HealthCritical, disk.ID, nil)
		case HealthWarning:
			add(FindingDiskWarning, HealthWarning, disk.ID, nil)
		case HealthHealthy:
		default:
			if inService {
				add(FindingDiskHealthUnknown, HealthWarning, disk.ID, nil)
			}
		}
		limit := hotSolidStateCelsius
		if disk.Rotational {
			limit = hotRotationalCelsius
		}
		if disk.TemperatureCelsius != nil && *disk.TemperatureCelsius >= limit {
			add(FindingDiskHot, HealthWarning, disk.ID, int64Pointer(int64(*disk.TemperatureCelsius)))
		}
		if disk.Role != DiskRoleData {
			continue
		}
		dataDisk = true
		if disk.FilesystemReadOnly {
			add(FindingVolumeReadOnly, HealthCritical, disk.ID, nil)
		}
		if disk.FilesystemCapacityBytes != nil && disk.FilesystemAvailableBytes != nil && *disk.FilesystemCapacityBytes > 0 {
			total, available := *disk.FilesystemCapacityBytes, min(*disk.FilesystemAvailableBytes, *disk.FilesystemCapacityBytes)
			used := total - available
			percent := int64Pointer(int64((used*100 + total/2) / total))
			switch {
			case available < capacity.Reserve(total):
				add(FindingVolumeFull, HealthCritical, disk.ID, percent)
			case used*100 >= total*nearlyFullPercent:
				add(FindingVolumeNearlyFull, HealthWarning, disk.ID, percent)
			}
		}
		if disk.FilesystemErrors != nil && *disk.FilesystemErrors > 0 {
			add(FindingVolumeErrors, HealthWarning, disk.ID, int64Pointer(int64(*disk.FilesystemErrors)))
		}
	}
	if expected.DataVolume && !dataDisk {
		add(FindingVolumeOffline, HealthCritical, ResourceID{}, nil)
	}
	slices.SortStableFunc(findings, func(left, right Finding) int {
		return severityRank(right.Severity) - severityRank(left.Severity)
	})
	health := HealthHealthy
	if len(findings) > 0 {
		health = findings[0].Severity
	}
	return health, findings
}

func severityRank(health Health) int {
	switch health {
	case HealthCritical:
		return 2
	case HealthWarning:
		return 1
	default:
		return 0
	}
}

func int64Pointer(value int64) *int64 { return &value }
