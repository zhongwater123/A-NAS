package linux

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// debianRoot is the least a Debian host needs for a state read.
func debianRoot() fstest.MapFS {
	return fstest.MapFS{
		"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
		"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
		"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
	}
}

// newTestReader reads root, with one NVMe system disk unless
// readBlockDevices says otherwise, and no SMART.
func newTestReader(root fstest.MapFS, readBlockDevices func(context.Context) ([]byte, error)) *Reader {
	if readBlockDevices == nil {
		readBlockDevices = func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[{"name":"nvme0n1","type":"disk","size":125000000000,"model":"SSD","tran":"nvme","rota":false,"wwn":"eui.0001","mountpoints":["/"]}]}`), nil
		}
	}
	return newReader(dependencies{
		root:             root,
		hostname:         func() (string, error) { return "a-nas-dev", nil },
		architecture:     "amd64",
		now:              func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: readBlockDevices,
	})
}

func sensorFiles(root fstest.MapFS, directory string, files map[string]string) {
	for name, value := range files {
		root[directory+"/"+name] = &fstest.MapFile{Data: []byte(value + "\n")}
	}
}

// The CPU's temperature is its hottest package, as coretemp reports the
// i3-12100 of the Experimental NAS, and it is hot 10 °C below critical.
func TestCPUTemperatureIsTheHottestPackage(t *testing.T) {
	root := fstest.MapFS{}
	sensorFiles(root, "sys/class/hwmon/hwmon0", map[string]string{"name": "acpitz", "temp1_input": "27800"})
	sensorFiles(root, "sys/class/hwmon/hwmon1", map[string]string{"name": "nvme", "temp1_input": "57850", "temp1_label": "Composite"})
	sensorFiles(root, "sys/class/hwmon/hwmon2", map[string]string{
		"name":        "coretemp",
		"temp1_input": "63000", "temp1_label": "Package id 0", "temp1_crit": "100000", "temp1_max": "80000",
		"temp2_input": "71000", "temp2_label": "Core 0", "temp2_crit": "100000",
	})
	sensorFiles(root, "sys/class/hwmon/hwmon3", map[string]string{
		"name":        "coretemp",
		"temp1_input": "66000", "temp1_label": "Package id 1", "temp1_crit": "105000",
	})
	got := readCPUTemperature(root)
	if got == nil || *got != (hoststate.Temperature{Celsius: 66, HotCelsius: 95}) {
		t.Fatalf("readCPUTemperature() = %+v, want 66 °C, hot from 95 °C", got)
	}
}

// AMD's k10temp reports Tctl, which some CPUs offset, beside Tdie, and no
// critical temperature.
func TestCPUTemperaturePrefersTdieAndDefaultsItsLimit(t *testing.T) {
	root := fstest.MapFS{}
	sensorFiles(root, "sys/class/hwmon/hwmon0", map[string]string{
		"name":        "k10temp",
		"temp1_input": "70250", "temp1_label": "Tctl",
		"temp2_input": "60400", "temp2_label": "Tdie",
		"temp3_input": "40000", "temp3_label": "Tccd1",
	})
	got := readCPUTemperature(root)
	if got == nil || *got != (hoststate.Temperature{Celsius: 60, HotCelsius: defaultHotCelsius}) {
		t.Fatalf("readCPUTemperature() = %+v, want Tdie 60 °C", got)
	}
}

// Without coretemp loaded, an Intel CPU still has its package thermal zone.
func TestCPUTemperatureFallsBackToThePackageThermalZone(t *testing.T) {
	root := fstest.MapFS{}
	sensorFiles(root, "sys/class/thermal/thermal_zone0", map[string]string{"type": "acpitz", "temp": "27800"})
	sensorFiles(root, "sys/class/thermal/thermal_zone3", map[string]string{"type": "x86_pkg_temp", "temp": "62000"})
	got := readCPUTemperature(root)
	if got == nil || got.Celsius != 62 {
		t.Fatalf("readCPUTemperature() = %+v, want 62 °C", got)
	}
}

func TestCPUTemperatureIgnoresImplausibleReadings(t *testing.T) {
	root := fstest.MapFS{}
	sensorFiles(root, "sys/class/hwmon/hwmon0", map[string]string{"name": "cpu_thermal", "temp1_input": "-128000", "temp2_input": "not a number"})
	if got := readCPUTemperature(root); got != nil {
		t.Fatalf("readCPUTemperature() = %+v, want none", got)
	}
	if got := readCPUTemperature(fstest.MapFS{}); got != nil {
		t.Fatalf("readCPUTemperature() without sensors = %+v, want none", got)
	}
}

// An NVMe drive's temperature is its Composite reading, taken live in place
// of the SMART cache; its other sensors are not the drive's temperature.
func TestReaderReadsAnNVMeDriveTemperatureLive(t *testing.T) {
	root := debianRoot()
	sensorFiles(root, "sys/class/block/nvme0n1/device/hwmon1", map[string]string{
		"name":        "nvme",
		"temp1_input": "57850", "temp1_label": "Composite", "temp1_crit": "254850",
		"temp2_input": "73850", "temp2_label": "Sensor 1",
	})
	sensorFiles(root, "sys/class/block/sda/device/hwmon/hwmon4", map[string]string{"name": "drivetemp", "temp1_input": "41000"})
	reader := newTestReader(root, func(context.Context) ([]byte, error) {
		return []byte(`{"blockdevices":[
			{"name":"nvme0n1","type":"disk","size":125000000000,"model":"A-NAS System SSD","tran":"nvme","rota":false,"wwn":"eui.0001","serial":"SYS1","mountpoints":["/"]},
			{"name":"sda","type":"disk","size":512000000000,"model":"A-NAS Data HDD","tran":"sata","rota":true,"wwn":"0x5000","serial":"HDD1","mountpoints":[null]}
		]}`), nil
	})
	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, disk := range state.Disks {
		switch disk.Transport {
		case hoststate.TransportNVMe:
			if disk.TemperatureCelsius == nil || *disk.TemperatureCelsius != 58 {
				t.Fatalf("NVMe temperature = %v, want 58", disk.TemperatureCelsius)
			}
		default:
			// A SATA disk keeps to SMART, which leaves a sleeping disk asleep.
			if disk.TemperatureCelsius != nil {
				t.Fatalf("SATA temperature = %v, want none without a SMART reading", *disk.TemperatureCelsius)
			}
		}
	}
}

// A GPU reports its own sensor; Intel's integrated GPU has none and shares
// the CPU's.
func TestReaderReportsGPUTemperatures(t *testing.T) {
	root := debianRoot()
	sensorFiles(root, "sys/class/drm/card0/device", map[string]string{"uevent": "DRIVER=i915\nPCI_ID=8086:4692\nPCI_SLOT_NAME=0000:00:02.0"})
	sensorFiles(root, "sys/class/drm/card1/device", map[string]string{"uevent": "DRIVER=amdgpu\nPCI_ID=1002:73FF\nPCI_SLOT_NAME=0000:03:00.0"})
	sensorFiles(root, "sys/class/drm/card1/device/hwmon/hwmon5", map[string]string{
		"name":        "amdgpu",
		"temp1_input": "52000", "temp1_label": "edge", "temp1_crit": "100000",
		"temp2_input": "61000", "temp2_label": "junction", "temp2_crit": "110000",
	})
	sensorFiles(root, "sys/class/drm/card0-HDMI-A-1", map[string]string{"status": "connected"})
	state, err := newTestReader(root, nil).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gpus := state.System.Thermal.GPUs
	if len(gpus) != 2 {
		t.Fatalf("GPUs = %+v, want two", gpus)
	}
	if !gpus[0].Integrated || gpus[0].Temperature != nil {
		t.Fatalf("integrated GPU = %+v, want no sensor of its own", gpus[0])
	}
	if gpus[1].Integrated || gpus[1].Temperature == nil || *gpus[1].Temperature != (hoststate.Temperature{Celsius: 52, HotCelsius: 90}) {
		t.Fatalf("discrete GPU = %+v, want edge 52 °C hot from 90 °C", gpus[1])
	}
}

// The task manager shows the same readings: the CPU package, and each disk
// with the limit the system health uses for its kind.
func TestTaskDeviceStatusCarriesTemperatures(t *testing.T) {
	r, root, _ := taskFixture(t)
	sensorFiles(root, "sys/class/hwmon/hwmon2", map[string]string{"name": "coretemp", "temp1_input": "64000", "temp1_label": "Package id 0", "temp1_crit": "100000"})
	sensorFiles(root, "sys/class/block/nvme0n1/device/hwmon1", map[string]string{"temp1_input": "48850", "temp1_label": "Composite"})
	root["sys/class/block/nvme0n1/stat"] = &fstest.MapFile{Data: []byte("1 0 8 0 1 0 8 0 0 0 0\n")}
	root["sys/class/block/sda/stat"] = &fstest.MapFile{Data: []byte("1 0 8 0 1 0 8 0 0 0 0\n")}
	r.dependencies.readTaskBlockDevices = func(context.Context) ([]byte, error) {
		return []byte(`{"blockdevices":[
			{"name":"nvme0n1","type":"disk","size":125000000000,"model":"SSD","tran":"nvme","rota":false,"wwn":"eui.0001","serial":"SYS1"},
			{"name":"sda","type":"disk","size":512000000000,"model":"HDD","tran":"sata","rota":true,"wwn":"0x5000","serial":"HDD1"}
		]}`), nil
	}
	r.smart.readings = map[string]smartReading{"wwn:0x5000": {health: hoststate.HealthHealthy, temperature: new(37), at: r.dependencies.now()}}
	got, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU.Temperature == nil || got.CPU.Temperature.Celsius != 64 || got.CPU.Temperature.HotCelsius != 90 {
		t.Fatalf("CPU temperature = %+v, want 64 °C hot from 90 °C", got.CPU.Temperature)
	}
	byModel := map[string]int{}
	for i, disk := range got.Disks.Items {
		byModel[disk.Model] = i
	}
	ssd, hdd := got.Disks.Items[byModel["SSD"]], got.Disks.Items[byModel["HDD"]]
	if ssd.Temperature == nil || ssd.Temperature.Celsius != 49 || ssd.Temperature.HotCelsius != 70 || ssd.Rotational {
		t.Fatalf("SSD = %+v, want live 49 °C hot from 70 °C", ssd)
	}
	if hdd.Temperature == nil || hdd.Temperature.Celsius != 37 || hdd.Temperature.HotCelsius != 55 || !hdd.Rotational {
		t.Fatalf("HDD = %+v, want SMART 37 °C hot from 55 °C", hdd)
	}
	if !got.Valid() {
		t.Fatalf("snapshot with temperatures is invalid: %+v", got)
	}
}
