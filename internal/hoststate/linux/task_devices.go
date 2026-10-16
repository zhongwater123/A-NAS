package linux

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

type taskDiskCounters struct{ read, write *uint64 }
type deviceTaskSample struct {
	at       time.Time
	cpu      *counterSample
	snapshot taskmanager.DeviceSnapshot
	disks    map[string]taskDiskCounters
}
type deviceTaskSampler struct {
	gate     taskGate
	previous *deviceTaskSample
	cached   taskmanager.DeviceSnapshot
}

func (r *Reader) ReadDeviceStatus(ctx context.Context) (taskmanager.DeviceSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()
	s := &r.deviceTasks
	if err := s.gate.lock(ctx); err != nil {
		return taskmanager.DeviceSnapshot{}, err
	}
	defer s.gate.unlock()
	now := r.dependencies.now()
	previous := s.previous
	if previous != nil && !now.Before(previous.at) && now.Sub(previous.at) < minSampleInterval {
		return s.cached, nil
	}
	current, err := r.sampleTaskDevice(ctx)
	if err != nil {
		return taskmanager.DeviceSnapshot{}, err
	}
	if previous == nil || !current.at.After(previous.at) || current.at.Sub(previous.at) > maxBaselineAge {
		previous = current
		if err := r.dependencies.sleep(ctx, baselineWindow); err != nil {
			return taskmanager.DeviceSnapshot{}, err
		}
		current, err = r.sampleTaskDevice(ctx)
		if err != nil {
			return taskmanager.DeviceSnapshot{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return taskmanager.DeviceSnapshot{}, err
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds <= 0 {
		return taskmanager.DeviceSnapshot{}, errors.New("task observation clock did not advance")
	}
	result := current.snapshot
	result.SampleSeconds = seconds
	result.CPU.UsagePercent = taskCPUPercent(previous.cpu, current.cpu)
	priorNetworks := map[string]taskmanager.Network{}
	for _, v := range previous.snapshot.Network.Items {
		priorNetworks[v.ID] = v
	}
	for i := range result.Network.Items {
		n := &result.Network.Items[i]
		if old, ok := priorNetworks[n.ID]; ok {
			n.ReceiveBytesPerSecond = taskRate(&old.ReceiveBytes, &n.ReceiveBytes, seconds)
			n.TransmitBytesPerSecond = taskRate(&old.TransmitBytes, &n.TransmitBytes, seconds)
		}
	}
	for i := range result.Disks.Items {
		d := &result.Disks.Items[i]
		if old, ok := previous.disks[d.ID]; ok {
			new := current.disks[d.ID]
			d.ReadBytesPerSecond = taskRate(old.read, new.read, seconds)
			d.WriteBytesPerSecond = taskRate(old.write, new.write, seconds)
		}
	}
	s.previous = current
	s.cached = result
	return result, nil
}

func (r *Reader) sampleTaskDevice(ctx context.Context) (*deviceTaskSample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := r.dependencies.root
	gpu, npu := readTaskAccelerators(root, "drm", "card"), readTaskAccelerators(root, "accel", "accel")
	// Run external discovery before the rate counters. A slow command must not
	// move the observation timestamp away from the CPU/network measurements.
	disks, counters := r.readTaskDisks(ctx)
	memory, err := readMemory(boundedTaskFS{root})
	if err != nil {
		return nil, err
	}
	networks := readTaskNetworks(root)
	model := readTaskCPUModel(root)
	cpu, err := taskCPU(root, r.dependencies.now())
	if err != nil {
		return nil, err
	}
	at := r.dependencies.now()
	result := taskmanager.DeviceSnapshot{ObservedAt: at.UTC(), CPU: taskmanager.CPU{LogicalCores: cpu.logicalCores, Model: model}, Memory: taskmanager.Memory{TotalBytes: memory.TotalBytes, UsedBytes: memory.UsedBytes}, Network: networks, Disks: disks, GPU: gpu, NPU: npu}
	return &deviceTaskSample{at: at, cpu: cpu, snapshot: result, disks: counters}, nil
}
func readTaskCPUModel(root fs.FS) string {
	data, err := readTaskFile(root, "proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && (strings.TrimSpace(key) == "model name" || strings.TrimSpace(key) == "Hardware") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func readTaskNetworks(root fs.FS) taskmanager.NetworkGroup {
	result := taskmanager.NetworkGroup{Status: taskmanager.Available, Items: []taskmanager.Network{}}
	data, err := readTaskFile(root, "proc/net/dev")
	if err != nil {
		result.Status = taskmanager.Unavailable
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, values, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || name == "lo" || strings.ContainsAny(name, "/|") {
			continue
		}
		fields := strings.Fields(values)
		if len(fields) < 9 {
			result.Status = taskmanager.Partial
			continue
		}
		rx, e1 := taskNumber(fields[0])
		tx, e2 := taskNumber(fields[8])
		if e1 != nil || e2 != nil {
			result.Status = taskmanager.Partial
			continue
		}
		_, physicalErr := fs.Stat(root, "sys/class/net/"+name+"/device")
		identity := name
		if index, err := taskUint(root, "sys/class/net/"+name+"/ifindex"); err == nil {
			identity = name + ":" + strconv.FormatUint(*index, 10)
		}
		result.Items = append(result.Items, taskmanager.Network{ID: identity, Name: name, Physical: physicalErr == nil, ReceiveBytes: rx, TransmitBytes: tx})
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Name < result.Items[j].Name })
	return result
}
func (r *Reader) readTaskDisks(ctx context.Context) (taskmanager.DiskGroup, map[string]taskDiskCounters) {
	result := taskmanager.DiskGroup{Status: taskmanager.Available, Items: []taskmanager.Disk{}}
	counters := map[string]taskDiskCounters{}
	readDevices := r.dependencies.readTaskBlockDevices
	if readDevices == nil {
		readDevices = r.dependencies.readBlockDevices
	}
	if readDevices == nil {
		result.Status = taskmanager.Unavailable
		return result, counters
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	data, err := readDevices(commandCtx)
	if errors.Is(err, errTaskObservationLimit) || len(data) > taskReadLimit {
		result.Status = taskmanager.Partial
		return result, counters
	}
	if err != nil || len(data) > taskReadLimit {
		result.Status = taskmanager.Unavailable
		return result, counters
	}
	var doc blockDeviceDocument
	if json.Unmarshal(data, &doc) != nil {
		result.Status = taskmanager.Unavailable
		return result, counters
	}
	seen := map[string]bool{}
	for _, device := range doc.BlockDevices {
		if device.Type != "disk" {
			continue
		}
		if device.Name == "" || strings.ContainsAny(device.Name, "/\\") {
			result.Status = taskmanager.Partial
			continue
		}
		identity, err := stableDiskIdentity(device)
		if err != nil {
			result.Status = taskmanager.Partial
			continue
		}
		id, err := derivedResourceID("disk", diskIdentityDomain, identity)
		if err != nil || seen[id.String()] {
			result.Status = taskmanager.Partial
			continue
		}
		seen[id.String()] = true
		d := taskmanager.Disk{ID: id.String(), Model: strings.TrimSpace(device.Model), CapacityBytes: device.Size}
		result.Items = append(result.Items, d)
		stat, err := readTaskFile(r.dependencies.root, "sys/class/block/"+device.Name+"/stat")
		if err != nil {
			result.Status = taskmanager.Partial
			continue
		}
		fields := strings.Fields(string(stat))
		if len(fields) < 7 {
			result.Status = taskmanager.Partial
			continue
		}
		read, e1 := taskNumber(fields[2])
		write, e2 := taskNumber(fields[6])
		if e1 != nil || e2 != nil || read > ^uint64(0)/512 || write > ^uint64(0)/512 {
			result.Status = taskmanager.Partial
			continue
		}
		// Kernel block statistics always use 512-byte sectors, including 4K disks.
		counters[d.ID] = taskDiskCounters{taskUintPointer(read * 512), taskUintPointer(write * 512)}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	return result, counters
}
func readTaskAccelerators(root fs.FS, class, prefix string) taskmanager.AcceleratorGroup {
	result := taskmanager.AcceleratorGroup{Status: taskmanager.Absent, Items: []taskmanager.Accelerator{}}
	entries, truncated, err := taskDirectories(root, "sys/class/"+class, 256)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			result.Status = taskmanager.Unavailable
		}
		return result
	}
	for _, entry := range entries {
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		if suffix == entry.Name() || suffix == "" {
			continue
		}
		if _, err := strconv.ParseUint(suffix, 10, 32); err != nil {
			continue
		}
		path := "sys/class/" + class + "/" + entry.Name() + "/device/"
		label := "GPU"
		if class == "accel" {
			label = "NPU"
		}
		item := taskmanager.Accelerator{ID: class + ":" + entry.Name(), Name: label + " " + suffix, Status: taskmanager.Unsupported, UsageStatus: taskmanager.Unsupported, MemoryUsedStatus: taskmanager.Unsupported, MemoryTotalStatus: taskmanager.Unsupported}
		slot, pciID := "", ""
		if data, err := readTaskFile(root, path+"uevent"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if ok && key == "DRIVER" {
					item.Driver = value
				}
				if ok && key == "PCI_SLOT_NAME" {
					slot = value
				}
				if ok && key == "PCI_ID" {
					pciID = value
					item.Name += " (" + value + ")"
				}
			}
		}
		if slot != "" {
			if id, err := derivedResourceID(class, "a-nas:accelerator:v1", class+"\x00"+slot+"\x00"+pciID); err == nil {
				item.ID = id.String()
			}
		}
		if class == "drm" && item.Driver == "amdgpu" {
			usage, usageStatus := readAcceleratorMetric(root, path+"gpu_busy_percent")
			item.UsageStatus = usageStatus
			if usage != nil {
				if *usage <= 100 {
					item.UsagePercent = taskFloat(float64(*usage))
				} else {
					item.UsageStatus = taskmanager.Unavailable
				}
			}
			item.MemoryUsedBytes, item.MemoryUsedStatus = readAcceleratorMetric(root, path+"mem_info_vram_used")
			item.MemoryTotalBytes, item.MemoryTotalStatus = readAcceleratorMetric(root, path+"mem_info_vram_total")
			if item.MemoryUsedBytes != nil && item.MemoryTotalBytes != nil && *item.MemoryUsedBytes > *item.MemoryTotalBytes {
				item.MemoryUsedBytes = nil
				item.MemoryUsedStatus = taskmanager.Unavailable
			}
			item.Status = item.UsageStatus
			if item.MemoryUsedStatus != item.Status || item.MemoryTotalStatus != item.Status {
				item.Status = taskmanager.Partial
			}
		}
		result.Items = append(result.Items, item)
	}
	if len(result.Items) > 0 {
		result.Status = result.Items[0].Status
		for _, item := range result.Items {
			if item.Status != result.Status {
				result.Status = taskmanager.Partial
			}
		}
	}
	if truncated {
		result.Status = taskmanager.Partial
	}
	return result
}

// Each counter keeps its own reason for being absent. A working VRAM counter
// must not hide whether usage is unsupported or temporarily unreadable.
func readAcceleratorMetric(root fs.FS, name string) (*uint64, taskmanager.Status) {
	value, err := taskUint(root, name)
	if err == nil {
		return value, taskmanager.Available
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, taskmanager.Unsupported
	}
	return nil, taskmanager.Unavailable
}
