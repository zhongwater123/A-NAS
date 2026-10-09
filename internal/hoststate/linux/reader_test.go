package linux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func TestReaderReturnsOneDebianObservation(t *testing.T) {
	observedAt := time.Date(2026, time.October, 6, 4, 30, 0, 0, time.UTC)
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("12345.67 98765.43\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return observedAt },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{
  "blockdevices": [
    {
      "name": "nvme0n1",
      "type": "disk",
      "size": 125000000000,
      "model": "A-NAS System SSD",
      "tran": "nvme",
      "rota": false,
      "mountpoints": [],
      "wwn": "0x5002538e12345678",
      "serial": "PRIVATE-SYSTEM-SERIAL",
      "children": [
        {"name": "nvme0n1p2", "type": "part", "mountpoints": ["/"]}
      ]
    },
    {
      "name": "sda",
      "type": "disk",
      "size": 512000000000,
      "model": "A-NAS Data HDD",
      "tran": "usb",
      "rota": true,
      "mountpoints": [],
      "wwn": "eui.002538b123456789",
      "serial": "PRIVATE-DATA-SERIAL"
    }
  ]
}`), nil
		},
	})

	got, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	want := hoststate.State{
		ObservedAt: observedAt,
		System: hoststate.System{
			ID:              mustResourceID(t, "host:b962654b4ef5f44d8db45a4e20f3733e"),
			Hostname:        "a-nas-dev",
			OperatingSystem: hoststate.OperatingSystem{Name: "Debian", Version: "13"},
			Architecture:    "amd64",
			UptimeSeconds:   12345,
			Health:          hoststate.HealthUnknown,
		},
		Disks: []hoststate.Disk{
			{
				ID:            mustResourceID(t, "disk:4ad977453dbc26894eeb4bfd1db0d5f2"),
				Model:         "A-NAS Data HDD",
				Transport:     hoststate.TransportUSB,
				CapacityBytes: 512000000000,
				Rotational:    true,
				Role:          hoststate.DiskRoleUnassigned,
				Health:        hoststate.HealthUnknown,
				SMARTStatus:   hoststate.HealthUnknown,
			},
			{
				ID:            mustResourceID(t, "disk:8bb18d37a490d7b4811b53077c58d2c5"),
				Model:         "A-NAS System SSD",
				Transport:     hoststate.TransportNVMe,
				CapacityBytes: 125000000000,
				Rotational:    false,
				InUse:         true,
				Role:          hoststate.DiskRoleSystem,
				Health:        hoststate.HealthUnknown,
				SMARTStatus:   hoststate.HealthUnknown,
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestObserveDataVolumeUsageReportsFilesystemCapacity(t *testing.T) {
	disks := []hoststate.Disk{{Role: hoststate.DiskRoleData}}
	observeDataVolumeUsage(disks, func(path string) (uint64, uint64, error) {
		if path != "/srv/a-nas/data" {
			t.Fatalf("usage path = %q", path)
		}
		return 500_107_862_016, 482_000_000_000, nil
	})

	if disks[0].FilesystemCapacityBytes == nil || *disks[0].FilesystemCapacityBytes != 500_107_862_016 {
		t.Fatalf("filesystem capacity = %v", disks[0].FilesystemCapacityBytes)
	}
	if disks[0].FilesystemAvailableBytes == nil || *disks[0].FilesystemAvailableBytes != 482_000_000_000 {
		t.Fatalf("filesystem available = %v", disks[0].FilesystemAvailableBytes)
	}
}

func TestReaderRejectsObservationWithoutSystemDisk(t *testing.T) {
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{
  "blockdevices": [
    {
      "name": "sda",
      "type": "disk",
      "size": 512000000000,
      "model": "A-NAS Data HDD",
      "tran": "sata",
      "rota": true,
      "mountpoints": [],
      "wwn": "0x5002538e12345678"
    }
  ]
}`), nil
		},
	})

	if _, err := reader.Read(context.Background()); err == nil {
		t.Fatal("Read() succeeded without a system disk, want error")
	}
}

func TestReaderKeepsDiskIdentityWhenKernelNameChanges(t *testing.T) {
	first := newFixtureReader(t, `{
  "blockdevices": [{
    "name": "sda",
    "type": "disk",
    "size": 125000000000,
    "model": "A-NAS System SSD",
    "tran": "sata",
    "rota": false,
    "mountpoints": [],
    "wwn": "0x5002538e12345678",
    "children": [{"name": "sda2", "type": "part", "mountpoints": ["/"]}]
  }]
}`)
	second := newFixtureReader(t, `{
  "blockdevices": [{
    "name": "nvme9n7",
    "type": "disk",
    "size": 125000000000,
    "model": "A-NAS System SSD",
    "tran": "nvme",
    "rota": false,
    "mountpoints": [],
    "wwn": "0x5002538e12345678",
    "children": [{"name": "nvme9n7p4", "type": "part", "mountpoints": ["/"]}]
  }]
}`)

	firstState, err := first.Read(context.Background())
	if err != nil {
		t.Fatalf("first Read() error = %v", err)
	}
	secondState, err := second.Read(context.Background())
	if err != nil {
		t.Fatalf("second Read() error = %v", err)
	}

	firstID := firstState.Disks[0].ID.String()
	secondID := secondState.Disks[0].ID.String()
	if firstID != secondID {
		t.Fatalf("disk IDs changed with kernel name: %q != %q", firstID, secondID)
	}
	for _, privateValue := range []string{"sda", "nvme9n7", "5002538e12345678"} {
		if strings.Contains(firstID, privateValue) || strings.Contains(secondID, privateValue) {
			t.Fatalf("disk ID leaked private or unstable value %q", privateValue)
		}
	}
}

