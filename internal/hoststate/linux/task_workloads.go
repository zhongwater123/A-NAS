package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

type taskUsage struct {
	mainPID          int
	row              taskmanager.Task
	identity         string
	cpu, read, write *uint64
}
type workloadTaskSample struct {
	at                           time.Time
	cpu                          *counterSample
	processes, services          map[string]taskUsage
	processStatus, serviceStatus taskmanager.Status
}
type workloadTaskSampler struct {
	icons    taskIconCache
	gate     taskGate
	previous *workloadTaskSample
	cached   taskmanager.TaskSnapshot
}

func (r *Reader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()
	s := &r.workloadTasks
	if err := s.gate.lock(ctx); err != nil {
		return taskmanager.TaskSnapshot{}, err
	}
	defer s.gate.unlock()
	previous := s.previous
	now := r.dependencies.now()
	if previous != nil && !now.Before(previous.at) && now.Sub(previous.at) < minSampleInterval {
		return s.cached, nil
	}
	current, err := r.sampleTasks(ctx)
	if err != nil {
		return taskmanager.TaskSnapshot{}, err
	}
	if previous == nil || !current.at.After(previous.at) || current.at.Sub(previous.at) > maxBaselineAge {
		previous = current
		if err := r.dependencies.sleep(ctx, baselineWindow); err != nil {
			return taskmanager.TaskSnapshot{}, err
		}
		current, err = r.sampleTasks(ctx)
		if err != nil {
			return taskmanager.TaskSnapshot{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return taskmanager.TaskSnapshot{}, err
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds <= 0 {
		return taskmanager.TaskSnapshot{}, errors.New("task observation clock did not advance")
	}
	result := taskmanager.TaskSnapshot{ObservedAt: current.at.UTC(), SampleSeconds: seconds, Processes: taskmanager.TaskGroup{Status: current.processStatus, Items: []taskmanager.Task{}}, Services: taskmanager.TaskGroup{Status: current.serviceStatus, Items: []taskmanager.Task{}}}
	for id, usage := range current.processes {
		row := usage.row
		if before, ok := previous.processes[id]; ok && before.identity == usage.identity {
			if previous.cpu.logicalCores == current.cpu.logicalCores && current.cpu.cpuTotal > previous.cpu.cpuTotal && usage.cpu != nil && before.cpu != nil && *usage.cpu >= *before.cpu {
				row.CPUPercent = taskFloat(math.Min(100, float64(*usage.cpu-*before.cpu)/float64(current.cpu.cpuTotal-previous.cpu.cpuTotal)*100))
			}
			row.ReadBytesPerSecond = taskRate(before.read, usage.read, seconds)
			row.WriteBytesPerSecond = taskRate(before.write, usage.write, seconds)
		}
		result.Processes.Items = append(result.Processes.Items, row)
	}
	for id, usage := range current.services {
		row := usage.row
		if before, ok := previous.services[id]; ok && usage.identity != "" && before.identity == usage.identity {
			if previous.cpu.logicalCores == current.cpu.logicalCores {
				if rate := taskRate(before.cpu, usage.cpu, seconds); rate != nil {
					row.CPUPercent = taskFloat(math.Min(100, *rate/(float64(current.cpu.logicalCores)*1e6)*100))
				}
			}
			row.ReadBytesPerSecond = taskRate(before.read, usage.read, seconds)
			row.WriteBytesPerSecond = taskRate(before.write, usage.write, seconds)
		}
		result.Services.Items = append(result.Services.Items, row)
	}
	sort.Slice(result.Processes.Items, func(i, j int) bool { return result.Processes.Items[i].PID < result.Processes.Items[j].PID })
	sort.Slice(result.Services.Items, func(i, j int) bool { return result.Services.Items[i].Name < result.Services.Items[j].Name })
	r.attachTaskIcons(ctx, &result, current.services)
	s.previous = current
	s.cached = result
	return result, nil
}
func (r *Reader) sampleTasks(ctx context.Context) (*workloadTaskSample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// systemctl can take substantially longer than reading proc counters.
	// Finish discovery first, then timestamp the CPU/process observation.
	services, serviceStatus := r.readTaskServices(ctx)
	processes, processStatus := readTaskProcesses(ctx, r.dependencies.root)
	cpu, err := taskCPU(r.dependencies.root, r.dependencies.now())
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &workloadTaskSample{at: r.dependencies.now(), cpu: cpu, processes: processes, services: services, processStatus: processStatus, serviceStatus: serviceStatus}, nil
}

func parseTaskProcess(data []byte, pid int) (taskUsage, error) {
	text := string(data)
	open := strings.IndexByte(text, '(')
	close := strings.LastIndexByte(text, ')')
	if open < 1 || close <= open {
		return taskUsage{}, errors.New("invalid process stat")
	}
	actual, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil || actual != pid {
		return taskUsage{}, errors.New("process identity mismatch")
	}
	fields := strings.Fields(text[close+1:])
	if len(fields) < 22 {
		return taskUsage{}, errors.New("incomplete process stat")
	}
	user, e1 := taskNumber(fields[11])
	system, e2 := taskNumber(fields[12])
	start, e3 := taskNumber(fields[19])
	rss, e4 := taskNumber(fields[21])
	if e1 != nil || e2 != nil || e3 != nil {
		return taskUsage{}, errors.New("invalid process counters")
	}
	name := strings.TrimSpace(text[open+1 : close])
	if name == "" {
		name = "(unknown)"
	}
	row := taskmanager.Task{ID: fmt.Sprintf("process:%d:%d", pid, start), Name: name, PID: pid, State: fields[0]}
	if e4 == nil && rss <= ^uint64(0)/uint64(os.Getpagesize()) {
		row.MemoryBytes = taskUintPointer(rss * uint64(os.Getpagesize()))
	}
	return taskUsage{row: row, identity: strconv.FormatUint(start, 10), cpu: taskUintPointer(user + system)}, nil
}
func readTaskProcesses(ctx context.Context, root fs.FS) (map[string]taskUsage, taskmanager.Status) {
	result := map[string]taskUsage{}
	status := taskmanager.Available
	entries, truncated, err := taskDirectories(root, "proc", taskProcessLimit+512)
	if err != nil {
		return result, taskmanager.Unavailable
	}
	if truncated {
		status = taskmanager.Partial
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return result, taskmanager.Partial
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		if len(result) >= taskProcessLimit {
			status = taskmanager.Partial
			break
		}
		path := "proc/" + entry.Name() + "/"
		data, err := readTaskFile(root, path+"stat")
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				status = taskmanager.Partial
			}
			continue
		}
		usage, err := parseTaskProcess(data, pid)
		if err != nil {
			status = taskmanager.Partial
			continue
		}
		ioData, ioErr := readTaskFile(root, path+"io")
		if ioErr == nil {
			usage.read, usage.write = parseProcessIO(ioData)
		}
		// Re-check start time after the other reads: a PID may exit and be reused
		// even within one observation, not just between two samples.
		verify, err := readTaskFile(root, path+"stat")
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				status = taskmanager.Partial
			}
			continue
		}
		checked, err := parseTaskProcess(verify, pid)
		if err != nil || checked.identity != usage.identity {
			status = taskmanager.Partial
			continue
		}
		if usage.read == nil || usage.write == nil || usage.row.MemoryBytes == nil {
			status = taskmanager.Partial
		}
		result[usage.row.ID] = usage
	}
	return result, status
}
func parseProcessIO(data []byte) (read, write *uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := taskNumber(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch key {
		case "read_bytes":
			read = taskUintPointer(n)
		case "write_bytes":
			write = taskUintPointer(n)
		}
	}
	return
}
func (r *Reader) readTaskServices(ctx context.Context) (map[string]taskUsage, taskmanager.Status) {
	result := map[string]taskUsage{}
	if r.dependencies.readTaskServices == nil {
		return result, taskmanager.Unavailable
	}
	data, err := r.dependencies.readTaskServices(ctx)
	if errors.Is(err, errTaskObservationLimit) {
		return result, taskmanager.Partial
	}
	if err != nil {
		return result, taskmanager.Unavailable
	}
	if len(data) > taskReadLimit {
		return result, taskmanager.Partial
	}
	status := taskmanager.Available
	root := r.dependencies.root
	validBlocks := 0
	_, controllersErr := fs.Stat(root, "sys/fs/cgroup/cgroup.controllers")
	for _, block := range strings.Split(strings.TrimSpace(string(data)), "\n\n") {
		if ctx.Err() != nil {
			return result, taskmanager.Partial
		}
		values := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				values[k] = v
			}
		}
		if values["Id"] != "" && values["ActiveState"] != "" && values["SubState"] != "" {
			validBlocks++
		} else if strings.TrimSpace(block) != "" {
			status = taskmanager.Partial
		}
		if values["ActiveState"] != "active" || values["SubState"] != "running" {
			continue
		}
		name := values["Id"]
		if name == "" || !strings.HasSuffix(name, ".service") {
			status = taskmanager.Partial
			continue
		}
		if len(result) >= taskServiceLimit {
			status = taskmanager.Partial
			break
		}
		usage := taskUsage{row: taskmanager.Task{ID: "service:" + name, Name: name, State: "running"}, identity: values["InvocationID"]}
		if pid, err := strconv.Atoi(values["MainPID"]); err == nil && pid > 0 {
			usage.mainPID = pid
		}
		group := strings.TrimPrefix(values["ControlGroup"], "/")
		if group == "" || !fs.ValidPath(group) || controllersErr != nil {
			status = taskmanager.Partial
			result[usage.row.ID] = usage
			continue
		}
		path := "sys/fs/cgroup/" + group + "/"
		// These v2 counters are hierarchical. Summing child cgroups again would
		// count their resource use twice. No process list traversal is required.
		if cpuData, err := readTaskFile(root, path+"cpu.stat"); err == nil {
			for _, line := range strings.Split(string(cpuData), "\n") {
				f := strings.Fields(line)
				if len(f) == 2 && f[0] == "usage_usec" {
					if n, e := taskNumber(f[1]); e == nil {
						usage.cpu = taskUintPointer(n)
					}
				}
			}
		}
		usage.row.MemoryBytes, _ = taskUint(root, path+"memory.current")
		if ioData, err := readTaskFile(root, path+"io.stat"); err == nil {
			usage.read, usage.write = parseCgroupIO(ioData)
		}
		if usage.cpu == nil || usage.row.MemoryBytes == nil || usage.read == nil || usage.write == nil || usage.identity == "" {
			status = taskmanager.Partial
		}
		result[usage.row.ID] = usage
	}
	if len(result) > 0 && controllersErr != nil {
		if errors.Is(controllersErr, fs.ErrNotExist) {
			status = taskmanager.Unsupported
		} else {
			status = taskmanager.Unavailable
		}
	}
	if len(strings.TrimSpace(string(data))) > 0 && validBlocks == 0 {
		status = taskmanager.Unavailable
	}
	return result, status
}
func parseCgroupIO(data []byte) (*uint64, *uint64) {
	var read, write uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		haveRead, haveWrite := false, false
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			if key != "rbytes" && key != "wbytes" {
				continue
			}
			n, err := taskNumber(value)
			if err != nil {
				return nil, nil
			}
			if key == "rbytes" {
				if n > ^uint64(0)-read {
					return nil, nil
				}
				read += n
				haveRead = true
			} else {
				if n > ^uint64(0)-write {
					return nil, nil
				}
				write += n
				haveWrite = true
			}
		}
		if !haveRead || !haveWrite {
			return nil, nil
		}
	}
	return taskUintPointer(read), taskUintPointer(write)
}
