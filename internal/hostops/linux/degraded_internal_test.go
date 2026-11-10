package linux

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

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

const degradedUUID = "1bf2da8f-c403-422e-83bb-ea5f87518214"

func TestOnlyAMirrorMissingOneDiskRunsOnTheDisksItLastRanOn(t *testing.T) {
	mirror := volumeMarker{FilesystemUUID: degradedUUID, FormatVersion: 1, Layout: datavolume.LayoutMirror}
	oneGone := []linuxhoststate.BtrfsDevice{{ID: 1}, {ID: 2, Missing: true}}
	cases := []struct {
		name      string
		marker    volumeMarker
		devices   []linuxhoststate.BtrfsDevice
		record    *degradedRecord
		survivors []uint64
	}{
		{name: "mirror without one disk", marker: mirror, devices: oneGone, survivors: []uint64{1}},
		{name: "span", marker: volumeMarker{FilesystemUUID: degradedUUID, Layout: datavolume.LayoutSpan}, devices: oneGone},
		{name: "stripe", marker: volumeMarker{FilesystemUUID: degradedUUID, Layout: datavolume.LayoutStripe}, devices: oneGone},
		{name: "another filesystem's marker", marker: volumeMarker{FilesystemUUID: volumeUUIDOther, Layout: datavolume.LayoutMirror}, devices: oneGone},
		{name: "nothing missing", marker: mirror, devices: []linuxhoststate.BtrfsDevice{{ID: 1}, {ID: 2}}},
		{name: "two of three missing", marker: mirror, devices: []linuxhoststate.BtrfsDevice{{ID: 1}, {ID: 2, Missing: true}, {ID: 3, Missing: true}}},
		{name: "the disk it last ran on", marker: mirror, devices: oneGone,
			record: &degradedRecord{FilesystemUUID: degradedUUID, Since: time.Unix(1, 0), Survivors: []uint64{1}}, survivors: []uint64{1}},
		{name: "only the stale disk", marker: mirror, devices: []linuxhoststate.BtrfsDevice{{ID: 1, Missing: true}, {ID: 2}},
			record: &degradedRecord{FilesystemUUID: degradedUUID, Since: time.Unix(1, 0), Survivors: []uint64{1}}},
		{name: "a record of another volume", marker: mirror, devices: []linuxhoststate.BtrfsDevice{{ID: 1, Missing: true}, {ID: 2}},
			record: &degradedRecord{FilesystemUUID: volumeUUIDOther, Since: time.Unix(1, 0), Survivors: []uint64{1}}, survivors: []uint64{2}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			survivors, refusal := degradedMountRefusal(test.marker, degradedUUID, test.devices, test.record)
			if test.survivors == nil && refusal == "" {
				t.Fatalf("degradedMountRefusal() allowed a mount on %v", survivors)
			}
			if test.survivors != nil && (refusal != "" || !slices.Equal(survivors, test.survivors)) {
				t.Fatalf("degradedMountRefusal() = %v, %q; want %v", survivors, refusal, test.survivors)
			}
		})
	}
}

const volumeUUIDOther = "aaaaaaaa-0000-4000-8000-000000000000"

// degradedHost stands in for systemd, blkid and the kernel: a mount through
// systemd-run puts the marker and the devices the kernel found in place, an
// unmount takes them away.
type degradedHost struct {
	t                *testing.T
	root, mountPoint string
	marker           volumeMarker
	devices          map[string][2]string // devid -> missing, writeable
	unitState        string
	// activeEnter is the mount unit's ActiveEnterTimestampMonotonic: 0 when
	// it has not been active since boot.
	activeEnter string
	members     string
	blkidStatus exitStatus
	// replaceStatus are the lines "btrfs replace status -1" prints, in turn.
	replaceStatus []string
	// mounted is the data volume the host state shows, and spares the disks
	// it can resolve.
	mounted  *hoststate.DataVolume
	spares   map[string]BlockDevice
	mu       sync.Mutex
	commands []string
}

// exitStatus is the error of a command that exited with the status.
type exitStatus int

