package linux

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// The Host Agent parses /proc, .desktop entries, smartctl's output and
// the kernel's sensor files as root. These fuzz targets feed each parser arbitrary input: it must return
// a value or an error, never panic (#223).

func FuzzTaskDesktop(f *testing.F) {
	f.Add([]byte("[Desktop Entry]\nType=Application\nExec=/usr/bin/player %U\nIcon=player\n"))
	f.Add([]byte("[Desktop Entry]\nExec=env MODE=1 \"/opt/My App/app\" --x\nIcon=/usr/share/pixmaps/a.png\n[Other]\nExec=x\n"))
	f.Add([]byte("Exec=\"unterminated\nIcon=\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		exe, _ := parseTaskDesktop(data)
		_ = taskDesktopExecutable(exe)
		_ = taskDesktopExecutable(string(data))
	})
}

func FuzzTaskProcess(f *testing.F) {
	f.Add([]byte(string(processStat(42, 10, 20, 3))), []byte("read_bytes: 1\nwrite_bytes: 2\n"))
	f.Add([]byte("42 (a) b) R 1 2 3"), []byte("8:0 rbytes=1 wbytes=2\n"))
	f.Add([]byte(")("), []byte(""))
	f.Fuzz(func(t *testing.T, stat, io []byte) {
		_, _ = parseTaskProcess(stat, 42)
		_, _ = parseProcessIO(io)
		_, _ = parseCgroupIO(io)
	})
}

func FuzzProcMetrics(f *testing.F) {
	f.Add([]byte("cpu  1 2 3 4 5 6 7 8 9 10\ncpu0 1 2 3 4 5 6 7 8 9 10\n"), []byte("MemTotal: 100 kB\nMemAvailable: 50 kB\n"),
		[]byte("Inter-|\n face |\n  eth0: 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16\n"))
	f.Add([]byte("cpu"), []byte("MemTotal:"), []byte("eth0:"))
	f.Fuzz(func(t *testing.T, stat, memory, network []byte) {
		root := fstest.MapFS{
			"proc/stat":    {Data: stat},
			"proc/meminfo": {Data: memory},
			"proc/net/dev": {Data: network},
		}
		_, _, _, _ = readCPU(root)
		_, _ = readMemory(root)
		_, _ = readNetwork(root)
	})
}

func FuzzSMART(f *testing.F) {
	f.Add([]byte(`{"smartctl":{"exit_status":0},"smart_status":{"passed":true},"temperature":{"current":35}}`))
	f.Add([]byte(`{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode"}]}}`))
	f.Add([]byte(`{"temperature":{"current":-1e309}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseSMART(data)
	})
}

// FuzzThermal feeds the readers of hwmon temperatures, the package thermal
// zone and fans arbitrary sensor files under one sensor number: each returns
// a plausible reading or none, never panics (#223).
func FuzzThermal(f *testing.F) {
	f.Add("coretemp", "1", "Package id 0", []byte("63000\n"), []byte("100000\n"), []byte("1200\n"))
	f.Add("k10temp", "2", "Tctl", []byte("-273150"), []byte("x"), []byte("18446744073709551615"))
	f.Add("nvme", "01", "Composite", []byte("9223372036854775807"), []byte(""), []byte("-1"))
	f.Add("nct6798", "0", "", []byte(" 41500 "), []byte("1e9"), []byte("50001"))
	f.Fuzz(func(t *testing.T, driver, number, label string, input, critical, fan []byte) {
		root := fstest.MapFS{}
		put := func(name string, data []byte) {
			if fs.ValidPath(name) {
				root[name] = &fstest.MapFile{Data: data}
			}
		}
		for _, directory := range []string{"sys/class/hwmon/hwmon0", "sys/class/nvme/nvme0/hwmon1", "sys/class/drm/card0/device/hwmon/hwmon2"} {
			put(directory+"/name", []byte(driver+"\n"))
			put(directory+"/temp"+number+"_input", input)
			put(directory+"/temp"+number+"_label", []byte(label))
			put(directory+"/temp"+number+"_crit", critical)
			put(directory+"/fan"+number+"_input", fan)
			put(directory+"/fan"+number+"_label", []byte(label))
		}
		put("sys/class/thermal/thermal_zone0/type", []byte(driver))
		put("sys/class/thermal/thermal_zone0/temp", input)
		temperatures := []*hoststate.Temperature{
			readCPUTemperature(root),
			readDeviceTemperature(root, "sys/class/nvme/nvme0", label, "Composite"),
			readDeviceTemperature(root, "sys/class/drm/card0/device", gpuTemperatureLabels...),
		}
		if zone := readPackageThermalZone(root); zone != nil {
			temperatures = append(temperatures, temperatureFrom(*zone))
		}
		for _, temperature := range temperatures {
			if temperature != nil && !temperature.Valid() {
				t.Fatalf("an implausible temperature %+v", *temperature)
			}
		}
		fans, _ := readFanSensors(root)
		for _, reading := range fans {
			if reading.number < 1 || reading.rpm < 0 || reading.rpm > hoststate.MaxFanRPM || reading.id == "" {
				t.Fatalf("an implausible fan %+v", reading)
			}
		}
	})
}

func FuzzBtrfsDevices(f *testing.F) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	f.Add([]byte("Label: 'ANAS_DATA'  uuid: " + uuid + "\n\tTotal devices 2 FS bytes used 46245638144\n" +
		"\tdevid    1 size 500106788864 used 52613349376 path /dev/sdb1\n\tdevid    2 size 500106788864 used 52613349376 path /dev/sda1\n\n"))
	f.Add([]byte("Label: none  uuid: " + uuid + "\n\tTotal devices 1 FS bytes used 1\n\tdevid 18446744073709551615 size 1 used 1 path x\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, device := range parseBtrfsDevices(data, uuid) {
			if device.DevID == 0 || device.SizeBytes == 0 || device.AllocatedBytes > device.SizeBytes {
				t.Fatalf("parseBtrfsDevices() accepted %+v", device)
			}
		}
	})
}
