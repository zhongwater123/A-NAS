// Package taskmanager defines read-only device and workload observations.
// Missing metrics are null, never synthetic zeroes. No command lines, process
// environments, cgroup paths or device paths cross this boundary.
package taskmanager

import (
	"context"
	"time"
)

type Status string

const (
	Available   Status = "available"
	Partial     Status = "partial"
	Unavailable Status = "unavailable"
	Unsupported Status = "unsupported"
	Absent      Status = "absent"
)

type Reader interface {
	ReadDeviceStatus(context.Context) (DeviceSnapshot, error)
	ReadTasks(context.Context) (TaskSnapshot, error)
}

type DeviceSnapshot struct {
	ObservedAt    time.Time        `json:"observedAt"`
	SampleSeconds float64          `json:"sampleSeconds"`
	CPU           CPU              `json:"cpu"`
	Memory        Memory           `json:"memory"`
	Network       NetworkGroup     `json:"network"`
	Disks         DiskGroup        `json:"disks"`
	GPU           AcceleratorGroup `json:"gpu"`
	NPU           AcceleratorGroup `json:"npu"`
}

type CPU struct {
	Model        string   `json:"model"`
	LogicalCores int      `json:"logicalCores"`
	UsagePercent *float64 `json:"usagePercent"`
}

type Memory struct {
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

type NetworkGroup struct {
	Status Status    `json:"status"`
	Items  []Network `json:"items"`
}

type Network struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Physical               bool     `json:"physical"`
	ReceiveBytes           uint64   `json:"receiveBytes"`
	TransmitBytes          uint64   `json:"transmitBytes"`
	ReceiveBytesPerSecond  *float64 `json:"receiveBytesPerSecond"`
	TransmitBytesPerSecond *float64 `json:"transmitBytesPerSecond"`
}

type DiskGroup struct {
	Status Status `json:"status"`
	Items  []Disk `json:"items"`
}

type Disk struct {
	ID                  string   `json:"id"`
	Model               string   `json:"model"`
	CapacityBytes       uint64   `json:"capacityBytes"`
	ReadBytesPerSecond  *float64 `json:"readBytesPerSecond"`
	WriteBytesPerSecond *float64 `json:"writeBytesPerSecond"`
}

type AcceleratorGroup struct {
	Status Status        `json:"status"`
	Items  []Accelerator `json:"items"`
}

type Accelerator struct {
	UsageStatus       Status   `json:"usageStatus"`
	MemoryUsedStatus  Status   `json:"memoryUsedStatus"`
	MemoryTotalStatus Status   `json:"memoryTotalStatus"`
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Driver            string   `json:"driver"`
	Status            Status   `json:"status"`
	UsagePercent      *float64 `json:"usagePercent"`
	MemoryUsedBytes   *uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes  *uint64  `json:"memoryTotalBytes"`
}

type TaskSnapshot struct {
	Icons         map[string]string `json:"icons,omitempty"`
	ObservedAt    time.Time         `json:"observedAt"`
	SampleSeconds float64           `json:"sampleSeconds"`
	Processes     TaskGroup         `json:"processes"`
	Services      TaskGroup         `json:"services"`
}

type TaskGroup struct {
	Status Status `json:"status"`
	Items  []Task `json:"items"`
}

type Task struct {
	IconID              string   `json:"iconId,omitempty"`
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	PID                 int      `json:"pid"`
	State               string   `json:"state"`
	CPUPercent          *float64 `json:"cpuPercent"`
	MemoryBytes         *uint64  `json:"memoryBytes"`
	ReadBytesPerSecond  *float64 `json:"readBytesPerSecond"`
	WriteBytesPerSecond *float64 `json:"writeBytesPerSecond"`
}