func (s exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }
func (s exitStatus) ExitCode() int { return int(s) }

func newDegradedHost(t *testing.T, layout datavolume.Layout) *degradedHost {
	t.Helper()
	host := &degradedHost{
		t: t, root: t.TempDir(), mountPoint: filepath.Join(t.TempDir(), "data"),
		marker:    volumeMarker{FilesystemUUID: degradedUUID, FormatVersion: 1, Layout: layout},
		devices:   map[string][2]string{"1": {"0", "1"}, "2": {"1", "0"}},
		unitState: "failed", activeEnter: "0", members: "/dev/sda1\n",
	}
	unit := filepath.Join(host.root, "etc", "systemd", "system", "a-nas-data.mount")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte(mountUnit(degradedUUID, host.mountPoint)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(host.mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	return host
}

func (h *degradedHost) executor() *Executor {
	return NewExecutor(h, h, Options{SystemRoot: h.root, MountPoint: h.mountPoint, MountUnitName: "a-nas-data.mount", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
}

func (h *degradedHost) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	h.mu.Lock()
	h.commands = append(h.commands, line)
	h.mu.Unlock()
	if name != "systemd-run" {
		return nil, nil
	}
	command := args[slices.Index(args, "--")+1:]
	sysfs := filepath.Join(h.root, "sys", "fs", "btrfs", degradedUUID)
	switch command[0] {
	case "mount":
		contents, _ := json.Marshal(h.marker)
		if err := os.WriteFile(filepath.Join(h.mountPoint, ".a-nas-volume.json"), contents, 0o644); err != nil {
			h.t.Fatal(err)
		}
		for devid, flags := range h.devices {
			dir := filepath.Join(sysfs, "devinfo", devid)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				h.t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(dir, "missing"), []byte(flags[0]+"\n"), 0o644)
			_ = os.WriteFile(filepath.Join(dir, "writeable"), []byte(flags[1]+"\n"), 0o644)
		}
	case "umount":
		_ = os.Remove(filepath.Join(h.mountPoint, ".a-nas-volume.json"))
		_ = os.RemoveAll(sysfs)
	}
	return nil, nil
}

func (h *degradedHost) Output(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	h.mu.Lock()
	h.commands = append(h.commands, strings.Join(append([]string{name}, args...), " "))
	h.mu.Unlock()
	switch name {
	case "systemctl":
		return []byte("ActiveState=" + h.unitState + "\nActiveEnterTimestampMonotonic=" + h.activeEnter + "\n"), nil
	case "blkid":
		if h.blkidStatus != 0 {
			return nil, h.blkidStatus
		}
		if h.members == "" {
			return nil, exitStatus(2)
		}
		return []byte(h.members), nil
	case "btrfs":
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.replaceStatus) == 0 {
			return []byte("Never started\n"), nil
		}
		line := h.replaceStatus[0]
		h.replaceStatus = h.replaceStatus[1:]
		return []byte(line + "\n"), nil
	}
	return nil, errors.New("unexpected command " + name)
}

func (h *degradedHost) MountedDataVolume(context.Context) (*hoststate.DataVolume, error) {
	return h.mounted, nil
}

func (h *degradedHost) ResolveDevice(_ context.Context, diskID string) (BlockDevice, error) {
	device, ok := h.spares[diskID]
	if !ok {
		return BlockDevice{}, errors.New("unknown disk")
	}
	return device, nil
}

func (h *degradedHost) mounts() []string {
	var mounts []string
	for _, line := range h.commands {
		if strings.HasPrefix(line, "systemd-run ") {
			mounts = append(mounts, line[strings.Index(line, " -- ")+4:])
		}
	}
	return mounts
}

func (h *degradedHost) record(t *testing.T) *degradedRecord {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(h.root, "var", "lib", stateDirectoryName, "degraded-volume.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var record degradedRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		t.Fatal(err)
	}
	return &record
}

// The mirror is looked at read-only, unmounted, recorded and mounted
// read-write in one step: a remount would not reach the Host Agent's own
// mount namespace.
func TestMountDegradedLooksFirstThenMountsTheMirrorWithoutItsDisk(t *testing.T) {
	host := newDegradedHost(t, datavolume.LayoutMirror)
	executor := host.executor()
	mounted, err := executor.MountDegraded(context.Background())
	if err != nil || !mounted {
		t.Fatalf("MountDegraded() = %v, %v; want mounted", mounted, err)
	}
	want := []string{
		"mount -t btrfs -o " + probeOptions + " /dev/sda1 " + host.mountPoint,
		"umount " + host.mountPoint,
		"mount -t btrfs -o " + degradedOptions + " /dev/sda1 " + host.mountPoint,
	}
	if got := host.mounts(); !slices.Equal(got, want) {
		t.Fatalf("mounts = %q\nwant %q", got, want)
	}
	record := host.record(t)
	if record == nil || record.FilesystemUUID != degradedUUID || !slices.Equal(record.Survivors, []uint64{1}) || !record.Since.Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("record = %+v, want the volume running on device 1", record)
	}
	if !executor.RestoringRedundancy() {
		t.Fatal("RestoringRedundancy() = false after running without a disk")
	}
	if !slices.Contains(host.commands, "udevadm trigger --action=change /dev/sda1") {
		t.Fatalf("commands = %q, want udev to re-probe the disk", host.commands)
	}
}

