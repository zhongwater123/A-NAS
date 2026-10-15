package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func taskFixture(t *testing.T) (*Reader, fstest.MapFS, *time.Time) {
	t.Helper()
	root := fstest.MapFS{}
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	root["proc/stat"] = &fstest.MapFile{Data: []byte("cpu 100 0 0 900 0 0 0 0\ncpu0 0\ncpu1 0\n")}
	root["proc/cpuinfo"] = &fstest.MapFile{Data: []byte("model name : Fixture CPU\n")}
	root["proc/meminfo"] = &fstest.MapFile{Data: []byte("MemTotal: 1024 kB\nMemAvailable: 256 kB\n")}
	root["proc/net/dev"] = &fstest.MapFile{Data: []byte("eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")}
	root["sys/class/net/eth0/device/present"] = &fstest.MapFile{}
	r := newReader(dependencies{root: root, now: func() time.Time { return now }, readBlockDevices: func(context.Context) ([]byte, error) { return []byte(`{"blockdevices":[]}`), nil }, sleep: func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		root["proc/stat"].Data = []byte("cpu 150 0 0 950 0 0 0 0\ncpu0 0\ncpu1 0\n")
		root["proc/net/dev"].Data = []byte("eth0: 200 0 0 0 0 0 0 0 400 0 0 0 0 0 0 0\n")
		return nil
	}})
	return r, root, &now
}

func TestTaskDeviceFirstSampleAndCache(t *testing.T) {
	r, _, now := taskFixture(t)
	got, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleSeconds != 0.5 || got.CPU.UsagePercent == nil || *got.CPU.UsagePercent != 50 || got.CPU.Model != "Fixture CPU" {
		t.Fatalf("bad CPU sample: %+v", got)
	}
	if got.Memory.UsedBytes != 768*1024 || len(got.Network.Items) != 1 || *got.Network.Items[0].ReceiveBytesPerSecond != 200 || !got.Network.Items[0].Physical {
		t.Fatalf("bad memory/network: %+v", got)
	}
	*now = now.Add(100 * time.Millisecond)
	cached, err := r.ReadDeviceStatus(context.Background())
	if err != nil || !cached.ObservedAt.Equal(got.ObservedAt) {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	*now = now.Add(16 * time.Second)
	fresh, err := r.ReadDeviceStatus(context.Background())
	if err != nil || fresh.SampleSeconds != 0.5 {
		t.Fatalf("stale baseline: %+v %v", fresh, err)
	}
}

func processStat(pid int, start, ticks, rss uint64) []byte {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[11] = fmt.Sprint(ticks)
	fields[19] = fmt.Sprint(start)
	fields[21] = fmt.Sprint(rss)
	return []byte(fmt.Sprintf("%d (worker ) name) %s", pid, joinTaskFields(fields)))
}
func joinTaskFields(fields []string) string {
	s := ""
	for i, f := range fields {
		if i > 0 {
			s += " "
		}
		s += f
	}
	return s
}

func TestTasksProcessRatesReuseAndExit(t *testing.T) {
	r, root, now := taskFixture(t)
	root["proc/42/stat"] = &fstest.MapFile{Data: processStat(42, 10, 20, 3)}
	root["proc/42/io"] = &fstest.MapFile{Data: []byte("rchar: 99999\nread_bytes: 100\nwrite_bytes: 200\n")}
	r.dependencies.sleep = func(_ context.Context, d time.Duration) error {
		*now = now.Add(d)
		root["proc/stat"].Data = []byte("cpu 150 0 0 950 0 0 0 0\ncpu0 0\ncpu1 0\n")
		root["proc/42/stat"].Data = processStat(42, 10, 45, 3)
		root["proc/42/io"].Data = []byte("read_bytes: 200\nwrite_bytes: 400\n")
		return nil
	}
	got, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Processes.Items) != 1 {
		t.Fatalf("processes: %+v", got)
	}
	p := got.Processes.Items[0]
	if p.CPUPercent == nil || *p.CPUPercent != 25 || p.Name != "worker ) name" || p.ReadBytesPerSecond == nil || *p.ReadBytesPerSecond != 200 || p.MemoryBytes == nil || *p.MemoryBytes == 0 {
		t.Fatalf("bad process: %+v", p)
	}
	if got.Services.Status != taskmanager.Unavailable {
		t.Fatalf("unreadable services must not be empty available: %+v", got.Services)
	}
	*now = now.Add(time.Second)
	root["proc/42/stat"].Data = processStat(42, 11, 900, 3)
	got, err = r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Processes.Items[0].CPUPercent != nil || got.Processes.Items[0].ReadBytesPerSecond != nil {
		t.Fatal("PID reuse inherited rate")
	}
	*now = now.Add(time.Second)
	delete(root, "proc/42/stat")
	delete(root, "proc/42/io")
	got, err = r.ReadTasks(context.Background())
	if err != nil || len(got.Processes.Items) != 0 {
		t.Fatalf("exited process: %+v %v", got, err)
	}
}

