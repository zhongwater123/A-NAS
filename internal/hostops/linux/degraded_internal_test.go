package linux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
)

const degradedUUID = "1bf2da8f-c403-422e-83bb-ea5f87518214"

func TestOnlyAMirrorMissingOneDiskRunsOnTheDisksItLastRanOn(t *testing.T) {
	mirror := volumeMarker{FilesystemUUID: degradedUUID, FormatVersion: 1, Layout: datavolume.LayoutMirror}
	oneGone := []btrfsDevice{{ID: 1}, {ID: 2, Missing: true}}
	cases := []struct {
		name      string
		marker    volumeMarker
		devices   []btrfsDevice
		record    *degradedRecord
		survivors []uint64
	}{
		{name: "mirror without one disk", marker: mirror, devices: oneGone, survivors: []uint64{1}},
		{name: "span", marker: volumeMarker{FilesystemUUID: degradedUUID, Layout: datavolume.LayoutSpan}, devices: oneGone},
		{name: "stripe", marker: volumeMarker{FilesystemUUID: degradedUUID, Layout: datavolume.LayoutStripe}, devices: oneGone},
		{name: "another filesystem's marker", marker: volumeMarker{FilesystemUUID: volumeUUIDOther, Layout: datavolume.LayoutMirror}, devices: oneGone},
		{name: "nothing missing", marker: mirror, devices: []btrfsDevice{{ID: 1}, {ID: 2}}},
		{name: "two of three missing", marker: mirror, devices: []btrfsDevice{{ID: 1}, {ID: 2, Missing: true}, {ID: 3, Missing: true}}},
		{name: "the disk it last ran on", marker: mirror, devices: oneGone,
			record: &degradedRecord{FilesystemUUID: degradedUUID, Since: time.Unix(1, 0), Survivors: []uint64{1}}, survivors: []uint64{1}},
		{name: "only the stale disk", marker: mirror, devices: []btrfsDevice{{ID: 1, Missing: true}, {ID: 2}},
			record: &degradedRecord{FilesystemUUID: degradedUUID, Since: time.Unix(1, 0), Survivors: []uint64{1}}},
		{name: "a record of another volume", marker: mirror, devices: []btrfsDevice{{ID: 1, Missing: true}, {ID: 2}},
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
	members          string
	mu               sync.Mutex
	commands         []string
}

func newDegradedHost(t *testing.T, layout datavolume.Layout) *degradedHost {
	t.Helper()
	host := &degradedHost{
		t: t, root: t.TempDir(), mountPoint: filepath.Join(t.TempDir(), "data"),
		marker:    volumeMarker{FilesystemUUID: degradedUUID, FormatVersion: 1, Layout: layout},
		devices:   map[string][2]string{"1": {"0", "1"}, "2": {"1", "0"}},
		unitState: "failed", members: "/dev/sda1\n",
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
	return NewExecutor(nil, h, Options{SystemRoot: h.root, MountPoint: h.mountPoint, MountUnitName: "a-nas-data.mount", Now: func() time.Time { return time.Unix(1_700_000_000, 0) }})
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
		return []byte(h.unitState + "\n"), nil
	case "blkid":
		if h.members == "" {
			return nil, errors.New("exit status 2")
		}
		return []byte(h.members), nil
	}
	return nil, errors.New("unexpected command " + name)
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
		"mount -t btrfs -o degraded,ro," + mountOptions + " /dev/sda1 " + host.mountPoint,
		"umount " + host.mountPoint,
		"mount -t btrfs -o degraded," + mountOptions + " /dev/sda1 " + host.mountPoint,
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
				want = []string{"mount -t btrfs -o degraded,ro," + mountOptions + " /dev/sda1 " + host.mountPoint, "umount " + host.mountPoint}
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
