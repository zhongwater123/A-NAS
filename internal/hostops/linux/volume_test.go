package linux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestCreateVolumeFormatsEveryDiskWithTheLayoutsProfiles(t *testing.T) {
	host := newDiskHost(t, "disk:first", "disk:second")
	executor := host.executor()

	operation := host.finish(t, executor, startCreate(t, executor, "plan:mirror", storage.LayoutMirror, host.targets("disk:first", "disk:second")...))
	if operation.State != storage.OperationSucceeded {
		t.Fatalf("operation = %+v, want succeeded", operation)
	}
	volume := operation.Volume
	if volume == nil || !slices.Equal(volume.DiskIDs, []string{"disk:first", "disk:second"}) || volume.Layout != storage.LayoutMirror || volume.CapacityBytes != 500_000_000_000 {
		t.Fatalf("volume = %+v", volume)
	}
	// Each new partition is wiped too: an md superblock inside the old one
	// would otherwise reappear where the new partition starts.
	want := []string{
		"wipefs --all /dev/sda", "parted --script /dev/sda mklabel gpt mkpart primary btrfs 1MiB 100%", "partprobe /dev/sda", "udevadm settle --timeout=10", "wipefs --all /dev/sda1",
		"wipefs --all /dev/sdb", "parted --script /dev/sdb mklabel gpt mkpart primary btrfs 1MiB 100%", "partprobe /dev/sdb", "udevadm settle --timeout=10", "wipefs --all /dev/sdb1",
		"mkfs.btrfs --force --label ANAS_DATA --data raid1 --metadata raid1 /dev/sda1 /dev/sdb1",
	}
	if got := host.lines(); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("commands =\n%s\nwant to start with\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	marker := readMarker(t, host.mountPoint)
	if marker["layout"] != "mirror" || marker["filesystemUuid"] != host.uuid(1) || marker["planId"] != "plan:mirror" {
		t.Fatalf("marker = %v, want the mirror layout and the plan recorded", marker)
	}
}

func TestCreateVolumeRefusesADiskThatChangedOrIsInUse(t *testing.T) {
	for _, name := range []string{"changed", "in use"} {
		host := newDiskHost(t, "disk:first")
		fingerprint := storage.FingerprintDisk(host.disks["disk:first"].Disk)
		device := host.disks["disk:first"]
		if name == "changed" {
			device.Disk.CapacityBytes++
		} else {
			device.Disk.InUse = true
			fingerprint = storage.FingerprintDisk(device.Disk)
		}
		host.disks["disk:first"] = device
		executor := host.executor()
		operation := host.finish(t, executor, startCreate(t, executor, "plan:x", storage.LayoutSingle, storage.DiskTarget{DiskID: "disk:first", Fingerprint: fingerprint}))
		if operation.State != storage.OperationFailed || operation.Step != "" || len(host.lines()) != 0 {
			t.Fatalf("%s disk: operation = %+v, commands = %v; want refused before any command", name, operation, host.lines())
		}
	}
}

// Closing the page cancels the request that started the operation; the
// disks must not notice (issue #165).
func TestAVolumeOperationOutlivesTheRequestThatStartedIt(t *testing.T) {
	host := newDiskHost(t, "disk:first")
	host.blockAt = "mkfs.btrfs"
	executor := host.executor()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := executor.StartCreateVolume(ctx, storage.CreateVolumeRequest{PlanID: "plan:page", Layout: storage.LayoutSingle, Disks: host.targets("disk:first")}); err != nil {
		t.Fatal(err)
	}
	host.waitBlocked(t)
	cancel()
	host.unblock()
	operation := host.finish(t, executor, "plan:page")
	if operation.State != storage.OperationSucceeded || host.killed {
		t.Fatalf("operation = %+v, killed = %v; want it finished after the request ended", operation, host.killed)
	}
}

// The Host Agent stops (a restart or a power cut) in each step of creating
// the volume. The journal then reads interrupted, and a retry finishes the
// volume: before it mounted, from the start, since nothing can be on it;
// after, without erasing or formatting again.
func TestCreatingTheVolumeContinuesAfterTheHostAgentStopsInAnyStep(t *testing.T) {
	for _, test := range []struct {
		stopAt  string
		step    storage.OperationStep
		mounted bool
	}{
		{"wipefs --all /dev/sdb", storage.StepErase, false},
		{"mkfs.btrfs", storage.StepFormat, false},
		{"systemctl enable --now", storage.StepMount, false},
		{"btrfs subvolume create", storage.StepPrepare, true},
	} {
		t.Run(string(test.step), func(t *testing.T) {
			host := newDiskHost(t, "disk:first", "disk:second")
			host.blockAt = test.stopAt
			first := host.executor()
			startCreate(t, first, "plan:cut", storage.LayoutMirror, host.targets("disk:first", "disk:second")...)
			host.waitBlocked(t)

			// The Host Agent starts again while the command still hangs.
			restarted := host.executor()
			operation, err := restarted.VolumeOperation(context.Background(), "plan:cut")
			if err != nil || operation.State != storage.OperationInterrupted || operation.Step != test.step || !strings.Contains(operation.Failure, string(test.step)) {
				t.Fatalf("operation after the restart = %+v, %v; want interrupted in %s", operation, err, test.step)
			}
			host.abandon()
			if (host.mounted != nil) != test.mounted {
				t.Fatalf("mounted = %+v, want mounted %v", host.mounted, test.mounted)
			}

			host.set(func() { host.commands = nil })
			operation = host.finish(t, restarted, startCreate(t, restarted, "plan:cut", storage.LayoutMirror, host.targets("disk:first", "disk:second")...))
			if operation.State != storage.OperationSucceeded {
				t.Fatalf("retry = %+v, want succeeded", operation)
			}
			erased := slices.ContainsFunc(host.lines(), func(line string) bool { return strings.HasPrefix(line, "wipefs") || strings.HasPrefix(line, "mkfs") })
			if erased == test.mounted {
				t.Fatalf("retry commands =\n%s\nwant erasing and formatting only before the volume mounted", strings.Join(host.lines(), "\n"))
			}
			marker := readMarker(t, host.mountPoint)
			if host.mounted == nil || marker["filesystemUuid"] != host.mounted.FilesystemUUID || marker["planId"] != "plan:cut" {
				t.Fatalf("marker = %v, mounted = %+v; want the mounted volume marked for the plan", marker, host.mounted)
			}
			if unit := host.unitUUID(t); unit != host.mounted.FilesystemUUID {
				t.Fatalf("mount unit names %q, want the mounted volume %q", unit, host.mounted.FilesystemUUID)
			}
		})
	}
}

// A disk command that is killed fails the step; a retry continues in the
// same way as after a restart.
func TestCreatingTheVolumeContinuesAfterAKilledCommand(t *testing.T) {
	host := newDiskHost(t, "disk:first")
	host.failAt = "btrfs subvolume create"
	executor := host.executor()
	operation := host.finish(t, executor, startCreate(t, executor, "plan:killed", storage.LayoutSingle, host.targets("disk:first")...))
	if operation.State != storage.OperationFailed || operation.Step != storage.StepPrepare || strings.Contains(operation.Failure, "/dev/") {
		t.Fatalf("operation = %+v, want failed in prepare without device paths", operation)
	}
	host.set(func() { host.failAt, host.commands = "", nil })
	operation = host.finish(t, executor, startCreate(t, executor, "plan:killed", storage.LayoutSingle, host.targets("disk:first")...))
	if operation.State != storage.OperationSucceeded || slices.ContainsFunc(host.lines(), func(line string) bool { return strings.HasPrefix(line, "mkfs") }) {
		t.Fatalf("retry = %+v, commands = %v; want the mounted volume prepared without formatting", operation, host.lines())
	}
	// Asking again changes nothing.
	host.set(func() { host.commands = nil })
	if again := startCreate(t, executor, "plan:killed", storage.LayoutSingle, host.targets("disk:first")...); again != "plan:killed" || len(host.lines()) != 0 {
		t.Fatalf("a succeeded plan ran commands again: %v", host.lines())
	}
}

// A data volume, or a mount unit for one, that the plan did not make stops
// the creation before any disk is touched (issue #165).
func TestCreateVolumeRefusesAVolumeThePlanDidNotMake(t *testing.T) {
	t.Run("mounted", func(t *testing.T) {
		host := newDiskHost(t, "disk:first", "disk:old")
		host.mount("aaaaaaaa-0000-4000-8000-000000000001", "disk:old")
		writeMarker(t, host.mountPoint, `{"filesystemUuid":"aaaaaaaa-0000-4000-8000-000000000001","diskId":"disk:old","formatVersion":1,"planId":"plan:old"}`)
		executor := host.executor()
		operation := host.finish(t, executor, startCreate(t, executor, "plan:new", storage.LayoutSingle, host.targets("disk:first")...))
		if operation.State != storage.OperationFailed || operation.Failure != storage.ErrVolumeExists.Error() || len(host.lines()) != 0 {
			t.Fatalf("operation = %+v, commands = %v; want refused before any command", operation, host.lines())
		}
	})
	t.Run("offline", func(t *testing.T) {
		host := newDiskHost(t, "disk:first")
		host.writeUnit(t, "aaaaaaaa-0000-4000-8000-000000000001")
		executor := host.executor()
		operation := host.finish(t, executor, startCreate(t, executor, "plan:new", storage.LayoutSingle, host.targets("disk:first")...))
		if operation.State != storage.OperationFailed || operation.Failure != storage.ErrVolumeExists.Error() {
			t.Fatalf("operation = %+v, want refused", operation)
		}
		if slices.ContainsFunc(host.lines(), func(line string) bool { return !strings.HasPrefix(line, "blkid --probe") }) {
			t.Fatalf("commands = %v, want only probes", host.lines())
		}
		if unit := host.unitUUID(t); unit != "aaaaaaaa-0000-4000-8000-000000000001" {
			t.Fatalf("mount unit names %q, want it left alone", unit)
		}
	})
	t.Run("on the planned disk", func(t *testing.T) {
		host := newDiskHost(t, "disk:first")
		host.writeUnit(t, "aaaaaaaa-0000-4000-8000-000000000001")
		host.partitionUUIDs = map[string]string{"/dev/sda1": "aaaaaaaa-0000-4000-8000-000000000001"}
		executor := host.executor()
		operation := host.finish(t, executor, startCreate(t, executor, "plan:new", storage.LayoutSingle, host.targets("disk:first")...))
		if operation.State != storage.OperationSucceeded || host.unitUUID(t) != host.mounted.FilesystemUUID {
			t.Fatalf("operation = %+v; want the unit of a filesystem the plan erases replaced", operation)
		}
	})
}

// A plan an older release ran, or whose journal entry is gone, made the
// mounted volume: its marker names the plan's first disk and the volume is
// on exactly the plan's disks. Retrying takes the volume over.
func TestCreateVolumeTakesOverTheVolumeItsPlanMade(t *testing.T) {
	host := newDiskHost(t, "disk:first")
	host.mount("aaaaaaaa-0000-4000-8000-000000000001", "disk:first")
	host.writeUnit(t, "aaaaaaaa-0000-4000-8000-000000000001")
	writeMarker(t, host.mountPoint, `{"filesystemUuid":"aaaaaaaa-0000-4000-8000-000000000001","diskId":"disk:first","formatVersion":1}`)
	executor := host.executor()
	operation := host.finish(t, executor, startCreate(t, executor, "plan:old", storage.LayoutSingle, host.targets("disk:first")...))
	if operation.State != storage.OperationSucceeded || operation.Volume.FilesystemUUID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("operation = %+v, want the mounted volume taken over", operation)
	}
	if slices.ContainsFunc(host.lines(), func(line string) bool { return strings.HasPrefix(line, "wipefs") || strings.HasPrefix(line, "mkfs") }) {
		t.Fatalf("commands = %v, want nothing erased", host.lines())
	}
	if marker := readMarker(t, host.mountPoint); marker["planId"] != "plan:old" {
		t.Fatalf("marker = %v, want the plan recorded", marker)
	}
}