func TestTaskCancellation(t *testing.T) {
	r, _, _ := taskFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ReadDeviceStatus(ctx); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := r.ReadTasks(ctx); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestTaskCacheAtSameInstant(t *testing.T) {
	r, _, _ := taskFixture(t)
	first, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.ReadTasks(context.Background())
	if err != nil || !first.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("same-instant request must use cache: %v %v %v", first.ObservedAt, second.ObservedAt, err)
	}
}

func TestTaskServicesHierarchicalCountersAndRestart(t *testing.T) {
	r, root, now := taskFixture(t)
	invocation := "first"
	r.dependencies.readTaskServices = func(context.Context) ([]byte, error) {
		return []byte("Id=worker.service\nActiveState=active\nSubState=running\nControlGroup=/custom.slice/worker.service\nInvocationID=" + invocation + "\n\nId=idle.service\nActiveState=active\nSubState=exited\n"), nil
	}
	root["sys/fs/cgroup/cgroup.controllers"] = &fstest.MapFile{Data: []byte("cpu memory io")}
	base := "sys/fs/cgroup/custom.slice/worker.service/"
	root[base+"cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 100000\n")}
	root[base+"memory.current"] = &fstest.MapFile{Data: []byte("4096")}
	root[base+"io.stat"] = &fstest.MapFile{Data: []byte("8:0 rbytes=100 wbytes=200\n8:16 rbytes=300 wbytes=400\n")}
	root[base+"child/cpu.stat"] = &fstest.MapFile{Data: []byte("usage_usec 9999999\n")}
	root[base+"child/memory.current"] = &fstest.MapFile{Data: []byte("999999")}
	root[base+"child/io.stat"] = &fstest.MapFile{Data: []byte("8:0 rbytes=999999 wbytes=999999\n")}
	r.dependencies.sleep = func(_ context.Context, d time.Duration) error {
		*now = now.Add(d)
		root[base+"cpu.stat"].Data = []byte("usage_usec 350000\n")
		root[base+"io.stat"].Data = []byte("8:0 rbytes=200 wbytes=400\n8:16 rbytes=300 wbytes=400\n")
		return nil
	}
	got, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Services.Status != taskmanager.Available || len(got.Services.Items) != 1 {
		t.Fatalf("services: %+v", got.Services)
	}
	s := got.Services.Items[0]
	if s.CPUPercent == nil || *s.CPUPercent != 25 || *s.MemoryBytes != 4096 || *s.ReadBytesPerSecond != 200 || *s.WriteBytesPerSecond != 400 {
		t.Fatalf("hierarchical counters counted wrong: %+v", s)
	}
	invocation = "restart"
	*now = now.Add(time.Second)
	got, err = r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Services.Items[0].CPUPercent != nil || got.Services.Items[0].ReadBytesPerSecond != nil {
		t.Fatal("restart inherited baseline")
	}
	delete(root, base+"memory.current")
	*now = now.Add(time.Second)
	got, err = r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Services.Status != taskmanager.Partial || got.Services.Items[0].MemoryBytes != nil {
		t.Fatal("missing cgroup memory became zero")
	}
}

func TestTaskMalformedServiceResponseIsUnavailable(t *testing.T) {
	r, _, _ := taskFixture(t)
	r.dependencies.readTaskServices = func(context.Context) ([]byte, error) { return []byte("not a systemctl observation"), nil }
	got, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Services.Status != taskmanager.Unavailable {
		t.Fatalf("malformed response became empty services: %+v", got.Services)
	}
}

