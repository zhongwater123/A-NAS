package datavolume_test

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const gb = uint64(1_000_000_000)

// A mirror's block group needs room on two devices. When a disk joins two
// full ones, Btrfs can use almost none of it until block groups move off the
// old disks; space the sizes themselves leave over is never counted.
func TestUnusableIsTheMirrorSpaceBtrfsCannotReachYet(t *testing.T) {
	for _, test := range []struct {
		name     string
		layout   datavolume.Layout
		devices  []hoststate.VolumeDevice
		unusable uint64
	}{
		{
			name:     "a third disk beside two full ones",
			layout:   datavolume.LayoutMirror,
			devices:  []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 3, SizeBytes: 500 * gb}},
			unusable: 230 * gb,
		},
		{
			name:    "a third disk beside two with room",
			layout:  datavolume.LayoutMirror,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 100 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 100 * gb}, {DevID: 3, SizeBytes: 500 * gb}},
		},
		{
			name:    "two disks joining two full ones",
			layout:  datavolume.LayoutMirror,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 3, SizeBytes: 500 * gb}, {DevID: 4, SizeBytes: 500 * gb}},
		},
		{
			// The large disk can only ever pair with the small ones: what the
			// sizes leave over is not stranded by the allocation.
			name:     "a large disk beside two small ones",
			layout:   datavolume.LayoutMirror,
			devices:  []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 100 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 100 * gb}, {DevID: 3, SizeBytes: 4000 * gb}},
			unusable: 100 * gb,
		},
		{
			name:    "spread evenly",
			layout:  datavolume.LayoutMirror,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb}, {DevID: 3, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb}},
		},
		{
			name:     "disks filled unevenly",
			layout:   datavolume.LayoutMirror,
			devices:  []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 470 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 490 * gb}, {DevID: 5, SizeBytes: 500 * gb}},
			unusable: 230 * gb,
		},
		{
			name:    "span uses every disk alone",
			layout:  datavolume.LayoutSpan,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 3, SizeBytes: 500 * gb}},
		},
		{
			name:    "a stripe needs only one device with room",
			layout:  datavolume.LayoutStripe,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 3, SizeBytes: 500 * gb}},
		},
		{
			// 1 GB of 1.5 TB is within a block group or two.
			name:    "too little to move",
			layout:  datavolume.LayoutMirror,
			devices: []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 499 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 499 * gb}, {DevID: 3, SizeBytes: 500 * gb, AllocatedBytes: 497 * gb}},
		},
		{
			name:     "a small volume moves less",
			layout:   datavolume.LayoutMirror,
			devices:  []hoststate.VolumeDevice{{DevID: 1, SizeBytes: 1 * gb, AllocatedBytes: gb * 9 / 10}, {DevID: 2, SizeBytes: 1 * gb, AllocatedBytes: gb * 9 / 10}, {DevID: 3, SizeBytes: 1 * gb}},
			unusable: gb * 4 / 10,
		},
		{name: "devices unknown", layout: datavolume.LayoutMirror},
	} {
		t.Run(test.name, func(t *testing.T) {
			if unusable := datavolume.Unusable(test.layout, test.devices); unusable != test.unusable {
				t.Fatalf("Unusable() = %d, want %d", unusable, test.unusable)
			}
		})
	}
}