func TestAddDisksNeedsTheMountedDataVolume(t *testing.T) {
	host := newDiskHost(t, "disk:second")
	executor := host.executor()
	operation := host.finish(t, executor, startAdd(t, executor, "plan:add", storage.LayoutMirror, host.targets("disk:second")...))
	if operation.State != storage.OperationFailed || operation.Failure != storage.ErrVolumeUnavailable.Error() || len(host.lines()) != 0 {
		t.Fatalf("operation on an unmounted volume = %+v, commands = %v; want refused before any command", operation, host.lines())
	}
}

// The layout is recorded before Btrfs adds the disks. Killed right after
// the disks joined, the retry skips them and only records the layout.
func TestAddingDisksRecordsTheLayoutFirstAndContinuesAfterAKill(t *testing.T) {
	host := newDiskHost(t, "disk:first", "disk:second")
	host.mount(volumeUUID, "disk:first")
	writeMarker(t, host.mountPoint, `{"filesystemUuid":"`+volumeUUID+`","diskId":"disk:first","formatVersion":1}`)
	host.blockAt = "btrfs device add"
	executor := host.executor()
	startAdd(t, executor, "plan:add", storage.LayoutMirror, host.targets("disk:second")...)
	host.waitBlocked(t)
	marker := readMarker(t, host.mountPoint)
	pending, _ := marker["pending"].(map[string]any)
	if marker["layout"] != nil || pending["layout"] != "mirror" || pending["planId"] != "plan:add" || pending["devices"] != float64(2) {
		t.Fatalf("marker before the disks join = %v, want the mirror pending for two devices", marker)
	}
	// Btrfs added the disk, then the run was killed.
	host.set(func() { host.killAfter = true })
	host.unblock()
	operation := host.finish(t, executor, "plan:add")
	if operation.State != storage.OperationFailed || operation.Step != storage.StepAddDevices || len(host.mounted.DiskIDs) != 2 {
		t.Fatalf("operation = %+v, members = %v; want killed after the disk joined", operation, host.mounted.DiskIDs)
	}

	host.set(func() { host.blockAt, host.killAfter, host.commands = "", false, nil })
	operation = host.finish(t, executor, startAdd(t, executor, "plan:add", storage.LayoutMirror, host.targets("disk:second")...))
	if operation.State != storage.OperationSucceeded || len(host.lines()) != 0 {
		t.Fatalf("retry = %+v, commands = %v; want the member skipped", operation, host.lines())
	}
	marker = readMarker(t, host.mountPoint)
	if marker["layout"] != "mirror" || marker["pending"] != nil {
		t.Fatalf("marker = %v, want the mirror recorded", marker)
	}
}

