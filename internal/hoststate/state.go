// Package hoststate defines the read-only state observed from an A-NAS host.
package hoststate

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidResourceID = errors.New("invalid stable resource ID")

// ResourceID is an opaque, path-independent identity for a host resource.
type ResourceID struct {
	value string
}

func NewResourceID(value string) (ResourceID, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, `/\\`) {
		return ResourceID{}, ErrInvalidResourceID
	}
	return ResourceID{value: value}, nil
}

func (id ResourceID) String() string {
	return id.value
}

type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthWarning  Health = "warning"
	HealthCritical Health = "critical"
	HealthUnknown  Health = "unknown"
)

type DiskRole string

const (
	DiskRoleSystem     DiskRole = "system"
	DiskRoleData       DiskRole = "data"
	DiskRoleUnassigned DiskRole = "unassigned"
)

type Transport string

const (
	TransportNVMe    Transport = "nvme"
	TransportSATA    Transport = "sata"
	TransportUSB     Transport = "usb"
	TransportUnknown Transport = "unknown"
)

type OperatingSystem struct {
	Name    string
	Version string
}

type System struct {
	ID              ResourceID
	Hostname        string
	OperatingSystem OperatingSystem
	Architecture    string
	UptimeSeconds   uint64
	Health          Health
	// Storage observes the filesystem holding system state, independently of
	// the hardware disk size and the data volume. Nil means not observed.
	Storage *StorageUsage
	// Thermal is what the CPU and GPU sensors read; disks carry their own
	// temperatures. Nil means not observed, as from an older Host Agent.
	Thermal *Thermal
}

// Thermal is what the host's temperature sensors outside the disks read.
type Thermal struct {
	// CPU is the hottest CPU package; nil when no sensor reports one.
	CPU  *Temperature
	GPUs []GPU
}

// GPU is a graphics device and the temperature its driver reports.
type GPU struct {
	ID   ResourceID
	Name string
	// Integrated is a GPU inside the CPU package, which has no sensor of its
	// own: the CPU's temperature is its temperature.
	Integrated bool
	// Temperature is nil when the driver reports none.
	Temperature *Temperature
}

// Temperature is a sensor reading in whole degrees Celsius.
type Temperature struct {
	Celsius int
	// HotCelsius is where the reading counts as running hot, some way below
	// where the chip throttles (docs/specs/device-temperatures.md).
	HotCelsius int
}

// Valid reports whether the reading is a plausible temperature.
func (t Temperature) Valid() bool {
	return PlausibleCelsius(t.Celsius) && t.HotCelsius > 0 && PlausibleCelsius(t.HotCelsius)
}

// Sensors outside this range are misreporting, such as an unconnected
// thermistor reading -128 °C.
const (
	minCelsius = -40
	maxCelsius = 150
)

// PlausibleCelsius reports whether a sensor reading can be a real
// temperature of the hardware A-NAS runs on.
func PlausibleCelsius(celsius int) bool {
	return celsius >= minCelsius && celsius <= maxCelsius
}

type StorageUsage struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

type Disk struct {
	// ObservationIssues prevent this observation from being used for storage
	// operations. Unidentified disks have display-only observation:* IDs,
	// never a replacement identity derived from a kernel device path.
	ObservationIssues        []DiskObservationIssue
	ID                       ResourceID
	Model                    string
	Transport                Transport
	CapacityBytes            uint64
	FilesystemCapacityBytes  *uint64
	FilesystemAvailableBytes *uint64
	Rotational               bool
	Removable                bool
	InUse                    bool
	Filesystems              []string
	Role                     DiskRole
	Health                   Health
	SMARTStatus              Health
	TemperatureCelsius       *int
	// FilesystemReadOnly and FilesystemErrors describe the data volume on a
	// data disk: whether it is mounted read-only, as Btrfs does after an
	// error, and the read, write, flush, checksum and generation errors
	// Btrfs has counted on it since it was created; nil when not observed.
	FilesystemReadOnly bool
	FilesystemErrors   *uint64
	// Signatures are the partition tables, filesystems and storage-stack
	// memberships found on the disk, in use or left behind by another system.
	Signatures []Signature
}

// DiskObservationIssue is a local device problem, not a failed host read.
type DiskObservationIssue string

const (
	DiskMissingIdentity   DiskObservationIssue = "missing_identity"
	DiskDuplicateIdentity DiskObservationIssue = "duplicate_identity"
	DiskMissingModel      DiskObservationIssue = "missing_model"
	DiskMissingCapacity   DiskObservationIssue = "missing_capacity"
	DiskReadOnly          DiskObservationIssue = "read_only"
	DiskInvalidDeviceName DiskObservationIssue = "invalid_device_name"
)

