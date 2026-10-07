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
}

type Disk struct {
	ID                 ResourceID
	Model              string
	Transport          Transport
	CapacityBytes      uint64
	Rotational         bool
	Removable          bool
	InUse              bool
	Filesystems        []string
	Role               DiskRole
	Health             Health
	SMARTStatus        Health
	TemperatureCelsius *int
}

type State struct {
	ObservedAt time.Time
	System     System
	Disks      []Disk
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