func TestOneVolumeOperationRunsAtATime(t *testing.T) {
	host := newDiskHost(t, "disk:first", "disk:second")
	host.blockAt = "mkfs.btrfs"
	executor := host.executor()
	startCreate(t, executor, "plan:one", storage.LayoutSingle, host.targets("disk:first")...)
	host.waitBlocked(t)
	if _, err := executor.StartCreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:two", Layout: storage.LayoutSingle, Disks: host.targets("disk:second"),
	}); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("second operation error = %v, want ErrPlanState", err)
	}
	if _, err := executor.StartCreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:one", Layout: storage.LayoutSingle, Disks: host.targets("disk:second"),
	}); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("the plan asking for other disks error = %v, want ErrPlanState", err)
	}
	if again, err := executor.StartCreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:one", Layout: storage.LayoutSingle, Disks: host.targets("disk:first"),
	}); err != nil || again.State != storage.OperationRunning {
		t.Fatalf("starting the running plan again = %+v, %v; want it reported running", again, err)
	}
	host.unblock()
	host.finish(t, executor, "plan:one")
	if operation, err := host.executor().VolumeOperation(context.Background(), "plan:one"); err != nil || operation.State != storage.OperationSucceeded {
		t.Fatalf("journal after a restart = %+v, %v; want succeeded", operation, err)
	}
}

