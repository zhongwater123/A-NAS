// Package capacity holds the data-volume reserve that every writer keeps
// free, so that files and photos run out of space at the same point.
package capacity

import "syscall"

// MinimumReserve stays free even on volumes small enough that 5% is less.
const MinimumReserve = 10 << 30

// Admits reports whether writing incoming more bytes to the volume that holds
// path still leaves 5% of the volume, and at least MinimumReserve, free.
func Admits(path string, incoming int64) (bool, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return false, err
	}
	return admits(uint64(stats.Blocks)*uint64(stats.Bsize), uint64(stats.Bavail)*uint64(stats.Bsize), uint64(max(incoming, 0))), nil
}

func admits(total, available, incoming uint64) bool {
	reserve := max(total/20, MinimumReserve)
	return incoming < available && available-incoming >= reserve
}
