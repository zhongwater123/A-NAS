package linux

import (
	"context"
	"reflect"
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
}

func TestNoDataVolumeWithoutABtrfsMountedThere(t *testing.T) {
	if volume := readDataVolume(fstest.MapFS{}, "", []hoststate.Disk{{Role: hoststate.DiskRoleData}}); volume != nil {
		t.Fatalf("data volume without a UUID = %+v, want nil", volume)
	}
	if volume := readDataVolume(fstest.MapFS{}, "09e275fe-794a-458d-8200-b6e67c55cc22", nil); volume != nil {
		t.Fatalf("data volume without data disks = %+v, want nil", volume)
	}
}
