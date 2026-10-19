package hoststate_test

import (
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func mustDiskID(t *testing.T, value string) hoststate.ResourceID {
	t.Helper()
	id, err := hoststate.NewResourceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pointer[T any](value T) *T { return &value }

type found struct {
	code     hoststate.FindingCode
	severity hoststate.Health
	disk     string
	value    int64
}

func summarize(findings []hoststate.Finding) []found {
	var out []found
	for _, finding := range findings {
		item := found{code: finding.Code, severity: finding.Severity, disk: finding.DiskID.String()}
		if finding.Value != nil {
			item.value = *finding.Value
		}
		out = append(out, item)
	}
	return out
}

func TestAssessTakesTheWorstObservedFact(t *testing.T) {
	system := func() hoststate.Disk {
		return hoststate.Disk{ID: mustDiskID(t, "disk:system"), Model: "NVMe", Role: hoststate.DiskRoleSystem, Health: hoststate.HealthHealthy, TemperatureCelsius: pointer(41)}
	}
	data := func() hoststate.Disk {
		return hoststate.Disk{
			ID: mustDiskID(t, "disk:data"), Model: "ST500DM002", Role: hoststate.DiskRoleData, Rotational: true, Health: hoststate.HealthHealthy,
			TemperatureCelsius: pointer(38), FilesystemCapacityBytes: pointer(uint64(500 << 30)), FilesystemAvailableBytes: pointer(uint64(300 << 30)),
			FilesystemErrors: pointer(uint64(0)),
		}
	}
	spare := func() hoststate.Disk {
		return hoststate.Disk{ID: mustDiskID(t, "disk:spare"), Model: "USB", Role: hoststate.DiskRoleUnassigned, Health: hoststate.HealthUnknown}
	}
	for _, test := range []struct {
		name     string
		disks    func() []hoststate.Disk
		expected hoststate.Expectations
		health   hoststate.Health
		findings []found
	}{
		{"everything observed and fine", func() []hoststate.Disk { return []hoststate.Disk{system(), data(), spare()} }, hoststate.Expectations{DataVolume: true}, hoststate.HealthHealthy, nil},
		{"nothing observed", func() []hoststate.Disk { return nil }, hoststate.Expectations{}, hoststate.HealthUnknown, nil},
		{"no data volume yet", func() []hoststate.Disk { return []hoststate.Disk{system(), spare()} }, hoststate.Expectations{}, hoststate.HealthHealthy, nil},
		{"data volume not mounted", func() []hoststate.Disk { return []hoststate.Disk{system(), spare()} }, hoststate.Expectations{DataVolume: true}, hoststate.HealthCritical,
			[]found{{hoststate.FindingVolumeOffline, hoststate.HealthCritical, "", 0}}},
		{"SMART unreadable on a disk in service, not on a spare", func() []hoststate.Disk {
			disk := data()
			disk.Health = hoststate.HealthUnknown
			return []hoststate.Disk{system(), disk, spare()}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthWarning, []found{{hoststate.FindingDiskHealthUnknown, hoststate.HealthWarning, "disk:data", 0}}},
		{"a failing spare still counts", func() []hoststate.Disk {
			disk := spare()
			disk.Health = hoststate.HealthCritical
			return []hoststate.Disk{system(), data(), disk}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthCritical, []found{{hoststate.FindingDiskFailing, hoststate.HealthCritical, "disk:spare", 0}}},
		{"hot by medium", func() []hoststate.Disk {
			hard, nvme := data(), system()
			hard.TemperatureCelsius = pointer(55)
			nvme.TemperatureCelsius = pointer(69)
			return []hoststate.Disk{nvme, hard}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthWarning, []found{{hoststate.FindingDiskHot, hoststate.HealthWarning, "disk:data", 55}}},
		{"nearly full", func() []hoststate.Disk {
			disk := data()
			disk.FilesystemAvailableBytes = pointer(uint64(45 << 30))
			return []hoststate.Disk{system(), disk}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthWarning, []found{{hoststate.FindingVolumeNearlyFull, hoststate.HealthWarning, "disk:data", 91}}},
		{"inside the write reserve", func() []hoststate.Disk {
			disk := data()
			disk.FilesystemAvailableBytes = pointer(uint64(24 << 30))
			return []hoststate.Disk{system(), disk}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthCritical, []found{{hoststate.FindingVolumeFull, hoststate.HealthCritical, "disk:data", 95}}},
		{"read-only with errors, worst first", func() []hoststate.Disk {
			disk := data()
			disk.FilesystemErrors = pointer(uint64(3))
			disk.FilesystemReadOnly = true
			return []hoststate.Disk{system(), disk}
		}, hoststate.Expectations{DataVolume: true}, hoststate.HealthCritical, []found{
			{hoststate.FindingVolumeReadOnly, hoststate.HealthCritical, "disk:data", 0},
			{hoststate.FindingVolumeErrors, hoststate.HealthWarning, "disk:data", 3},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			health, findings := hoststate.Assess(hoststate.State{Disks: test.disks()}, test.expected)
			if health != test.health || !reflect.DeepEqual(summarize(findings), test.findings) {
				t.Fatalf("Assess() = %q, %+v; want %q, %+v", health, summarize(findings), test.health, test.findings)
			}
		})
	}
}