const volumeUUID = "09e275fe-794a-458d-8200-b6e67c55cc22"

// diskHost stands in for the disks, the kernel and systemd: it records the
// commands, formats with a new UUID each time, mounts the data volume when
// systemd starts the mount unit, and lets Btrfs add disks to it. A command
// can block until released, or fail as if killed.
type diskHost struct {
	t          *testing.T
	systemRoot string
	mountPoint string

	mu             sync.Mutex
	disks          map[string]linux.BlockDevice
	mounted        *hoststate.DataVolume
	formats        int
	formatted      []string
	partitionUUIDs map[string]string
	commands       []string
	killed         bool
	// blockAt holds the first command starting with it; failAt fails it.
	blockAt, failAt string
	// killAfter fails the blocked command after it took effect.
	killAfter bool
	blocked   chan struct{}
	release   chan struct{}
	abandoned bool
}

func newDiskHost(t *testing.T, diskIDs ...string) *diskHost {
	t.Helper()
	host := &diskHost{
		t: t, systemRoot: t.TempDir(), mountPoint: filepath.Join(t.TempDir(), "data"),
		disks: make(map[string]linux.BlockDevice), blocked: make(chan struct{}), release: make(chan struct{}),
	}
	for i, id := range diskIDs {
		name := "sd" + string(rune('a'+i))
		host.disks[id] = linux.BlockDevice{Disk: spareDisk(t, id), DevicePath: "/dev/" + name, PartitionPath: "/dev/" + name + "1"}
	}
	t.Cleanup(host.abandon)
	return host
}

