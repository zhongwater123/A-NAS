package linux

import (
	"io/fs"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// Temperatures come from the kernel's hwmon class, whose files hold
// millidegrees Celsius (Documentation/hwmon/sysfs-interface.rst). Reading
// them needs no privilege and wakes nothing: CPU and GPU sensors are on the
// chip, and an NVMe drive answers from its controller. SATA disks are left
// to the SMART cache, because the kernel's drivetemp may keep a hard disk
// from spinning down (docs/specs/device-temperatures.md).

const (
	// hotMargin is how far below its critical temperature, where it
	// throttles hard or shuts down, a chip counts as running hot.
	hotMargin = 10
	// defaultHotCelsius applies when a sensor reports no critical
	// temperature, as AMD's k10temp does.
	defaultHotCelsius = 85
	// hwmonLimit bounds the hwmon devices and readings looked at.
	hwmonLimit = 256
)

// cpuSensors are the hwmon drivers that report CPU packages, with the
// labels of their package readings, best first. A label ending in a space
// matches every reading it begins, such as coretemp's "Package id 0" and
// "Package id 1"; without one, every reading of the driver counts.
var cpuSensors = map[string][]string{
	"coretemp":    {"Package id "},
	"k10temp":     {"Tdie", "Tctl"},
	"zenpower":    {"Tdie", "Tctl"},
	"cpu_thermal": nil,
}

type hwmonReading struct {
	index    int
	label    string
	celsius  int
	critical *int
}

// readCPUTemperature returns the hottest CPU package, or nil without a
// CPU sensor. On x86 without a hwmon driver it falls back to the thermal
// zone of the package.
func readCPUTemperature(root fs.FS) *hoststate.Temperature {
	if root == nil {
		return nil
	}
	var best *hwmonReading
	entries, _, err := taskDirectories(root, "sys/class/hwmon", hwmonLimit)
	if err == nil {
		for _, entry := range entries {
			directory := "sys/class/hwmon/" + entry.Name()
			name, err := readTaskFile(root, directory+"/name")
			if err != nil {
				continue
			}
			labels, ok := cpuSensors[strings.TrimSpace(string(name))]
			if !ok {
				continue
			}
			for _, reading := range pickReadings(readHwmonTemperatures(root, directory), labels) {
				if best == nil || reading.celsius > best.celsius {
					best = &reading
				}
			}
		}
	}
	if best == nil {
		best = readPackageThermalZone(root)
	}
	if best == nil {
		return nil
	}
	return temperatureFrom(*best)
}

// readDeviceTemperature reads the hwmon of a device, such as a GPU at
// sys/class/drm/card0/device or an NVMe controller at
// sys/class/block/nvme0n1/device: the reading with the first of labels
// present, else its lowest-numbered reading.
func readDeviceTemperature(root fs.FS, device string, labels ...string) *hoststate.Temperature {
	if root == nil {
		return nil
	}
	var readings []hwmonReading
	for _, directory := range deviceHwmons(root, device) {
		readings = append(readings, readHwmonTemperatures(root, directory)...)
	}
	if len(readings) == 0 {
		return nil
	}
	for _, label := range labels {
		for _, reading := range readings {
			if reading.label == label {
				return temperatureFrom(reading)
			}
		}
	}
	return temperatureFrom(readings[0])
}

// deviceHwmons lists the hwmon directories of a device. Class devices such
// as an NVMe controller hold them directly (hwmon1), bus devices such as a
// PCI GPU in a hwmon directory (hwmon/hwmon3).
func deviceHwmons(root fs.FS, device string) []string {
	var directories []string
	for _, parent := range []string{device, device + "/hwmon"} {
		entries, _, err := taskDirectories(root, parent, hwmonLimit)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if suffix, ok := strings.CutPrefix(entry.Name(), "hwmon"); ok && isDecimal(suffix) {
				directories = append(directories, parent+"/"+entry.Name())
			}
		}
	}
	return directories
}

