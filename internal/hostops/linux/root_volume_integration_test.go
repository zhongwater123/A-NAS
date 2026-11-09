//go:build rootintegration

package linux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

// A disk joins a single-disk volume the way adding disks does after
// partitioning; the conversion check then turns the volume into a mirror
// with real btrfs-progs, reading its progress from sysfs (ADR 0025).
func TestRootConversionTurnsAJoinedDiskIntoAMirror(t *testing.T) {
	ctx := context.Background()
	mount := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	first, second := loopDevice(t, "first.img", "1G"), loopDevice(t, "second.img", "1G")
	run(t, "mkfs.btrfs", "--quiet", "--force", "--label", "ANAS_DATA", "--data", "single", "--metadata", "dup", first)
	run(t, "mount", first, mount)
	t.Cleanup(func() { _ = exec.Command("umount", mount).Run() })
	uuid := strings.ToLower(run(t, "blkid", "--probe", "--output", "value", "--match-tag", "UUID", first))
	run(t, "dd", "if=/dev/urandom", "of="+filepath.Join(mount, "files.bin"), "bs=1M", "count=96", "status=none")
	run(t, "btrfs", "filesystem", "sync", mount)

	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	marker := volumeMarker{FilesystemUUID: uuid, FormatVersion: 1}
	if err := executor.writeMarker(marker); err != nil {
		t.Fatal(err)
	}
	if started, err := executor.ReconcileConversion(ctx); started || err != nil {
		t.Fatalf("conversion check on a single disk = %v, %v; want nothing to do", started, err)
	}

	// Adding the disk records the mirror as pending first. The run is then
	// killed right after Btrfs added the disk, before it could record the
	// layout (issue #165): the check takes the layout once the kernel lists
	// both devices.
	marker.Pending = &pendingLayout{PlanID: "plan:add", Layout: datavolume.LayoutMirror, Devices: 2}
	if err := executor.writeMarker(marker); err != nil {
		t.Fatal(err)
	}
	if started, err := executor.ReconcileConversion(ctx); started || err != nil {
		t.Fatalf("conversion check before the disk joined = %v, %v; want nothing to do", started, err)
	}
	run(t, "btrfs", "device", "add", "-f", second, mount)
	started, err := executor.ReconcileConversion(ctx)
	if err != nil || !started {
		t.Fatalf("conversion check after adding a disk = %v, %v; want a balance started", started, err)
	}
	if recorded, err := executor.readMarker(); err != nil || recorded.Layout != datavolume.LayoutMirror || recorded.Pending != nil {
		t.Fatalf("marker after the disk joined = %+v, %v; want the mirror recorded", recorded, err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		data, metadata, operation := linuxhoststate.ReadBtrfsAllocation(os.DirFS("/"), uuid)
		convertData, convertMetadata := datavolume.OutsideLayout(data, metadata, datavolume.LayoutMirror)
		executor.conversionMu.Lock()
		busy := executor.converting
		executor.conversionMu.Unlock()
		if !convertData && !convertMetadata && operation == hoststate.VolumeOperationNone && !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still converting after 3 minutes: data %+v, metadata %+v, operation %q\n%s", data, metadata, operation, run(t, "btrfs", "filesystem", "df", mount))
		}
		time.Sleep(500 * time.Millisecond)
	}
	usage := run(t, "btrfs", "filesystem", "df", mount)
	for _, want := range []string{"Data, RAID1", "System, RAID1", "Metadata, RAID1"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("btrfs filesystem df =\n%s\nwant %q", usage, want)
		}
	}
	if started, err := executor.ReconcileConversion(ctx); started || err != nil {
		t.Fatalf("conversion check on a mirror = %v, %v; want nothing left to do", started, err)
	}
}

func loopDevice(t *testing.T, name, size string) string {
	t.Helper()
	image := filepath.Join(t.TempDir(), name)
	run(t, "truncate", "--size="+size, image)
	device := run(t, "losetup", "--find", "--show", image)
	t.Cleanup(func() { _ = exec.Command("losetup", "--detach", device).Run() })
	return device
}

// A disk that joins a mirror larger than the free space of the old disks
// stays partly unused: every block group needs room on two disks, so the
// new disk can only hold copies of block groups moved off the old ones. The
// Host Agent's check spreads the data with real btrfs-progs, measuring the space
// it cannot use from `btrfs filesystem show`.
func TestRootAMirrorSpreadsOntoAJoinedDisk(t *testing.T) {
	ctx := context.Background()
	mount := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	first, second, third := loopDevice(t, "first.img", "1G"), loopDevice(t, "second.img", "1G"), loopDevice(t, "third.img", "2G")
	run(t, "mkfs.btrfs", "--quiet", "--force", "--label", "ANAS_DATA", "--data", "raid1", "--metadata", "raid1", first, second)
	run(t, "btrfs", "device", "scan")
	run(t, "mount", first, mount)
	t.Cleanup(func() { _ = exec.Command("umount", mount).Run() })
	uuid := strings.ToLower(run(t, "blkid", "--probe", "--output", "value", "--match-tag", "UUID", first))
	// The mirror of two 1 GiB disks holds a third of a GiB; the 2 GiB disk can
	// hold a copy of all of it only once block groups move off the old disks.
	// They keep room for a block group's new copy, as the write reserve keeps
	// it on the NAS: on these small disks a block group is a fifth of one.
	for i := range 3 {
		run(t, "dd", "if=/dev/urandom", "of="+filepath.Join(mount, fmt.Sprintf("files-%d.bin", i)), "bs=1M", "count=120", "status=none")
	}
	run(t, "btrfs", "filesystem", "sync", mount)
	run(t, "btrfs", "device", "add", "-f", third, mount)

	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	if err := executor.writeMarker(volumeMarker{FilesystemUUID: uuid, FormatVersion: 1, Layout: datavolume.LayoutMirror}); err != nil {
		t.Fatal(err)
	}
	before, _, err := executor.measureSpread(ctx, uuid)
	if err != nil || before == 0 {
		t.Fatalf("unusable space after the disk joined = %d, %v; want some\n%s", before, err, run(t, "btrfs", "filesystem", "show", "--raw", mount))
	}
	started, err := executor.ReconcileConversion(ctx)
	if err != nil || !started {
		t.Fatalf("conversion check on a mirror that cannot use its new disk = %v, %v; want the data spread", started, err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		executor.conversionMu.Lock()
		busy := executor.converting
		executor.conversionMu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still spreading after 3 minutes\n%s", run(t, "btrfs", "filesystem", "show", "--raw", mount))
		}
		time.Sleep(500 * time.Millisecond)
	}
	shown := run(t, "btrfs", "filesystem", "show", "--raw", mount)
	after, _, err := executor.measureSpread(ctx, uuid)
	if err != nil || after != 0 {
		t.Fatalf("unusable space = %d of %d before, %v; want all of it usable\n%s", after, before, err, shown)
	}
	devices := linuxhoststate.VolumeDevices(os.DirFS("/"), uuid, []byte(shown))
	if len(devices) != 3 || devices[2].AllocatedBytes == 0 {
		t.Fatalf("devices = %+v, want block groups on the joined disk\n%s", devices, shown)
	}
	usage := run(t, "btrfs", "filesystem", "df", mount)
	for _, kind := range []string{"Data", "System", "Metadata"} {
		if !strings.Contains(usage, kind+", RAID1:") || strings.Contains(usage, kind+", single:") {
			t.Fatalf("btrfs filesystem df =\n%s\nwant every block group still in the mirror", usage)
		}
	}
}