// executor is a Host Agent on the host; a second one is the same Host Agent
// after a restart.
func (h *diskHost) executor() *linux.Executor {
	return linux.NewExecutor(h, h, linux.Options{SystemRoot: h.systemRoot, MountPoint: h.mountPoint, MountUnitName: "a-nas-data.mount"})
}

func (h *diskHost) targets(ids ...string) []storage.DiskTarget {
	targets := make([]storage.DiskTarget, len(ids))
	for i, id := range ids {
		targets[i] = storage.DiskTarget{DiskID: id, Fingerprint: storage.FingerprintDisk(h.disks[id].Disk)}
	}
	return targets
}

func (h *diskHost) uuid(n int) string { return fmt.Sprintf("11111111-2222-3333-4444-%012d", n) }

func (h *diskHost) ResolveDevice(_ context.Context, diskID string) (linux.BlockDevice, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	device, ok := h.disks[diskID]
	if !ok {
		return linux.BlockDevice{}, errors.New("unknown disk")
	}
	if h.mounted != nil && slices.ContainsFunc(h.mounted.DiskIDs, func(id hoststate.ResourceID) bool { return id.String() == diskID }) {
		device.Disk.Role, device.Disk.InUse = hoststate.DiskRoleData, true
	}
	return device, nil
}

func (h *diskHost) MountedDataVolume(context.Context) (*hoststate.DataVolume, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mounted == nil {
		return nil, nil
	}
	volume := *h.mounted
	volume.DiskIDs = append([]hoststate.ResourceID(nil), volume.DiskIDs...)
	return &volume, nil
}

func (h *diskHost) Run(ctx context.Context, name string, args []string, _ string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	h.mu.Lock()
	h.commands = append(h.commands, line)
	block := h.blockAt != "" && strings.HasPrefix(line, h.blockAt)
	if block {
		h.blockAt = ""
	}
	failing := h.failAt != "" && strings.HasPrefix(line, h.failAt)
	h.mu.Unlock()
	if block {
		close(h.blocked)
		select {
		case <-h.release:
		case <-ctx.Done():
			h.mu.Lock()
			h.killed = true
			h.mu.Unlock()
			return nil, errors.New("signal: killed")
		}
		h.mu.Lock()
		abandoned, killAfter := h.abandoned, h.killAfter
		h.mu.Unlock()
		if abandoned {
			// The Host Agent that ran it is gone.
			return nil, errors.New("signal: killed")
		}
		if killAfter {
			h.apply(name, args)
			return []byte("killed on /dev/sdb1"), errors.New("signal: killed")
		}
	}
	if failing {
		return []byte("killed on /dev/sda1"), errors.New("signal: killed")
	}
	return h.apply(name, args), nil
}

