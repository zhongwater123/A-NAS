package linux

import (
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
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
		layout    storage.Layout
		data      usage
		metadata  usage
		operation hoststate.VolumeOperation
		want      []string
	}{
		{"single disk joined by a mirror", storage.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft", "-mconvert=raid1,soft"}},
		{"cancelled halfway", storage.LayoutMirror, usage{single(4), raid1(5)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft"}},
		{"span keeps data single", storage.LayoutSpan, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-mconvert=raid1,soft"}},
		{"stripe", storage.LayoutStripe, usage{single(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid0,soft"}},
		{"empty leftovers", storage.LayoutMirror, usage{single(0), raid1(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone, nil},
		{"already converting", storage.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationBalance, nil},
		{"another operation", storage.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationOther, nil},
		{"paused at unmount", storage.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationBalancePaused, []string{"balance", "resume"}},
	} {
		if got := conversionCommand(test.layout, test.data, test.metadata, test.operation); !slices.Equal(got, test.want) {
			t.Errorf("%s: command = %v, want %v", test.name, got, test.want)
		}
	}
}
