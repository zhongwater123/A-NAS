package linux

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func TestUnidentifiedDiskDoesNotBlockHostObservation(t *testing.T) {
	reader := newFixtureReader(t, `{"blockdevices":[
		{"name":"sda","type":"disk","size":1000,"model":"System","wwn":"system","mountpoints":["/"]},
		{"name":"sdb","type":"disk","size":0,"model":"Card Reader","tran":"usb","rm":true}
	]}`)
	state, err := reader.Read(t.Context())
	if err != nil {
		t.Fatalf("an empty card reader blocked host observation: %v", err)
	}
	if len(state.Disks) != 2 {
		t.Fatalf("got %d disks, want system disk and unavailable card reader", len(state.Disks))
	}
}

func TestUnusableDevicesRemainVisibleWithoutBlockingHealthyDisks(t *testing.T) {
	for _, test := range []struct {
		name   string
		device blockDevice
		issues []hoststate.DiskObservationIssue
	}{
		{"empty reader", blockDevice{Name: "sdb", Type: "disk", Model: "Reader", Transport: "usb", Removable: true}, []hoststate.DiskObservationIssue{hoststate.DiskMissingCapacity, hoststate.DiskMissingIdentity}},
		{"zram swap", blockDevice{Name: "zram0", Type: "disk", Size: 4096, Filesystem: "swap", Mountpoints: []*string{new("[SWAP]")}}, []hoststate.DiskObservationIssue{hoststate.DiskMissingModel, hoststate.DiskMissingIdentity}},
		{"eMMC boot", blockDevice{Name: "mmcblk0boot0", Type: "disk", Size: 4096, ReadOnly: true}, []hoststate.DiskObservationIssue{hoststate.DiskMissingModel, hoststate.DiskReadOnly, hoststate.DiskMissingIdentity}},
		{"SATA missing model", blockDevice{Name: "sdb", Type: "disk", Size: 4096, Transport: "sata", WWN: "other"}, []hoststate.DiskObservationIssue{hoststate.DiskMissingModel}},
		{"SATA missing identity", blockDevice{Name: "sdb", Type: "disk", Size: 4096, Model: "Other", Transport: "sata"}, []hoststate.DiskObservationIssue{hoststate.DiskMissingIdentity}},
		{"read-only SATA", blockDevice{Name: "sdb", Type: "disk", Size: 4096, Model: "Other", Transport: "sata", WWN: "other", ReadOnly: true}, []hoststate.DiskObservationIssue{hoststate.DiskReadOnly}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			devices := []blockDevice{
				{Name: "nvme0n1", Type: "disk", Size: 1000, Model: "System", WWN: "system", Mountpoints: []*string{new("/")}},
				{Name: "sda", Type: "disk", Size: 4096, Model: "Healthy", Transport: "sata", WWN: "healthy"},
				test.device,
			}
			contents, err := json.Marshal(blockDeviceDocument{BlockDevices: devices})
			if err != nil {
				t.Fatal(err)
			}
			reader := newFixtureReader(t, string(contents))
			state, err := reader.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Disks) != 3 {
				t.Fatalf("disks = %+v", state.Disks)
			}
			for _, disk := range state.Disks {
				if disk.Model == "Healthy" {
					resolved, err := reader.ResolveDevice(t.Context(), disk.ID.String())
					if err != nil || resolved.DevicePath != "/dev/sda" {
						t.Fatalf("healthy disk = %+v, %v", resolved, err)
					}
					continue
				}
				if disk.Role == hoststate.DiskRoleSystem {
					continue
				}
				if !slices.Equal(disk.ObservationIssues, test.issues) {
					t.Fatalf("issues = %v, want %v", disk.ObservationIssues, test.issues)
				}
				if disk.CapacityBytes != test.device.Size || disk.Model != test.device.Model {
					t.Fatalf("observation lost device facts: %+v", disk)
				}
				if _, err := reader.ResolveDevice(t.Context(), disk.ID.String()); err == nil {
					t.Fatal("unusable disk resolved to an operational path")
				}
				if strings.Contains(disk.ID.String(), test.device.Name) {
					t.Fatal("ID leaks kernel name")
				}
			}
			_, targets, err := parseDisks(contents, nil, nil)
			if err != nil || len(targets) != 2 {
				t.Fatalf("SMART targets = %v, %v; want only healthy observations", targets, err)
			}
		})
	}
}

func TestDuplicateDiskIdentitiesCannotResolveIncludingAnEarlierValidID(t *testing.T) {
	for _, useWWN := range []bool{true, false} {
		devices := []blockDevice{
			{Name: "sda", Type: "disk", Model: "Bridge", Size: 4096, Serial: "duplicate", Mountpoints: []*string{new("/")}},
			{Name: "sdb", Type: "disk", Model: "Bridge", Size: 4096, Serial: "duplicate"},
		}
		if useWWN {
			devices[0].WWN, devices[1].WWN = "duplicate", "duplicate"
		}
		identity, err := stableDiskIdentity(devices[0])
		if err != nil {
			t.Fatal(err)
		}
		oldID, err := derivedResourceID("disk", diskIdentityDomain, identity)
		if err != nil {
			t.Fatal(err)
		}
		contents, err := json.Marshal(blockDeviceDocument{BlockDevices: devices})
		if err != nil {
			t.Fatal(err)
		}
		reader := newFixtureReader(t, string(contents))
		state, err := reader.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Disks) != 2 || state.Disks[0].ID == state.Disks[1].ID {
			t.Fatalf("duplicate rows: %+v", state.Disks)
		}
		for _, disk := range state.Disks {
			if !slices.Contains(disk.ObservationIssues, hoststate.DiskDuplicateIdentity) {
				t.Fatalf("duplicate disk not blocked: %+v", disk)
			}
			if !strings.HasPrefix(disk.ID.String(), "observation:") {
				t.Fatalf("ambiguous identity presented as stable: %s", disk.ID)
			}
			if _, err := reader.ResolveDevice(t.Context(), disk.ID.String()); err == nil {
				t.Fatal("duplicate disk resolved")
			}
		}
		if _, err := reader.ResolveDevice(t.Context(), oldID.String()); err == nil {
			t.Fatal("stale stable ID resolved to first duplicate")
		}
		_, targets, err := parseDisks(contents, nil, nil)
		if err != nil || len(targets) != 0 {
			t.Fatalf("ambiguous SMART targets = %v, %v", targets, err)
		}
	}
}

func TestSystemDiskWithoutHardwareIdentityStillReportsItsRole(t *testing.T) {
	state, err := newFixtureReader(t, `{"blockdevices":[{"name":"mmcblk0","type":"disk","size":4096,"mountpoints":["/"]}]}`).Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Disks) != 1 || state.Disks[0].Role != hoststate.DiskRoleSystem || !state.Disks[0].InUse || len(state.Disks[0].ObservationIssues) == 0 {
		t.Fatalf("system disk observation = %+v", state.Disks)
	}
}
