package fake

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
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
		item := taskmanager.Disk{ID: disk.ID.String(), Model: disk.Model, CapacityBytes: disk.CapacityBytes, Rotational: disk.Rotational, ReadBytesPerSecond: taskFloat(1048576), WriteBytesPerSecond: taskFloat(262144)}
		if disk.TemperatureCelsius != nil {
			item.Temperature = &taskmanager.Temperature{Celsius: *disk.TemperatureCelsius, HotCelsius: hoststate.DiskHotCelsius(disk.Rotational)}
		}
		disks = append(disks, item)
	}
	var cpuTemperature *taskmanager.Temperature
	fans := taskmanager.FanGroup{Status: taskmanager.Absent, Items: []taskmanager.Fan{}}
	if thermal := r.state.System.Thermal; thermal != nil {
		if thermal.CPU != nil {
			cpuTemperature = &taskmanager.Temperature{Celsius: thermal.CPU.Celsius, HotCelsius: thermal.CPU.HotCelsius}
		}
		if len(thermal.Fans) > 0 {
			fans.Status = taskmanager.Available
		}
		for _, fan := range thermal.Fans {
			fans.Items = append(fans.Items, taskmanager.Fan{ID: fan.ID.String(), Number: fan.Number, Label: fan.Label, RPM: fan.RPM, Stopped: fan.Stopped})
		}
	}
	return taskmanager.DeviceSnapshot{ObservedAt: time.Now().UTC(), SampleSeconds: 2, CPU: taskmanager.CPU{Model: "A-NAS Fake CPU", LogicalCores: metrics.CPU.LogicalCores, UsagePercent: new(metrics.CPU.UsagePercent), Temperature: cpuTemperature}, Memory: taskmanager.Memory{TotalBytes: metrics.Memory.TotalBytes, UsedBytes: metrics.Memory.UsedBytes}, Network: taskmanager.NetworkGroup{Status: taskmanager.Available, Items: []taskmanager.Network{{ID: "fake-eth0", Name: "eth0", Physical: true, ReceiveBytes: 100 << 30, TransmitBytes: 20 << 30, ReceiveBytesPerSecond: new(float64(metrics.Network.ReceiveBytesPerSecond)), TransmitBytesPerSecond: new(float64(metrics.Network.TransmitBytesPerSecond))}}}, Disks: taskmanager.DiskGroup{Status: taskmanager.Available, Items: disks}, GPU: taskmanager.AcceleratorGroup{Status: taskmanager.Unsupported, Items: []taskmanager.Accelerator{{ID: "gpu:fake-01", Name: "A-NAS Fake GPU", Driver: "fake", Integrated: true, Status: taskmanager.Unsupported, UsageStatus: taskmanager.Unsupported, MemoryUsedStatus: taskmanager.Unsupported, MemoryTotalStatus: taskmanager.Unsupported}}}, NPU: taskmanager.AcceleratorGroup{Status: taskmanager.Absent, Items: []taskmanager.Accelerator{}}, Fans: fans}, nil
}
func (r *Reader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return taskmanager.TaskSnapshot{}, err
	}
	if r.err != nil {
		return taskmanager.TaskSnapshot{}, r.err
	}
	return taskmanager.TaskSnapshot{ObservedAt: time.Now().UTC(), SampleSeconds: 2, Processes: taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{{ID: "process:1201:1", Name: "anas-api", PID: 1201, State: "S", CPUPercent: new(4.2), MemoryBytes: taskBytes(128 << 20), ReadBytesPerSecond: taskFloat(524288), WriteBytesPerSecond: taskFloat(131072)}, {ID: "process:1202:1", Name: "anas-host-agent", PID: 1202, State: "S", CPUPercent: new(0.4), MemoryBytes: taskBytes(24 << 20), ReadBytesPerSecond: taskFloat(0), WriteBytesPerSecond: taskFloat(0)}}}, Services: taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{{ID: "service:anas-api.service", Name: "anas-api.service", State: "running", CPUPercent: new(4.2), MemoryBytes: taskBytes(128 << 20), ReadBytesPerSecond: taskFloat(524288), WriteBytesPerSecond: taskFloat(131072)}, {ID: "service:anas-host-agent.service", Name: "anas-host-agent.service", State: "running", CPUPercent: new(0.4), MemoryBytes: taskBytes(24 << 20), ReadBytesPerSecond: taskFloat(0), WriteBytesPerSecond: taskFloat(0)}}}}, nil
}

//go:fix inline
func taskFloat(v float64) *float64 { return new(v) }

//go:fix inline
func taskBytes(v uint64) *uint64 { return new(v) }

var _ taskmanager.Reader = (*Reader)(nil)