// readHwmonTemperatures reads the plausible temp<N>_input readings of one
// hwmon directory with their labels and critical limits, by number.
func readHwmonTemperatures(root fs.FS, directory string) []hwmonReading {
	entries, _, err := taskDirectories(root, directory, hwmonLimit)
	if err != nil {
		return nil
	}
	var readings []hwmonReading
	for _, entry := range entries {
		number, ok := strings.CutPrefix(entry.Name(), "temp")
		if !ok {
			continue
		}
		number, ok = strings.CutSuffix(number, "_input")
		if !ok || !isDecimal(number) {
			continue
		}
		index, err := strconv.Atoi(number)
		if err != nil {
			continue
		}
		celsius, ok := readMillidegrees(root, directory+"/temp"+number+"_input")
		if !ok {
			continue
		}
		reading := hwmonReading{index: index, celsius: celsius}
		if label, err := readTaskFile(root, directory+"/temp"+number+"_label"); err == nil {
			reading.label = strings.TrimSpace(string(label))
		}
		if critical, ok := readMillidegrees(root, directory+"/temp"+number+"_crit"); ok {
			reading.critical = &critical
		}
		readings = append(readings, reading)
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].index < readings[j].index })
	return readings
}

// pickReadings keeps the readings with the first of labels any of them
// has; nil labels keep every reading.
func pickReadings(readings []hwmonReading, labels []string) []hwmonReading {
	if labels == nil {
		return readings
	}
	for _, label := range labels {
		var picked []hwmonReading
		for _, reading := range readings {
			if reading.label == label || (strings.HasSuffix(label, " ") && strings.HasPrefix(reading.label, label)) {
				picked = append(picked, reading)
			}
		}
		if len(picked) > 0 {
			return picked
		}
	}
	return nil
}

// readPackageThermalZone reads the x86_pkg_temp thermal zone that Intel
// CPUs have even when coretemp is not loaded.
func readPackageThermalZone(root fs.FS) *hwmonReading {
	entries, _, err := taskDirectories(root, "sys/class/thermal", hwmonLimit)
	if err != nil {
		return nil
	}
	var best *hwmonReading
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}
		directory := "sys/class/thermal/" + entry.Name()
		kind, err := readTaskFile(root, directory+"/type")
		if err != nil || strings.TrimSpace(string(kind)) != "x86_pkg_temp" {
			continue
		}
		if celsius, ok := readMillidegrees(root, directory+"/temp"); ok && (best == nil || celsius > best.celsius) {
			best = &hwmonReading{celsius: celsius}
		}
	}
	return best
}

func temperatureFrom(reading hwmonReading) *hoststate.Temperature {
	hot := defaultHotCelsius
	if reading.critical != nil && *reading.critical-hotMargin > 0 {
		hot = *reading.critical - hotMargin
	}
	return &hoststate.Temperature{Celsius: reading.celsius, HotCelsius: hot}
}

// readMillidegrees reads a millidegree file as whole degrees, rounded to
// the nearest; implausible readings, such as those of an unconnected
// thermistor, are not readings.
func readMillidegrees(root fs.FS, name string) (int, bool) {
	data, err := readTaskFile(root, name)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	celsius := int(math.Round(float64(value) / 1000))
	if !hoststate.PlausibleCelsius(celsius) {
		return 0, false
	}
	return celsius, true
}

func isDecimal(value string) bool {
	return value != "" && !slices.ContainsFunc([]byte(value), func(c byte) bool { return c < '0' || c > '9' })
}

// gpuIntegrated tells an Intel GPU inside the CPU package, which Intel
// places at PCI 00:02.0, from a discrete card.
func gpuIntegrated(slot, pciID string) bool {
	return slot == "0000:00:02.0" && strings.HasPrefix(strings.ToUpper(pciID), "8086:")
}

