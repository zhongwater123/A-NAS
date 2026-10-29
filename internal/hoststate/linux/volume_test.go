package linux

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// Btrfs remounts read-only after an error and counts errors per device in
// sysfs; both reach the data disk for the system health assessment.
func TestReaderReportsADataVolumeReadOnlyAndItsBtrfsErrors(t *testing.T) {
	const filesystem = "sys/fs/btrfs/09e275fe-794a-458d-8200-b6e67c55cc22"
	const other = "sys/fs/btrfs/aaaaaaaa-0000-4000-8000-000000000000"
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release":                      &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id":                      &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":                         &fstest.MapFile{Data: []byte("1.0 2.0\n")},
			"sys/fs/btrfs/features/raid1c34":      &fstest.MapFile{Data: []byte("0\n")},
			other + "/devices/sdz1":               &fstest.MapFile{},
			other + "/devinfo/1/error_stats":      &fstest.MapFile{Data: []byte("write_errs 9\nread_errs 9\nflush_errs 0\ncorruption_errs 0\ngeneration_errs 0\n")},
			filesystem + "/devices/sda1":          &fstest.MapFile{},
			filesystem + "/devinfo/1/error_stats": &fstest.MapFile{Data: []byte("write_errs 0\nread_errs 2\nflush_errs 0\ncorruption_errs 1\ngeneration_errs 0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[
              {"name":"nvme0n1","type":"disk","size":125000000000,"model":"System","tran":"nvme","wwn":"system",
               "children":[{"name":"nvme0n1p2","type":"part","mountpoints":["/"]}]},
              {"name":"sda","type":"disk","size":500107862016,"model":"ST500DM002-1BD142","tran":"sata","rota":true,"wwn":"data",
               "children":[{"name":"sda1","type":"part","fstype":"btrfs","mountpoints":["/var/lib/docker/volumes/x/_data","/srv/a-nas/data"]}]}
            ]}`), nil
		},
		readFilesystemUsage: func(string) (filesystemUsage, error) {
			return filesystemUsage{total: 500_000_000_000, available: 300_000_000_000, readOnly: true}, nil
		},
	})

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	for _, disk := range state.Disks {
		if disk.Role != hoststate.DiskRoleData {
			if disk.FilesystemErrors != nil || disk.FilesystemReadOnly {
				t.Fatalf("system disk = %+v, want no data volume facts", disk)
			}
			continue
		}
		if !disk.FilesystemReadOnly || disk.FilesystemErrors == nil || *disk.FilesystemErrors != 3 {
			t.Fatalf("data disk = %+v, want read-only with 3 errors", disk)
		}
	}
}

func TestBtrfsErrorsAreUnknownWithoutTheirCounters(t *testing.T) {
	root := fstest.MapFS{
		"sys/fs/btrfs/09e275fe-794a-458d-8200-b6e67c55cc22/devices/sda1": &fstest.MapFile{},
	}
	if got := readBtrfsErrors(root, []string{"sda1"}); got != nil {
		t.Fatalf("errors without error_stats = %d, want unknown", *got)
	}
	if got := readBtrfsErrors(root, []string{"sdb1"}); got != nil {
		t.Fatalf("errors of another device = %d, want unknown", *got)
	}
}