func TestTaskResetMissingIOAndPageSize(t *testing.T) {
	r, root, now := taskFixture(t)
	root["proc/42/stat"] = &fstest.MapFile{Data: processStat(42, 10, 20, 3)}
	got, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := got.Processes.Items[0]
	if got.Processes.Status != taskmanager.Partial || p.ReadBytesPerSecond != nil || *p.MemoryBytes != 3*uint64(os.Getpagesize()) {
		t.Fatalf("unknown IO/page size: %+v", p)
	}
	*now = now.Add(time.Second)
	root["proc/stat"].Data = []byte("cpu 1 0 0 1 0 0 0 0\ncpu0 0\ncpu1 0\n")
	root["proc/42/stat"].Data = processStat(42, 10, 5, 3)
	got, err = r.ReadTasks(context.Background())
	if err != nil || got.Processes.Items[0].CPUPercent != nil {
		t.Fatalf("counter reset: %+v %v", got, err)
	}
}

func TestTaskDisksPhysicalStableIDAndReset(t *testing.T) {
	r, root, now := taskFixture(t)
	r.dependencies.readBlockDevices = func(context.Context) ([]byte, error) {
		return []byte(`{"blockdevices":[{"name":"sda","type":"disk","model":"Fixture disk","size":4096,"serial":"abc","children":[{"name":"sda1","type":"part","size":2048}]},{"name":"dm-0","type":"lvm","size":1024}]}`), nil
	}
	root["sys/class/block/sda/stat"] = &fstest.MapFile{Data: []byte("0 0 10 0 0 0 20 0")}
	sleep := r.dependencies.sleep
	r.dependencies.sleep = func(ctx context.Context, d time.Duration) error {
		root["sys/class/block/sda/stat"].Data = []byte("0 0 12 0 0 0 24 0")
		return sleep(ctx, d)
	}
	got, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Disks.Items) != 1 || !strings.HasPrefix(got.Disks.Items[0].ID, "disk:") || *got.Disks.Items[0].ReadBytesPerSecond != 2048 || *got.Disks.Items[0].WriteBytesPerSecond != 4096 {
		t.Fatalf("disk sample: %+v", got.Disks)
	}
	*now = now.Add(time.Second)
	root["sys/class/block/sda/stat"].Data = []byte("0 0 1 0 0 0 1 0")
	got, err = r.ReadDeviceStatus(context.Background())
	if err != nil || got.Disks.Items[0].ReadBytesPerSecond != nil {
		t.Fatalf("disk reset: %+v %v", got.Disks, err)
	}
}

func TestTaskAcceleratorStates(t *testing.T) {
	root := fstest.MapFS{}
	if got := readTaskAccelerators(root, "accel", "accel"); got.Status != taskmanager.Absent {
		t.Fatal(got)
	}
	root["sys/class/accel/accel0/device/uevent"] = &fstest.MapFile{Data: []byte("DRIVER=ivpu\nPCI_ID=8086:7D1D\n")}
	if got := readTaskAccelerators(root, "accel", "accel"); got.Status != taskmanager.Unsupported || len(got.Items) != 1 {
		t.Fatal(got)
	}
	base := "sys/class/drm/card0/device/"
	root[base+"uevent"] = &fstest.MapFile{Data: []byte("DRIVER=amdgpu\nPCI_ID=1002:9999\n")}
	root[base+"gpu_busy_percent"] = &fstest.MapFile{Data: []byte("42")}
	root[base+"mem_info_vram_used"] = &fstest.MapFile{Data: []byte("100")}
	root[base+"mem_info_vram_total"] = &fstest.MapFile{Data: []byte("200")}
	root["sys/class/drm/card0-HDMI-A-1/status"] = &fstest.MapFile{Data: []byte("connected")}
	got := readTaskAccelerators(root, "drm", "card")
	if got.Status != taskmanager.Available || len(got.Items) != 1 || *got.Items[0].UsagePercent != 42 {
		t.Fatal(got)
	}
	root[base+"mem_info_vram_used"].Data = []byte("300")
	got = readTaskAccelerators(root, "drm", "card")
	if got.Status != taskmanager.Partial || got.Items[0].MemoryUsedBytes != nil || got.Items[0].MemoryUsedStatus != taskmanager.Unavailable || got.Items[0].MemoryTotalStatus != taskmanager.Available {
		t.Fatalf("invalid VRAM metrics published: %+v", got)
	}
	delete(root, base+"mem_info_vram_used")
	delete(root, base+"mem_info_vram_total")
	root[base+"gpu_busy_percent"].Data = []byte("bad")
	if got := readTaskAccelerators(root, "drm", "card"); got.Status != taskmanager.Partial {
		t.Fatal(got)
	}
}

