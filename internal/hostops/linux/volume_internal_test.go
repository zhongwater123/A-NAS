package linux

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func TestConversionCommandConvertsOnlyWhatIsOutsideTheLayout(t *testing.T) {
	type usage = []hoststate.ProfileUsage
	single := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileSingle, UsedBytes: used}
	}
	dup := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileDup, UsedBytes: used}
	}
	raid1 := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileRAID1, UsedBytes: used}
	}
	for _, test := range []struct {
		name      string
		layout    datavolume.Layout
		data      usage
		metadata  usage
		operation hoststate.VolumeOperation
		want      []string
	}{
		{"single disk joined by a mirror", datavolume.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft", "-mconvert=raid1,soft"}},
		{"cancelled halfway", datavolume.LayoutMirror, usage{single(4), raid1(5)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft"}},
		{"span keeps data single", datavolume.LayoutSpan, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-mconvert=raid1,soft"}},
		{"stripe", datavolume.LayoutStripe, usage{single(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid0,soft"}},
		{"empty leftovers", datavolume.LayoutMirror, usage{single(0), raid1(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone, nil},
		{"already converting", datavolume.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationBalance, nil},
		{"another operation", datavolume.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationOther, nil},
		{"paused at unmount", datavolume.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationBalancePaused, []string{"balance", "resume"}},
	} {
		if got := conversionCommand(test.layout, test.data, test.metadata, test.operation); !slices.Equal(got, test.want) {
			t.Errorf("%s: command = %v, want %v", test.name, got, test.want)
		}
	}
}

// Disks whose addition was cut short after Btrfs added them still get the
// layout their plan asked for: it takes effect once the kernel lists them as
// devices of the volume (issue #165).
func TestAPendingLayoutTakesEffectOnceTheVolumeHasTheDisks(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	systemRoot := t.TempDir()
	executor := NewExecutor(nil, nil, Options{SystemRoot: systemRoot, MountPoint: t.TempDir()})
	if err := executor.writeMarker(volumeMarker{
		FilesystemUUID: uuid, FormatVersion: 1, Pending: &pendingLayout{PlanID: "plan:add", Layout: datavolume.LayoutMirror, Devices: 2},
	}); err != nil {
		t.Fatal(err)
	}
	devices := filepath.Join(systemRoot, "sys", "fs", "btrfs", uuid, "devices")
	if err := os.MkdirAll(devices, 0o755); err != nil {
		t.Fatal(err)
	}
	join := func(name string) {
		if err := os.WriteFile(filepath.Join(devices, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	join("sda1")
	if marker, err := executor.promotePendingLayout(); err != nil || marker.Layout != "" || marker.Pending == nil {
		t.Fatalf("marker with one of two devices = %+v, %v; want the layout still pending", marker, err)
	}
	join("sdb1")
	if marker, err := executor.promotePendingLayout(); err != nil || marker.Layout != datavolume.LayoutMirror || marker.Pending != nil {
		t.Fatalf("marker with both devices = %+v, %v; want the mirror", marker, err)
	}
	if marker, err := executor.readMarker(); err != nil || marker.Layout != datavolume.LayoutMirror || marker.Pending != nil {
		t.Fatalf("stored marker = %+v, %v; want the mirror recorded", marker, err)
	}
}
