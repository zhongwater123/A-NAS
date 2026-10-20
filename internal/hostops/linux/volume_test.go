package linux_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestCreateVolumeFormatsEveryDiskWithTheLayoutsProfiles(t *testing.T) {
	first, second := spareDisk(t, "disk:first"), spareDisk(t, "disk:second")
	resolver := diskResolver{
		first.ID.String():  {Disk: first, DevicePath: "/dev/sda", PartitionPath: "/dev/sda1"},
		second.ID.String(): {Disk: second, DevicePath: "/dev/sdb", PartitionPath: "/dev/sdb1"},
	}
	runner := &recordingRunner{}
	mountPoint := filepath.Join(t.TempDir(), "data")
	executor := linux.NewExecutor(resolver, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: mountPoint, MountUnitName: "a-nas-data.mount"})

	volume, err := executor.CreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:mirror", Layout: storage.LayoutMirror, Disks: []storage.DiskTarget{
			{DiskID: first.ID.String(), Fingerprint: storage.FingerprintDisk(first)},
			{DiskID: second.ID.String(), Fingerprint: storage.FingerprintDisk(second)},
		},
	})
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if !slices.Equal(volume.DiskIDs, []string{"disk:first", "disk:second"}) || volume.Layout != storage.LayoutMirror || volume.CapacityBytes != 500_000_000_000 {
		t.Fatalf("volume = %+v", volume)
	}
	// Each new partition is wiped too: an md superblock inside the old one
	// would otherwise reappear where the new partition starts.
	want := []string{
		"wipefs --all /dev/sda", "parted --script /dev/sda mklabel gpt mkpart primary btrfs 1MiB 100%", "partprobe /dev/sda", "udevadm settle --timeout=10", "wipefs --all /dev/sda1",
		"wipefs --all /dev/sdb", "parted --script /dev/sdb mklabel gpt mkpart primary btrfs 1MiB 100%", "partprobe /dev/sdb", "udevadm settle --timeout=10", "wipefs --all /dev/sdb1",
		"mkfs.btrfs --force --label ANAS_DATA --data raid1 --metadata raid1 /dev/sda1 /dev/sdb1",
	}
	if got := runner.lines(); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("commands =\n%s\nwant to start with\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	marker := readMarker(t, mountPoint)
	if marker["layout"] != "mirror" || marker["filesystemUuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("marker = %v, want the mirror layout recorded", marker)
	}
}

func TestCreateVolumeRefusesADiskThatChangedOrIsInUse(t *testing.T) {
	planned := spareDisk(t, "disk:first")
	changed := planned
	changed.CapacityBytes++
	busy := planned
	busy.InUse = true
	for name, device := range map[string]hoststate.Disk{"changed": changed, "in use": busy} {
		runner := &recordingRunner{}
		resolver := diskResolver{"disk:first": {Disk: device, DevicePath: "/dev/sda", PartitionPath: "/dev/sda1"}}
		executor := linux.NewExecutor(resolver, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: t.TempDir()})
		fingerprint := storage.FingerprintDisk(planned)
		if name == "in use" {
			fingerprint = storage.FingerprintDisk(busy)
		}
		_, err := executor.CreateVolume(context.Background(), storage.CreateVolumeRequest{
			PlanID: "plan:x", Layout: storage.LayoutSingle, Disks: []storage.DiskTarget{{DiskID: "disk:first", Fingerprint: fingerprint}},
		})
		if err == nil || len(runner.commands) != 0 {
			t.Fatalf("%s disk: error = %v, commands = %v; want refused before any command", name, err, runner.lines())
		}
	}
}

func TestAddDisksNeedsTheMountedDataVolume(t *testing.T) {
	disk := spareDisk(t, "disk:second")
	runner := &recordingRunner{}
	resolver := diskResolver{"disk:second": {Disk: disk, DevicePath: "/dev/sdb", PartitionPath: "/dev/sdb1"}}
	executor := linux.NewExecutor(resolver, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: t.TempDir()})
	_, err := executor.AddDisks(context.Background(), storage.AddDisksRequest{
		PlanID: "plan:add", FilesystemUUID: "11111111-2222-3333-4444-555555555555", Layout: storage.LayoutMirror,
		Disks: []storage.DiskTarget{{DiskID: "disk:second", Fingerprint: storage.FingerprintDisk(disk)}},
	})
	if !errors.Is(err, storage.ErrVolumeUnavailable) || len(runner.commands) != 0 {
		t.Fatalf("AddDisks() on an unmounted volume = %v, commands = %v; want ErrVolumeUnavailable before any command", err, runner.lines())
	}
}

type diskResolver map[string]linux.BlockDevice

func (r diskResolver) ResolveDevice(_ context.Context, diskID string) (linux.BlockDevice, error) {
	device, ok := r[diskID]
	if !ok {
		return linux.BlockDevice{}, errors.New("unknown disk")
	}
	return device, nil
}

func spareDisk(t *testing.T, id string) hoststate.Disk {
	t.Helper()
	diskID, err := hoststate.NewResourceID(id)
	if err != nil {
		t.Fatal(err)
	}
	return hoststate.Disk{ID: diskID, Model: "ST500DM002-1BD142", Transport: hoststate.TransportSATA, CapacityBytes: 500_000_000_000, Role: hoststate.DiskRoleUnassigned}
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