func TestTaskBoundedReadsAndCancellationWhileWaiting(t *testing.T) {
	root := fstest.MapFS{"huge": &fstest.MapFile{Data: []byte(strings.Repeat("x", taskReadLimit+1))}}
	if _, err := readTaskFile(root, "huge"); err == nil {
		t.Fatal("unbounded read")
	}
	r, _, _ := taskFixture(t)
	if err := r.workloadTasks.gate.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.workloadTasks.gate.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := r.ReadTasks(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked request ignored deadline: %v", err)
	}
}

func TestTaskProcessAndServiceLimits(t *testing.T) {
	r, root, _ := taskFixture(t)
	for pid := 1; pid <= taskProcessLimit+1; pid++ {
		root[fmt.Sprintf("proc/%d/stat", pid)] = &fstest.MapFile{Data: processStat(pid, 1, 1, 1)}
		root[fmt.Sprintf("proc/%d/io", pid)] = &fstest.MapFile{Data: []byte("read_bytes: 0\nwrite_bytes: 0\n")}
	}
	processes, status := readTaskProcesses(context.Background(), root)
	if status != taskmanager.Partial || len(processes) != taskProcessLimit {
		t.Fatalf("unbounded processes: %d %s", len(processes), status)
	}
	root["sys/fs/cgroup/cgroup.controllers"] = &fstest.MapFile{}
	var services strings.Builder
	for i := 0; i < taskServiceLimit+1; i++ {
		fmt.Fprintf(&services, "Id=worker%d.service\nActiveState=active\nSubState=running\n\n", i)
	}
	r.dependencies.readTaskServices = func(context.Context) ([]byte, error) { return []byte(services.String()), nil }
	rows, status := r.readTaskServices(context.Background())
	if status != taskmanager.Partial || len(rows) != taskServiceLimit {
		t.Fatalf("unbounded services: %d %s", len(rows), status)
	}
}

func TestTaskAcceleratorIdentitySurvivesCardRenumber(t *testing.T) {
	root := fstest.MapFS{"sys/class/drm/card0/device/uevent": &fstest.MapFile{Data: []byte("DRIVER=i915\nPCI_ID=8086:1234\nPCI_SLOT_NAME=0000:00:02.0\n")}}
	before := readTaskAccelerators(root, "drm", "card").Items[0].ID
	root["sys/class/drm/card1/device/uevent"] = root["sys/class/drm/card0/device/uevent"]
	delete(root, "sys/class/drm/card0/device/uevent")
	after := readTaskAccelerators(root, "drm", "card").Items[0].ID
	if before != after {
		t.Fatalf("PCI device identity changed with card index: %s != %s", before, after)
	}
}

func TestTaskInterfaceReuseDoesNotInheritRate(t *testing.T) {
	r, root, now := taskFixture(t)
	root["sys/class/net/eth0/ifindex"] = &fstest.MapFile{Data: []byte("2")}
	before, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root["sys/class/net/eth0/ifindex"].Data = []byte("3")
	*now = now.Add(time.Second)
	after, err := r.ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Network.Items[0].ID == after.Network.Items[0].ID || after.Network.Items[0].ReceiveBytesPerSecond != nil {
		t.Fatalf("replaced interface inherited baseline: %+v", after.Network)
	}
}

func TestTaskSubprocessLatencyDoesNotSkewCPUWindow(t *testing.T) {
	r, root, now := taskFixture(t)
	r.dependencies.readBlockDevices = func(context.Context) ([]byte, error) {
		*now = now.Add(time.Second)
		root["proc/stat"].Data = []byte("cpu 200 0 0 1000 0 0 0 0\ncpu0 0\ncpu1 0\n")
		return []byte(`{"blockdevices":[]}`), nil
	}
	sample, err := r.sampleTaskDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sample.cpu.cpuTotal != 1200 {
		t.Fatalf("CPU captured before blocking subprocess: %d", sample.cpu.cpuTotal)
	}
}

