package fake

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

// Development observations are deterministic and visibly use fake names.
func (r *Reader) ReadDeviceStatus(ctx context.Context) (taskmanager.DeviceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return taskmanager.DeviceSnapshot{}, err
	}
	if r.err != nil {
		return taskmanager.DeviceSnapshot{}, r.err
	}
	metrics := r.metrics
	disks := []taskmanager.Disk{}
	for _, disk := range r.state.Disks {
		disks = append(disks, taskmanager.Disk{ID: disk.ID.String(), Model: disk.Model, CapacityBytes: disk.CapacityBytes, ReadBytesPerSecond: taskFloat(1048576), WriteBytesPerSecond: taskFloat(262144)})
	}
	return taskmanager.DeviceSnapshot{ObservedAt: time.Now().UTC(), SampleSeconds: 2, CPU: taskmanager.CPU{Model: "A-NAS Fake CPU", LogicalCores: metrics.CPU.LogicalCores, UsagePercent: taskFloat(metrics.CPU.UsagePercent)}, Memory: taskmanager.Memory{TotalBytes: metrics.Memory.TotalBytes, UsedBytes: metrics.Memory.UsedBytes}, Network: taskmanager.NetworkGroup{Status: taskmanager.Available, Items: []taskmanager.Network{{ID: "fake-eth0", Name: "eth0", Physical: true, ReceiveBytes: 100 << 30, TransmitBytes: 20 << 30, ReceiveBytesPerSecond: taskFloat(float64(metrics.Network.ReceiveBytesPerSecond)), TransmitBytesPerSecond: taskFloat(float64(metrics.Network.TransmitBytesPerSecond))}}}, Disks: taskmanager.DiskGroup{Status: taskmanager.Available, Items: disks}, GPU: taskmanager.AcceleratorGroup{Status: taskmanager.Unsupported, Items: []taskmanager.Accelerator{{ID: "gpu:fake-01", Name: "A-NAS Fake GPU", Driver: "fake", Status: taskmanager.Unsupported, UsageStatus: taskmanager.Unsupported, MemoryUsedStatus: taskmanager.Unsupported, MemoryTotalStatus: taskmanager.Unsupported}}}, NPU: taskmanager.AcceleratorGroup{Status: taskmanager.Absent, Items: []taskmanager.Accelerator{}}}, nil
}
func (r *Reader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return taskmanager.TaskSnapshot{}, err
	}
	if r.err != nil {
		return taskmanager.TaskSnapshot{}, r.err
	}
	return taskmanager.TaskSnapshot{ObservedAt: time.Now().UTC(), SampleSeconds: 2, Processes: taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{{ID: "process:1201:1", Name: "anas-api", PID: 1201, State: "S", CPUPercent: taskFloat(4.2), MemoryBytes: taskBytes(128 << 20), ReadBytesPerSecond: taskFloat(524288), WriteBytesPerSecond: taskFloat(131072)}, {ID: "process:1202:1", Name: "anas-host-agent", PID: 1202, State: "S", CPUPercent: taskFloat(0.4), MemoryBytes: taskBytes(24 << 20), ReadBytesPerSecond: taskFloat(0), WriteBytesPerSecond: taskFloat(0)}}}, Services: taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{{ID: "service:anas-api.service", Name: "anas-api.service", State: "running", CPUPercent: taskFloat(4.2), MemoryBytes: taskBytes(128 << 20), ReadBytesPerSecond: taskFloat(524288), WriteBytesPerSecond: taskFloat(131072)}, {ID: "service:anas-host-agent.service", Name: "anas-host-agent.service", State: "running", CPUPercent: taskFloat(0.4), MemoryBytes: taskBytes(24 << 20), ReadBytesPerSecond: taskFloat(0), WriteBytesPerSecond: taskFloat(0)}}}}, nil
}
func taskFloat(v float64) *float64 { return &v }
func taskBytes(v uint64) *uint64   { return &v }

var _ taskmanager.Reader = (*Reader)(nil)