func TestMountDegradedLeavesAVolumeOfflineWhenItMustNotRunWithoutADisk(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*degradedHost)
		// looked says whether the volume was mounted read-only to look at it.
		looked bool
	}{
		{name: "span", setup: func(h *degradedHost) { h.marker.Layout = datavolume.LayoutSpan }, looked: true},
		{name: "the stale disk", setup: func(h *degradedHost) {
			h.devices = map[string][2]string{"1": {"1", "0"}, "2": {"0", "1"}}
			record := filepath.Join(h.root, "var", "lib", stateDirectoryName, "degraded-volume.json")
			if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
				h.t.Fatal(err)
			}
			if err := os.WriteFile(record, []byte(`{"filesystemUuid":"`+degradedUUID+`","since":"2026-11-06T00:00:00Z","survivors":[1]}`), 0o600); err != nil {
				h.t.Fatal(err)
			}
		}, looked: true},
		{name: "systemd still waits for the disks", setup: func(h *degradedHost) { h.unitState = "activating" }},
		{name: "an administrator unmounted it", setup: func(h *degradedHost) { h.unitState, h.activeEnter = "inactive", "51234567" }},
		{name: "no disk of the volume", setup: func(h *degradedHost) { h.members = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newDegradedHost(t, datavolume.LayoutMirror)
			test.setup(host)
			before := host.record(t)
			mounted, err := host.executor().MountDegraded(context.Background())
			if err != nil || mounted {
				t.Fatalf("MountDegraded() = %v, %v; want the volume left offline", mounted, err)
			}
			var want []string
			if test.looked {
				want = []string{"mount -t btrfs -o " + probeOptions + " /dev/sda1 " + host.mountPoint, "umount " + host.mountPoint}
			}
			if got := host.mounts(); !slices.Equal(got, want) {
				t.Fatalf("mounts = %q, want %q", got, want)
			}
			if after := host.record(t); (before == nil) != (after == nil) {
				t.Fatalf("record changed from %+v to %+v", before, after)
			}
		})
	}
}

// A record that cannot be read may name the only disks safe to run on, so it
// keeps the volume from running without a disk until it is fixed.
func TestAnUnreadableRecordKeepsTheVolumeFromRunningWithoutADisk(t *testing.T) {
	host := newDegradedHost(t, datavolume.LayoutMirror)
	record := filepath.Join(host.root, "var", "lib", stateDirectoryName, "degraded-volume.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mounted, err := host.executor().MountDegraded(context.Background()); err == nil || mounted {
		t.Fatalf("MountDegraded() = %v, %v; want an error", mounted, err)
	}
	if got := host.mounts(); len(got) != 0 {
		t.Fatalf("mounts = %q, want none", got)
	}
}

