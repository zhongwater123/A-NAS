package linux

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// The mount table names one device of a Btrfs filesystem on several disks;
// every disk holding it is a data disk, found by the filesystem UUID.
func TestReaderFindsEveryDiskOfTheDataVolumeAndItsConversion(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	const sysfs = "sys/fs/btrfs/" + uuid
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
			sysfs + "/allocation/data/single/used_bytes":    &fstest.MapFile{Data: []byte("70\n")},
			sysfs + "/allocation/data/raid1/used_bytes":     &fstest.MapFile{Data: []byte("30\n")},
			sysfs + "/allocation/data/bytes_used":           &fstest.MapFile{Data: []byte("100\n")},
			sysfs + "/allocation/metadata/raid1/used_bytes": &fstest.MapFile{Data: []byte("2\n")},
			sysfs + "/allocation/metadata/dup/used_bytes":   &fstest.MapFile{Data: []byte("0\n")},
			sysfs + "/allocation/system/raid1/used_bytes":   &fstest.MapFile{Data: []byte("1\n")},
			sysfs + "/exclusive_operation":                  &fstest.MapFile{Data: []byte("balance\n")},
			sysfs + "/devinfo/1/missing":                    &fstest.MapFile{Data: []byte("0\n")},
			sysfs + "/devinfo/2/missing":                    &fstest.MapFile{Data: []byte("0\n")},
		},
		readBtrfsDevices: func(_ context.Context, path string) ([]byte, error) {
			if path != "/srv/a-nas/data" {
				t.Errorf("btrfs filesystem show %q, want the data mount point", path)
			}
			return []byte(btrfsShow(uuid, "\tdevid    1 size 500106788864 used 52613349376 path /dev/sdb1\n", "\tdevid    2 size 500106788864 used 50465865728 path /dev/sda1\n")), nil
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[
              {"name":"nvme0n1","type":"disk","size":125000000000,"model":"System","tran":"nvme","wwn":"system",
               "children":[{"name":"nvme0n1p2","type":"part","fstype":"ext4","mountpoints":["/"]}]},
              {"name":"sda","type":"disk","size":500107862016,"model":"ST500DM002-1BD142","tran":"sata","rota":true,"wwn":"new",
               "children":[{"name":"sda1","type":"part","fstype":"btrfs","uuid":"` + uuid + `","label":"ANAS_DATA","mountpoints":[null]}]},
              {"name":"sdb","type":"disk","size":500107862016,"model":"ST500DM002-1BD142","tran":"sata","rota":true,"wwn":"old",
               "children":[{"name":"sdb1","type":"part","fstype":"btrfs","uuid":"` + uuid + `","label":"ANAS_DATA","mountpoints":["/srv/a-nas/data"]}]},
              {"name":"sdc","type":"disk","size":500107862016,"model":"ST500DM002-1BD142","tran":"sata","rota":true,"wwn":"spare",
               "children":[{"name":"sdc1","type":"part","fstype":"linux_raid_member","uuid":"2a5a4bd2-c45f-586f-20c3-f2657f46b405","label":"OrangePi3B:0","mountpoints":[null]}]}
            ]}`), nil
		},
	})

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	roles := map[string]hoststate.DiskRole{}
	var spare hoststate.Disk
	for _, disk := range state.Disks {
		roles[disk.Model+"/"+disk.ID.String()[len(disk.ID.String())-4:]] = disk.Role
		if disk.Role == hoststate.DiskRoleUnassigned {
			spare = disk
		}
	}
	data := 0
	for _, role := range roles {
		if role == hoststate.DiskRoleData {
			data++
		}
	}
	if data != 2 {
		t.Fatalf("roles = %v, want both disks of the filesystem as data disks", roles)
	}
	if spare.InUse || !reflect.DeepEqual(spare.Signatures, []hoststate.Signature{{Type: "linux_raid_member", Label: "OrangePi3B:0", UUID: "2a5a4bd2-c45f-586f-20c3-f2657f46b405"}}) {
		t.Fatalf("spare disk = %+v, want an unused disk with the md signature it carries", spare)
	}
	volume := state.DataVolume
	if volume == nil || volume.FilesystemUUID != uuid || len(volume.DiskIDs) != 2 || volume.Operation != hoststate.VolumeOperationBalance {
		t.Fatalf("data volume = %+v", volume)
	}
	wantData := []hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 30}, {Profile: hoststate.ProfileSingle, UsedBytes: 70}}
	wantMetadata := []hoststate.ProfileUsage{{Profile: hoststate.ProfileDup, UsedBytes: 0}, {Profile: hoststate.ProfileRAID1, UsedBytes: 2}}
	if !reflect.DeepEqual(volume.Data, wantData) || !reflect.DeepEqual(volume.Metadata, wantMetadata) {
		t.Fatalf("profiles = %+v / %+v, want %+v / %+v", volume.Data, volume.Metadata, wantData, wantMetadata)
	}
	wantDevices := []hoststate.VolumeDevice{
		{DevID: 1, SizeBytes: 500106788864, AllocatedBytes: 52613349376},
		{DevID: 2, SizeBytes: 500106788864, AllocatedBytes: 50465865728},
	}
	if !reflect.DeepEqual(volume.Devices, wantDevices) {
		t.Fatalf("devices = %+v, want %+v", volume.Devices, wantDevices)
	}
}

// btrfsShow is what `btrfs filesystem show --raw` prints for a mounted
// filesystem with the device lines.
func btrfsShow(uuid string, devices ...string) string {
	out := "Label: 'ANAS_DATA'  uuid: " + uuid + "\n\tTotal devices " + strconv.Itoa(len(devices)) + " FS bytes used 46245638144\n"
	for _, device := range devices {
		out += device
	}
	return out + "\n"
}

// Only a complete list of the filesystem's devices says how Btrfs allocates
// them; a missing device, or one sysfs knows and the tool does not list,
// leaves it unknown.
func TestVolumeDevicesNeedEveryDeviceOfTheFilesystem(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	const other = "aaaaaaaa-0000-4000-8000-000000000000"
	first := "\tdevid    1 size 1000 used 600 path /dev/sdb1\n"
	second := "\tdevid    2 size 1000 used 400 path /dev/sda1\n"
	two := fstest.MapFS{
		"sys/fs/btrfs/" + uuid + "/devinfo/1/missing": &fstest.MapFile{Data: []byte("0\n")},
		"sys/fs/btrfs/" + uuid + "/devinfo/2/missing": &fstest.MapFile{Data: []byte("0\n")},
	}
	want := []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 1000, AllocatedBytes: 600}, {DevID: 2, SizeBytes: 1000, AllocatedBytes: 400}}
	for _, test := range []struct {
		name   string
		root   fstest.MapFS
		output string
		want   []hoststate.VolumeDevice
	}{
		{name: "every device", root: two, output: btrfsShow(uuid, first, second), want: want},
		{name: "another filesystem first", root: two, output: btrfsShow(other, first) + btrfsShow(uuid, first, second), want: want},
		{name: "only another filesystem", root: two, output: btrfsShow(other, first, second)},
		{name: "a device missing", root: two, output: btrfsShow(uuid, first, "\tdevid    2 size 0 used 0 path <missing disk> MISSING\n")},
		{name: "some devices missing", root: two, output: strings.Replace(btrfsShow(uuid, first, second), "\n\n", "\n\t*** Some devices missing\n\n", 1)},
		{name: "fewer devices than the total", root: two, output: strings.Replace(btrfsShow(uuid, first, second), second, "", 1)},
		{name: "a device sysfs knows is not listed", root: fstest.MapFS{
			"sys/fs/btrfs/" + uuid + "/devinfo/1/missing": &fstest.MapFile{Data: []byte("0\n")},
			"sys/fs/btrfs/" + uuid + "/devinfo/2/missing": &fstest.MapFile{Data: []byte("0\n")},
			"sys/fs/btrfs/" + uuid + "/devinfo/3/missing": &fstest.MapFile{Data: []byte("1\n")},
		}, output: btrfsShow(uuid, first, second)},
		{name: "sysfs counts the device missing", root: fstest.MapFS{
			"sys/fs/btrfs/" + uuid + "/devinfo/1/missing": &fstest.MapFile{Data: []byte("0\n")},
			"sys/fs/btrfs/" + uuid + "/devinfo/2/missing": &fstest.MapFile{Data: []byte("1\n")},
		}, output: btrfsShow(uuid, first, second)},
		{name: "more allocated than the size", root: two, output: btrfsShow(uuid, first, "\tdevid    2 size 1000 used 1001 path /dev/sda1\n")},
		{name: "the same devid twice", root: two, output: btrfsShow(uuid, first, first)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := volumeDevices(test.root, uuid, []byte(test.output)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("volumeDevices() = %+v, want %+v", got, test.want)
			}
		})
	}
}

// After btrfs device add the udev database, and so lsblk, shows the new
// partition wiped until the next boot. The kernel lists it at once; going by
// lsblk alone offered a disk of the mounted volume to be erased again.
func TestADiskJustAddedToTheVolumeIsADataDiskBeforeUdevKnows(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	const other = "aaaaaaaa-0000-4000-8000-000000000000"
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release":                         &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id":                         &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":                            &fstest.MapFile{Data: []byte("1.0 2.0\n")},
			"sys/fs/btrfs/" + uuid + "/devices/sda1": &fstest.MapFile{},
			"sys/fs/btrfs/" + uuid + "/devices/sdb1": &fstest.MapFile{},
			"sys/fs/btrfs/" + other + "/devices/sdc": &fstest.MapFile{},
			"sys/fs/btrfs/" + other + "/devices/sdd": &fstest.MapFile{},
			"sys/fs/btrfs/features/raid1c34":         &fstest.MapFile{Data: []byte("0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[
              {"name":"nvme0n1","type":"disk","size":125000000000,"model":"System","tran":"nvme","wwn":"system",
               "children":[{"name":"nvme0n1p2","type":"part","fstype":"ext4","mountpoints":["/"]}]},
              {"name":"sda","type":"disk","size":500107862016,"model":"Added","tran":"sata","wwn":"added",
               "children":[{"name":"sda1","type":"part","mountpoints":[null]}]},
              {"name":"sdb","type":"disk","size":500107862016,"model":"Original","tran":"sata","wwn":"original",
               "children":[{"name":"sdb1","type":"part","fstype":"btrfs","uuid":"` + uuid + `","mountpoints":["/srv/a-nas/data"]}]},
              {"name":"sdc","type":"disk","size":500107862016,"model":"Other","tran":"sata","wwn":"other","fstype":"btrfs","mountpoints":["/mnt/other"]},
              {"name":"sdd","type":"disk","size":500107862016,"model":"OtherSecond","tran":"sata","wwn":"other-second","mountpoints":[null]}
            ]}`), nil
		},
	})

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	byModel := map[string]hoststate.Disk{}
	for _, disk := range state.Disks {
		byModel[disk.Model] = disk
	}
	if added := byModel["Added"]; added.Role != hoststate.DiskRoleData || !added.InUse {
		t.Fatalf("added disk = %+v, want a data disk in use", added)
	}
	if second := byModel["OtherSecond"]; second.Role != hoststate.DiskRoleUnassigned || !second.InUse {
		t.Fatalf("second disk of another mounted Btrfs = %+v, want unassigned but in use", second)
	}
	if volume := state.DataVolume; volume == nil || volume.FilesystemUUID != uuid || len(volume.DiskIDs) != 2 {
		t.Fatalf("data volume = %+v, want both disks", volume)
	}
}

func TestNoDataVolumeWithoutABtrfsMountedThere(t *testing.T) {
	if volume := readDataVolume(fstest.MapFS{}, "", []hoststate.Disk{{Role: hoststate.DiskRoleData}}); volume != nil {
		t.Fatalf("data volume without a UUID = %+v, want nil", volume)
	}
	if volume := readDataVolume(fstest.MapFS{}, "09e275fe-794a-458d-8200-b6e67c55cc22", nil); volume != nil {
		t.Fatalf("data volume without data disks = %+v, want nil", volume)
	}
}
