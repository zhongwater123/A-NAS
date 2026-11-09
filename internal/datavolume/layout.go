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

// maxUnusableIgnored caps the space a mirror may leave unused: Btrfs makes a
// data block group a tenth of the filesystem and at most 1 GiB, and no
// balance gets closer than about one of them.
const maxUnusableIgnored uint64 = 2 << 30

// Unusable reports how much less the volume can still store than its
// devices allow, because Btrfs has not spread its block groups over them.
// Only a mirror strands space: each of its block groups needs room on
// two devices, so once a disk joins two full ones, most of the new disk
// stays unused until block groups move off the old ones. Less than a tenth
// of the devices' size, and never more than 2 GiB, is within a block group
// and counts as none. It is 0 for other layouts, without the devices, and
// while profiles are being converted, which the caller rules out.
func Unusable(layout Layout, devices []hoststate.VolumeDevice) uint64 {
	if layout != LayoutMirror || len(devices) < 2 {
		return 0
	}
	sizes := make([]uint64, 0, len(devices))
	var total, allocated, free, largestFree uint64
	for _, device := range devices {
		left := device.SizeBytes - min(device.AllocatedBytes, device.SizeBytes)
		sizes = append(sizes, device.SizeBytes)
		total += device.SizeBytes
		allocated += device.AllocatedBytes
		free += left
		largestFree = max(largestFree, left)
	}
	// Every block group of a mirror is allocated twice.
	possible, stored := UsableBytes(LayoutMirror, sizes), allocated/2
	reachable := min(free/2, free-largestFree)
	if possible <= stored || possible-stored <= reachable {
		return 0
	}
	if unusable := possible - stored - reachable; unusable >= min(maxUnusableIgnored, total/10) {
		return unusable
	}
	return 0
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