func (issue DiskObservationIssue) Valid() bool {
	switch issue {
	case DiskMissingIdentity, DiskDuplicateIdentity, DiskMissingModel, DiskMissingCapacity, DiskReadOnly, DiskInvalidDeviceName:
		return true
	default:
		return false
	}
}

// Signature is one on-disk format blkid recognised, such as btrfs or
// linux_raid_member, with the label and UUID it carries.
type Signature struct {
	Type  string
	Label string
	UUID  string
}

// Profile is a Btrfs block-group profile.
type Profile string

const (
	ProfileSingle  Profile = "single"
	ProfileDup     Profile = "dup"
	ProfileRAID0   Profile = "raid0"
	ProfileRAID1   Profile = "raid1"
	ProfileRAID1C3 Profile = "raid1c3"
	ProfileRAID1C4 Profile = "raid1c4"
	ProfileRAID10  Profile = "raid10"
	ProfileRAID5   Profile = "raid5"
	ProfileRAID6   Profile = "raid6"
)

func (p Profile) Valid() bool {
	switch p {
	case ProfileSingle, ProfileDup, ProfileRAID0, ProfileRAID1, ProfileRAID1C3, ProfileRAID1C4, ProfileRAID10, ProfileRAID5, ProfileRAID6:
		return true
	default:
		return false
	}
}

// ProfileUsage is how many bytes of data or metadata a profile holds.
type ProfileUsage struct {
	Profile   Profile
	UsedBytes uint64
}

// VolumeOperation is the exclusive Btrfs operation running on the data volume.
type VolumeOperation string

const (
	VolumeOperationNone          VolumeOperation = ""
	VolumeOperationBalance       VolumeOperation = "balance"
	VolumeOperationBalancePaused VolumeOperation = "balance_paused"
	VolumeOperationOther         VolumeOperation = "other"
)

func (o VolumeOperation) Valid() bool {
	switch o {
	case VolumeOperationNone, VolumeOperationBalance, VolumeOperationBalancePaused, VolumeOperationOther:
		return true
	default:
		return false
	}
}

// DataVolume is the Btrfs filesystem mounted as the data volume. Its disks
// are found by filesystem UUID, because the mount table names only one of
// them; Data and Metadata hold more than one profile while a conversion runs.
type DataVolume struct {
	FilesystemUUID string
	DiskIDs        []ResourceID
	Data           []ProfileUsage
	Metadata       []ProfileUsage
	Operation      VolumeOperation
}

type State struct {
	ObservedAt time.Time
	System     System
	Disks      []Disk
	// DataVolume is nil when nothing is mounted as the data volume.
	DataVolume *DataVolume
	// ReconcileFailed says the Host Agent's last reconciliation of the data
	// volume failed: repairing the space permissions, writing the identity
	// manifest onto it or refreshing the Samba configuration for it. The Host
	// Agent keeps serving and tries again (#166).
	ReconcileFailed bool
}

// Reader returns one point-in-time observation of the host and its disks.
type Reader interface {
	Read(ctx context.Context) (State, error)
}

// Metrics is a short-interval utilisation observation. CPU usage and network
// rates cover the interval between two samples ending at ObservedAt.
type Metrics struct {
	ObservedAt time.Time
	CPU        CPUMetrics
	Memory     MemoryMetrics
	Network    NetworkMetrics
}

type CPUMetrics struct {
	UsagePercent float64
	LogicalCores int
}

// MemoryMetrics counts memory the kernel cannot make available without
// swapping as used, so reclaimable page cache is not reported as pressure.
type MemoryMetrics struct {
	TotalBytes uint64
	UsedBytes  uint64
}

// NetworkMetrics sums physical interfaces, excluding loopback and virtual links.
type NetworkMetrics struct {
	ReceiveBytesPerSecond  uint64
	TransmitBytesPerSecond uint64
}

// Valid reports whether the observation is internally consistent.
func (m Metrics) Valid() bool {
	return !m.ObservedAt.IsZero() &&
		m.CPU.UsagePercent >= 0 && m.CPU.UsagePercent <= 100 && m.CPU.LogicalCores > 0 &&
		m.Memory.TotalBytes > 0 && m.Memory.UsedBytes <= m.Memory.TotalBytes
}

// MetricsReader returns the latest utilisation observation of the host.
type MetricsReader interface {
	ReadMetrics(ctx context.Context) (Metrics, error)
}

// Observer provides both host state and utilisation metrics from one source.
type Observer interface {
	Reader
	MetricsReader
}