func TestTaskCommandOverflowReportsPartial(t *testing.T) {
	var output taskOutput
	_, overflow := output.Write([]byte(strings.Repeat("x", taskReadLimit+1)))
	if overflow == nil || output.Len() > taskReadLimit {
		t.Fatal("command output not bounded")
	}
	r, _, _ := taskFixture(t)
	r.dependencies.readTaskServices = func(context.Context) ([]byte, error) { return nil, overflow }
	_, status := r.readTaskServices(context.Background())
	if status != taskmanager.Partial {
		t.Fatalf("truncated enumeration status: %s", status)
	}
}

func TestTaskCommandCopyCannotBypassOutputLimit(t *testing.T) {
	var output taskOutput
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", taskReadLimit+1))}
	_, err := io.Copy(&output, source)
	if err == nil || output.Len() > taskReadLimit {
		t.Fatalf("io.Copy bypassed bound: %d bytes, %v", output.Len(), err)
	}
}

func TestTaskAcceleratorMetricAvailability(t *testing.T) {
	for _, tt := range []struct {
		name  string
		usage *string
		want  taskmanager.Status
	}{{"missing", nil, taskmanager.Unsupported}, {"malformed", taskString("bad"), taskmanager.Unavailable}, {"out of range", taskString("101"), taskmanager.Unavailable}, {"valid", taskString("42"), taskmanager.Available}} {
		t.Run(tt.name, func(t *testing.T) {
			base := "sys/class/drm/card0/device/"
			root := fstest.MapFS{base + "uevent": &fstest.MapFile{Data: []byte("DRIVER=amdgpu\n")}, base + "mem_info_vram_used": &fstest.MapFile{Data: []byte("100")}, base + "mem_info_vram_total": &fstest.MapFile{Data: []byte("200")}}
			if tt.usage != nil {
				root[base+"gpu_busy_percent"] = &fstest.MapFile{Data: []byte(*tt.usage)}
			}
			got := readTaskAccelerators(root, "drm", "card")
			a := got.Items[0]
			if a.UsageStatus != tt.want || a.MemoryUsedStatus != taskmanager.Available || a.MemoryTotalStatus != taskmanager.Available || a.MemoryUsedBytes == nil || *a.MemoryUsedBytes != 100 || a.MemoryTotalBytes == nil || *a.MemoryTotalBytes != 200 {
				t.Fatalf("metric availability lost valid VRAM or error cause: %+v", a)
			}
			if tt.want != taskmanager.Available && (a.UsagePercent != nil || a.Status != taskmanager.Partial) {
				t.Fatalf("mixed availability: %+v", a)
			}
		})
	}
}
func taskString(s string) *string { return &s }

type taskMetricReadErrorFS struct {
	fs.FS
	blocked string
}

func (f taskMetricReadErrorFS) Open(name string) (fs.File, error) {
	if name == f.blocked {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return f.FS.Open(name)
}
func TestTaskAcceleratorMetricReadFailure(t *testing.T) {
	base := "sys/class/drm/card0/device/"
	root := fstest.MapFS{base + "uevent": &fstest.MapFile{Data: []byte("DRIVER=amdgpu\n")}, base + "mem_info_vram_used": &fstest.MapFile{Data: []byte("100")}, base + "mem_info_vram_total": &fstest.MapFile{Data: []byte("200")}}
	got := readTaskAccelerators(taskMetricReadErrorFS{root, base + "gpu_busy_percent"}, "drm", "card")
	a := got.Items[0]
	if a.UsageStatus != taskmanager.Unavailable || a.Status != taskmanager.Partial || a.MemoryUsedStatus != taskmanager.Available || a.MemoryTotalStatus != taskmanager.Available {
		t.Fatalf("read failure mislabeled unsupported: %+v", a)
	}
}
func TestTaskAcceleratorUnimplementedDriverStatuses(t *testing.T) {
	root := fstest.MapFS{"sys/class/accel/accel0/device/uevent": &fstest.MapFile{Data: []byte("DRIVER=ivpu\n")}}
	a := readTaskAccelerators(root, "accel", "accel").Items[0]
	if a.Status != taskmanager.Unsupported || a.UsageStatus != taskmanager.Unsupported || a.MemoryUsedStatus != taskmanager.Unsupported || a.MemoryTotalStatus != taskmanager.Unsupported {
		t.Fatalf("unknown driver metric status: %+v", a)
	}
}