func TestReaderRejectsInvalidBlockDeviceObservations(t *testing.T) {
	tests := map[string]string{
		"malformed JSON": `{`,
		"missing stable identity": `{
  "blockdevices": [{
    "name": "sda", "type": "disk", "size": 1, "model": "Disk",
    "mountpoints": ["/"]
  }]
}`,
		"duplicate stable identity": `{
  "blockdevices": [
    {
      "name": "sda", "type": "disk", "size": 1, "model": "Disk A",
      "wwn": "duplicate", "mountpoints": ["/"]
    },
    {
      "name": "sdb", "type": "disk", "size": 2, "model": "Disk B",
      "wwn": "duplicate", "mountpoints": []
    }
  ]
}`,
	}

	for name, observation := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := newFixtureReader(t, observation).Read(context.Background()); err == nil {
				t.Fatal("Read() succeeded, want error")
			}
		})
	}
}

func TestReaderReportsRemovableUseAndFilesystemEvidence(t *testing.T) {
	reader := newFixtureReader(t, `{
  "blockdevices": [
    {
      "name": "nvme0n1", "type": "disk", "size": 125000000000,
      "model": "System", "tran": "nvme", "rota": false, "rm": false,
      "mountpoints": [], "wwn": "system",
      "children": [{"name": "nvme0n1p2", "type": "part", "fstype": "ext4", "mountpoints": ["/"]}]
    },
    {
      "name": "sda", "type": "disk", "size": 64000000000,
      "model": "Installer", "tran": "usb", "rota": false, "rm": true,
      "mountpoints": [], "wwn": "installer",
      "children": [{"name": "sda1", "type": "part", "fstype": "vfat", "mountpoints": ["/media/installer"]}]
    }
  ]
}`)

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	var installer hoststate.Disk
	for _, disk := range state.Disks {
		if disk.Model == "Installer" {
			installer = disk
		}
	}
	if !installer.Removable {
		t.Fatal("installer Removable = false, want true")
	}
	if !installer.InUse {
		t.Fatal("installer InUse = false, want true")
	}
	if got, want := installer.Filesystems, []string{"vfat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("installer Filesystems = %#v, want %#v", got, want)
	}
}

func TestDeviceInUseRejectsStorageStackMembersEvenWhenUnmounted(t *testing.T) {
	for _, filesystem := range []string{"swap", "linux_raid_member", "LVM2_member", "crypto_LUKS"} {
		if !deviceInUse(blockDevice{Type: "part", Filesystem: filesystem}) {
			t.Fatalf("deviceInUse(%q) = false, want true", filesystem)
		}
	}
}

func TestReaderAddsSMARTHealthAndTemperatureWithoutExposingDeviceName(t *testing.T) {
	reader := newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(`{"blockdevices":[{
              "name":"nvme0n1","type":"disk","size":125000000000,
              "model":"System","tran":"nvme","wwn":"system",
              "children":[{"name":"nvme0n1p2","type":"part","mountpoints":["/"]}]
            }]}`), nil
		},
		readSMART: func(_ context.Context, deviceName string) ([]byte, error) {
			if got, want := deviceName, "nvme0n1"; got != want {
				t.Fatalf("SMART device = %q, want %q", got, want)
			}
			return []byte(`{"smart_status":{"passed":true},"temperature":{"current":34}}`), nil
		},
	})

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	disk := state.Disks[0]
	if got, want := disk.SMARTStatus, hoststate.HealthHealthy; got != want {
		t.Fatalf("SMARTStatus = %q, want %q", got, want)
	}
	if disk.TemperatureCelsius == nil || *disk.TemperatureCelsius != 34 {
		t.Fatalf("TemperatureCelsius = %v, want 34", disk.TemperatureCelsius)
	}
	if strings.Contains(disk.ID.String(), "nvme0n1") {
		t.Fatalf("public disk ID leaked device name: %q", disk.ID.String())
	}
}

func TestReaderHonorsCanceledContextBeforeReading(t *testing.T) {
	called := false
	reader := newReader(dependencies{
		root:         fstest.MapFS{},
		hostname:     func() (string, error) { return "", nil },
		architecture: "amd64",
		now:          time.Now,
		readBlockDevices: func(context.Context) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := reader.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("Read() invoked block-device reader after context cancellation")
	}
}

func newFixtureReader(t *testing.T, blockDevices string) *Reader {
	t.Helper()
	return newReader(dependencies{
		root: fstest.MapFS{
			"etc/os-release": &fstest.MapFile{Data: []byte("ID=debian\nNAME=Debian\nVERSION_ID=13\n")},
			"etc/machine-id": &fstest.MapFile{Data: []byte("0123456789abcdef0123456789abcdef\n")},
			"proc/uptime":    &fstest.MapFile{Data: []byte("1.0 2.0\n")},
		},
		hostname:     func() (string, error) { return "a-nas-dev", nil },
		architecture: "amd64",
		now:          func() time.Time { return time.Unix(0, 0) },
		readBlockDevices: func(context.Context) ([]byte, error) {
			return []byte(blockDevices), nil
		},
		readSMART: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("SMART unavailable in fixture")
		},
	})
}

func mustResourceID(t *testing.T, value string) hoststate.ResourceID {
	t.Helper()
	id, err := hoststate.NewResourceID(value)
	if err != nil {
		t.Fatalf("NewResourceID(%q) error = %v", value, err)
	}
	return id
}