// Lines "btrfs replace status -1" printed in the VM of the investigation.
func TestParseReplaceStatusFollowsARebuild(t *testing.T) {
	for _, test := range []struct {
		line     string
		finished bool
		percent  int
		fails    bool
	}{
		{line: "28.2% done, 0 write errs, 0 uncorr. read errs\n", percent: 28},
		{line: "0.0% done, 0 write errs, 0 uncorr. read errs", percent: 0},
		{line: "Started on  6.Nov 13:10:52, finished on  6.Nov 13:11:50, 0 write errs, 0 uncorr. read errs\n", finished: true, percent: 100},
		{line: "Started on  6.Nov 12:57:09, canceled on  6.Nov 12:59:41 at 0.0%, 0 write errs, 0 uncorr. read errs", fails: true},
		{line: "Never started", fails: true},
		{line: "something else", fails: true},
	} {
		finished, percent, err := parseReplaceStatus(test.line)
		if (err != nil) != test.fails || finished != test.finished || (!test.fails && percent != test.percent) {
			t.Errorf("parseReplaceStatus(%q) = %v, %d, %v", test.line, finished, percent, err)
		}
	}
}

// A blkid that fails other than by finding nothing is an error, not an
// absent volume: the volume would otherwise stay offline without a word.
func TestMountDegradedReportsABlkidThatFails(t *testing.T) {
	host := newDegradedHost(t, datavolume.LayoutMirror)
	host.blkidStatus = exitStatus(4)
	if mounted, err := host.executor().MountDegraded(context.Background()); err == nil || mounted {
		t.Fatalf("MountDegraded() = %v, %v; want the blkid failure", mounted, err)
	}
	if got := host.mounts(); len(got) != 0 {
		t.Fatalf("mounts = %q, want none", got)
	}
}

