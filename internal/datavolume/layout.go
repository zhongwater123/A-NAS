package datavolume

import "github.com/zhongwater123/A-NAS/internal/hoststate"

// Layout is how the data volume keeps its files on its disks (ADR 0025).
type Layout string

const (
	// LayoutSingle is a volume on one disk.
	LayoutSingle Layout = "single"
	// LayoutMirror keeps two copies of everything on different disks.
	LayoutMirror Layout = "mirror"
	// LayoutSpan adds the disks' capacities and keeps one copy of the data.
	LayoutSpan Layout = "span"
	// LayoutStripe adds the capacities and stripes the data across disks.
	LayoutStripe Layout = "stripe"
)

// Fits reports whether a volume of n disks can have the layout.
func (l Layout) Fits(disks int) bool {
	switch l {
	case LayoutSingle:
		return disks == 1
	case LayoutMirror, LayoutSpan, LayoutStripe:
		return disks >= 2
	default:
		return false
	}
}

// Profiles are the Btrfs profiles the layout keeps data and metadata in. With
// two disks or more, metadata always has two copies, so a lost disk leaves
// the filesystem structure whole even without a mirror.
func (l Layout) Profiles() (data, metadata hoststate.Profile) {
	switch l {
	case LayoutMirror:
		return hoststate.ProfileRAID1, hoststate.ProfileRAID1
	case LayoutSpan:
		return hoststate.ProfileSingle, hoststate.ProfileRAID1
	case LayoutStripe:
		return hoststate.ProfileRAID0, hoststate.ProfileRAID1
	default:
		return hoststate.ProfileSingle, hoststate.ProfileDup
	}
}

// UsableBytes estimates the space for files the layout offers on disks of the
// sizes, before filesystem overhead. A mirror holds half the total and never
// more than the other disks can copy of the largest.
func UsableBytes(layout Layout, sizes []uint64) uint64 {
	var total, largest uint64
	for _, size := range sizes {
		total += size
		largest = max(largest, size)
	}
	if layout == LayoutMirror {
		return min(total/2, total-largest)
	}
	return total
}

// OutsideLayout reports whether data or metadata bytes are in profiles other
// than the layout's, so that a balance must convert them.
func OutsideLayout(data, metadata []hoststate.ProfileUsage, layout Layout) (convertData, convertMetadata bool) {
	dataProfile, metadataProfile := layout.Profiles()
	outside := func(usage []hoststate.ProfileUsage, target hoststate.Profile) bool {
		for _, item := range usage {
			if item.Profile != target && item.UsedBytes > 0 {
				return true
			}
		}
		return false
	}
	return outside(data, dataProfile), outside(metadata, metadataProfile)
}
