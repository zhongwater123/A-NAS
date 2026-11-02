package taskmanager

import (
	"math"
	"testing"
	"time"
)

func validDevice() DeviceSnapshot {
	value := 25.0
	return DeviceSnapshot{ObservedAt: time.Now(), SampleSeconds: 0.5,
		CPU: CPU{Model: "Test CPU", LogicalCores: 4, UsagePercent: &value}, Memory: Memory{TotalBytes: 100, UsedBytes: 25},
		Network: NetworkGroup{Status: Available, Items: []Network{}}, Disks: DiskGroup{Status: Available, Items: []Disk{}},
		GPU: AcceleratorGroup{Status: Absent, Items: []Accelerator{}}, NPU: AcceleratorGroup{Status: Absent, Items: []Accelerator{}},
		Fans: FanGroup{Status: Available, Items: []Fan{{ID: "fan:1", Number: 1, RPM: 1912}}}}
}

func TestDeviceValidation(t *testing.T) {
	d := validDevice()
	if !d.Valid() {
		t.Fatal("valid snapshot rejected")
	}
	for _, mutate := range []func(*DeviceSnapshot){
		func(d *DeviceSnapshot) { d.ObservedAt = time.Time{} },
		func(d *DeviceSnapshot) { d.SampleSeconds = 0 },
		func(d *DeviceSnapshot) { d.SampleSeconds = math.Inf(1) },
		func(d *DeviceSnapshot) { d.Memory.UsedBytes = 101 },
		func(d *DeviceSnapshot) { v := 101.0; d.CPU.UsagePercent = &v },
		func(d *DeviceSnapshot) { d.Network.Status = "invalid" },
		func(d *DeviceSnapshot) { d.GPU.Items = nil },
		func(d *DeviceSnapshot) { d.Fans.Items = nil },
		func(d *DeviceSnapshot) { d.Fans.Items[0].Stopped = true },
		func(d *DeviceSnapshot) { d.Fans.Items[0].RPM = 60000 },
		func(d *DeviceSnapshot) { d.CPU.Temperature = &Temperature{Celsius: 400, HotCelsius: 90} },
		func(d *DeviceSnapshot) { d.Disks.Items = []Disk{{ID: "x"}, {ID: "x"}} },
		func(d *DeviceSnapshot) { v := -1.0; d.Disks.Items = []Disk{{ID: "x", ReadBytesPerSecond: &v}} },
	} {
		d = validDevice()
		mutate(&d)
		if d.Valid() {
			t.Fatalf("invalid device accepted: %+v", d)
		}
	}
	d = validDevice()
	d.CPU.UsagePercent = nil
	if !d.Valid() {
		t.Fatal("unknown measurement should remain null")
	}
}

func TestTaskValidation(t *testing.T) {
	snapshot := TaskSnapshot{ObservedAt: time.Now(), SampleSeconds: 1, Processes: TaskGroup{Status: Available, Items: []Task{{ID: "1:2", Name: "worker", PID: 1, State: "running"}}}, Services: TaskGroup{Status: Unavailable, Items: []Task{}}}
	if !snapshot.Valid() {
		t.Fatal("unknown task metrics rejected")
	}
	snapshot.Processes.Items = append(snapshot.Processes.Items, snapshot.Processes.Items[0])
	if snapshot.Valid() {
		t.Fatal("duplicate task IDs accepted")
	}
	snapshot.Processes.Items = snapshot.Processes.Items[:1]
	value := math.NaN()
	snapshot.Processes.Items[0].CPUPercent = &value
	if snapshot.Valid() {
		t.Fatal("NaN accepted")
	}
}