// replaceHost is a mirror mounted without device 2, with a spare disk.
func replaceHost(t *testing.T, operation string, joined bool) (*degradedHost, *volumeOperation) {
	t.Helper()
	host := newDegradedHost(t, datavolume.LayoutMirror)
	survivor, spare := mustResourceID(t, "disk:survivor"), mustResourceID(t, "disk:spare")
	host.mounted = &hoststate.DataVolume{FilesystemUUID: degradedUUID, DiskIDs: []hoststate.ResourceID{survivor}}
	if joined {
		host.mounted.DiskIDs = append(host.mounted.DiskIDs, spare)
	}
	disk := hoststate.Disk{ID: spare, Model: "ST500DM002", Transport: hoststate.TransportSATA, CapacityBytes: 500_000_000_000, Role: hoststate.DiskRoleUnassigned}
	host.spares = map[string]BlockDevice{"disk:spare": {Disk: disk, DevicePath: "/dev/sdc", PartitionPath: "/dev/sdc1"}}
	sysfs := filepath.Join(host.root, "sys", "fs", "btrfs", degradedUUID)
	for devid, flags := range map[string][2]string{"1": {"0", "1"}, "2": {"1", "0"}} {
		dir := filepath.Join(sysfs, "devinfo", devid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "missing"), []byte(flags[0]+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "writeable"), []byte(flags[1]+"\n"), 0o644)
	}
	if err := os.WriteFile(filepath.Join(sysfs, "exclusive_operation"), []byte(operation+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := datavolume.DiskTarget{DiskID: "disk:spare", Fingerprint: datavolume.FingerprintDisk(disk)}
	return host, &volumeOperation{PlanID: "plan:replace", Kind: datavolume.PlanKindReplaceDisk, Disks: []datavolume.DiskTarget{target}, Layout: datavolume.LayoutMirror, FilesystemUUID: degradedUUID}
}

func mustResourceID(t *testing.T, value string) hoststate.ResourceID {
	t.Helper()
	id, err := hoststate.NewResourceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *degradedHost) ran(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, line := range h.commands {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

// The rebuild runs by the missing device's ID; a balance the degraded mount
// kept paused would block it and is cancelled first.
func TestReplaceDiskRebuildsTheMissingDeviceAfterCancellingAPausedBalance(t *testing.T) {
	host, operation := replaceHost(t, "balance paused", false)
	host.replaceStatus = []string{"Started on  6.Nov 13:10:52, finished on  6.Nov 13:11:50, 0 write errs, 0 uncorr. read errs"}
	volume, err := host.executor().replaceDisk(context.Background(), operation)
	if err != nil {
		t.Fatalf("replaceDisk() error = %v", err)
	}
	cancel := slices.Index(host.commands, "btrfs balance cancel "+host.mountPoint)
	start := slices.Index(host.commands, "btrfs replace start 2 /dev/sdc1 "+host.mountPoint)
	if cancel < 0 || start < cancel {
		t.Fatalf("commands = %q, want the paused balance cancelled before the rebuild starts", host.commands)
	}
	if !slices.Equal(volume.DiskIDs, []string{"disk:survivor", "disk:spare"}) {
		t.Fatalf("volume disks = %v, want the survivor and the new disk", volume.DiskIDs)
	}
}

// Retried after a reboot that came after the rebuild finished, the plan
// finds the disk joined and nothing running; btrfs reports such a rebuild as
// never started, so waiting on it would fail the plan for good.
func TestReplaceDiskRetriedAfterTheRebuildFinishedDoesNotWait(t *testing.T) {
	host, operation := replaceHost(t, "none", true)
	if _, err := host.executor().replaceDisk(context.Background(), operation); err != nil {
		t.Fatalf("replaceDisk() error = %v", err)
	}
	if host.ran("btrfs replace") != 0 || host.ran("wipefs") != 0 {
		t.Fatalf("commands = %q, want no erase, rebuild or status", host.commands)
	}
	if host.ran("btrfs filesystem resize") == 0 {
		t.Fatalf("commands = %q, want the volume grown onto the new disk", host.commands)
	}
}

// A rebuild the kernel still runs after a Host Agent restart is followed to
// its end, not started again.
func TestReplaceDiskFollowsARebuildThatRuns(t *testing.T) {
	host, operation := replaceHost(t, "device replace", true)
	host.replaceStatus = []string{"Started on  6.Nov 13:10:52, finished on  6.Nov 13:11:50, 0 write errs, 0 uncorr. read errs"}
	if _, err := host.executor().replaceDisk(context.Background(), operation); err != nil {
		t.Fatalf("replaceDisk() error = %v", err)
	}
	if host.ran("btrfs replace start") != 0 || host.ran("btrfs replace status") != 1 {
		t.Fatalf("commands = %q, want the running rebuild followed", host.commands)
	}
}

// A scrub that found data neither copy repairs has done what it can: the
// record goes. One that stopped keeps the record and waits before the next.
func TestFinishScrubEndsTheRecordOrWaitsToRetry(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		keeps bool
	}{
		{name: "clean", err: nil},
		{name: "uncorrectable errors", err: exitStatus(3)},
		{name: "stopped", err: exitStatus(1), keeps: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newDegradedHost(t, datavolume.LayoutMirror)
			executor := host.executor()
			if err := executor.saveDegraded(&degradedRecord{FilesystemUUID: degradedUUID, Since: time.Unix(1, 0), Survivors: []uint64{1}}); err != nil {
				t.Fatal(err)
			}
			executor.scrubbing = true
			executor.finishScrub(nil, test.err)
			if got := host.record(t) != nil; got != test.keeps || executor.RestoringRedundancy() != test.keeps {
				t.Fatalf("record kept = %v (restoring %v), want %v", got, executor.RestoringRedundancy(), test.keeps)
			}
			if executor.scrubbing {
				t.Fatal("still scrubbing after the scrub ended")
			}
			if test.keeps && !executor.scrubRetryAt.Equal(time.Unix(1_700_000_000, 0).Add(scrubRetry)) {
				t.Fatalf("retry at %v, want %v later", executor.scrubRetryAt, scrubRetry)
			}
		})
	}
}