// apply makes the command take effect on the host.
func (h *diskHost) apply(name string, args []string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case name == "mkfs.btrfs":
		h.formats++
		h.formatted = nil
		for _, partition := range args {
			for id, device := range h.disks {
				if device.PartitionPath == partition {
					h.formatted = append(h.formatted, id)
				}
			}
		}
	case name == "blkid" && args[0] == "--probe":
		return []byte(h.partitionUUIDs[args[len(args)-1]] + "\n")
	case name == "blkid":
		return []byte(h.uuid(h.formats) + "\n")
	case name == "systemctl" && args[0] == "enable":
		slices.Sort(h.formatted)
		h.mountLocked(h.uuid(h.formats), h.formatted...)
	case name == "btrfs" && args[0] == "device" && args[1] == "add":
		for _, partition := range args[3 : len(args)-1] {
			for id, device := range h.disks {
				if device.PartitionPath == partition {
					h.mounted.DiskIDs = append(h.mounted.DiskIDs, resourceID(h.t, id))
				}
			}
		}
	}
	return nil
}

func (h *diskHost) mount(uuid string, diskIDs ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mountLocked(uuid, diskIDs...)
}

func (h *diskHost) mountLocked(uuid string, diskIDs ...string) {
	h.mounted = &hoststate.DataVolume{FilesystemUUID: uuid}
	for _, id := range diskIDs {
		h.mounted.DiskIDs = append(h.mounted.DiskIDs, resourceID(h.t, id))
	}
	if err := os.MkdirAll(h.mountPoint, 0o755); err != nil {
		h.t.Error(err)
	}
}

func (h *diskHost) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-h.blocked:
	case <-time.After(5 * time.Second):
		t.Fatalf("no command blocked; commands = %v", h.lines())
	}
}

func (h *diskHost) unblock() { close(h.release) }

// set changes the host while operations may run.
func (h *diskHost) set(change func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	change()
}

// abandon ends a blocked command of a Host Agent that is gone.
func (h *diskHost) abandon() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.abandoned {
		h.abandoned = true
		select {
		case <-h.release:
		default:
			close(h.release)
		}
	}
}

// finish waits until the plan's operation ends.
func (h *diskHost) finish(t *testing.T, executor *linux.Executor, planID string) storage.VolumeOperation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		operation, err := executor.VolumeOperation(context.Background(), planID)
		if err != nil {
			t.Fatalf("VolumeOperation(%s) error = %v", planID, err)
		}
		if operation.State != storage.OperationRunning {
			return operation
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s still running; commands = %v", planID, h.lines())
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *diskHost) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...)
}

func (h *diskHost) writeUnit(t *testing.T, uuid string) {
	t.Helper()
	path := filepath.Join(h.systemRoot, "etc", "systemd", "system", "a-nas-data.mount")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Mount]\nWhat=UUID="+uuid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *diskHost) unitUUID(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(h.systemRoot, "etc", "systemd", "system", "a-nas-data.mount"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if value, found := strings.CutPrefix(line, "What=UUID="); found {
			return value
		}
	}
	return ""
}

func startCreate(t *testing.T, executor *linux.Executor, planID string, layout storage.Layout, disks ...storage.DiskTarget) string {
	t.Helper()
	if _, err := executor.StartCreateVolume(context.Background(), storage.CreateVolumeRequest{PlanID: planID, Layout: layout, Disks: disks}); err != nil {
		t.Fatalf("StartCreateVolume() error = %v", err)
	}
	return planID
}

func startAdd(t *testing.T, executor *linux.Executor, planID string, layout storage.Layout, disks ...storage.DiskTarget) string {
	t.Helper()
	if _, err := executor.StartAddDisks(context.Background(), storage.AddDisksRequest{PlanID: planID, FilesystemUUID: volumeUUID, Layout: layout, Disks: disks}); err != nil {
		t.Fatalf("StartAddDisks() error = %v", err)
	}
	return planID
}

func spareDisk(t *testing.T, id string) hoststate.Disk {
	t.Helper()
	return hoststate.Disk{ID: resourceID(t, id), Model: "ST500DM002-1BD142", Transport: hoststate.TransportSATA, CapacityBytes: 500_000_000_000, Role: hoststate.DiskRoleUnassigned}
}

func resourceID(t *testing.T, id string) hoststate.ResourceID {
	t.Helper()
	diskID, err := hoststate.NewResourceID(id)
	if err != nil {
		t.Fatal(err)
	}
	return diskID
}

func readMarker(t *testing.T, mountPoint string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(mountPoint, ".a-nas-volume.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal(contents, &marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

func writeMarker(t *testing.T, mountPoint, contents string) {
	t.Helper()
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountPoint, ".a-nas-volume.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