// thermal reads the CPU and GPU temperatures of the host state.
func (r *Reader) thermal() *hoststate.Thermal {
	root := r.dependencies.root
	if root == nil {
		return nil
	}
	fans, _ := r.fans()
	thermal := &hoststate.Thermal{CPU: readCPUTemperature(root), GPUs: []hoststate.GPU{}, Fans: fans}
	for _, item := range readTaskAccelerators(root, "drm", "card").Items {
		id, err := hoststate.NewResourceID(item.ID)
		if err != nil {
			continue
		}
		gpu := hoststate.GPU{ID: id, Name: item.Name, Integrated: item.Integrated}
		if temperature := item.Temperature; temperature != nil {
			gpu.Temperature = &hoststate.Temperature{Celsius: temperature.Celsius, HotCelsius: temperature.HotCelsius}
		}
		thermal.GPUs = append(thermal.GPUs, gpu)
	}
	return thermal
}

// gpuHwmons are the hwmon drivers of graphics cards. Their fans are the
// card's own, not the board's, and are left out of the fan list.
var gpuHwmons = map[string]bool{"amdgpu": true, "radeon": true, "nouveau": true, "i915": true, "xe": true}

// fanMemory keeps the fans that turned since the Host Agent started. A fan
// input reads 0 both when nothing is plugged in and when its fan stopped;
// only the second is worth showing.
type fanMemory struct {
	mu   sync.Mutex
	spun map[string]bool
}

type fanReading struct {
	id     string
	number int
	label  string
	rpm    int
}

// readFanSensors reads every fan<N>_input of the hwmon devices other than
// GPUs, and says whether there was any; none means no sensor chip driver.
func readFanSensors(root fs.FS) ([]fanReading, bool) {
	entries, _, err := taskDirectories(root, "sys/class/hwmon", hwmonLimit)
	if err != nil {
		return nil, false
	}
	var readings []fanReading
	found := false
	for _, entry := range entries {
		directory := "sys/class/hwmon/" + entry.Name()
		name, err := readTaskFile(root, directory+"/name")
		if err != nil || gpuHwmons[strings.TrimSpace(string(name))] {
			continue
		}
		files, _, err := taskDirectories(root, directory, hwmonLimit)
		if err != nil {
			continue
		}
		for _, file := range files {
			number, ok := strings.CutPrefix(file.Name(), "fan")
			if !ok {
				continue
			}
			number, ok = strings.CutSuffix(number, "_input")
			index, err := strconv.Atoi(number)
			if !ok || !isDecimal(number) || err != nil || index < 1 {
				continue
			}
			found = true
			rpm, err := taskUint(root, directory+"/"+file.Name())
			if err != nil || *rpm > hoststate.MaxFanRPM {
				continue
			}
			id, err := derivedResourceID("fan", "a-nas:fan:v1", entry.Name()+"\x00"+strings.TrimSpace(string(name))+"\x00"+number)
			if err != nil {
				continue
			}
			reading := fanReading{id: id.String(), number: index, rpm: int(*rpm)}
			if label, err := readTaskFile(root, directory+"/fan"+number+"_label"); err == nil {
				reading.label = strings.TrimSpace(string(label))
			}
			readings = append(readings, reading)
		}
	}
	sort.Slice(readings, func(i, j int) bool {
		if readings[i].number != readings[j].number {
			return readings[i].number < readings[j].number
		}
		return readings[i].id < readings[j].id
	})
	return readings, found
}

// fans returns the fans turning now or since the Host Agent started, and
// whether the host has any fan sensor at all.
func (r *Reader) fans() ([]hoststate.Fan, bool) {
	readings, found := readFanSensors(r.dependencies.root)
	memory := &r.fanMemory
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.spun == nil {
		memory.spun = map[string]bool{}
	}
	fans := []hoststate.Fan{}
	for _, reading := range readings {
		if reading.rpm > 0 {
			memory.spun[reading.id] = true
		}
		if !memory.spun[reading.id] {
			continue
		}
		id, err := hoststate.NewResourceID(reading.id)
		if err != nil {
			continue
		}
		fans = append(fans, hoststate.Fan{ID: id, Number: reading.number, Label: reading.label, RPM: reading.rpm, Stopped: reading.rpm == 0})
	}
	return fans, found
}
