package modules

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrStoreUnavailable reports that the module's store is not the one the
// Host Agent keeps for it on a writable data volume.
var ErrStoreUnavailable = errors.New("module store is unavailable")

// RequireOwnedStore checks the store a module unit binds in: a directory of
// this process's account, 0700, on a writable Btrfs volume. When the data
// volume was not mounted as the unit started, the bind is skipped and the
// check fails; the module then exits so that systemd restarts it with the
// mount (ADR 0037, consequences).
func RequireOwnedStore(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s is not this account's 0700 directory", ErrStoreUnavailable, root)
	}
	var fs unix.Statfs_t
	if err = unix.Statfs(root, &fs); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	if uint64(fs.Type) != unix.BTRFS_SUPER_MAGIC || fs.Flags&unix.ST_RDONLY != 0 {
		return fmt.Errorf("%w: %s is not on the writable data volume", ErrStoreUnavailable, root)
	}
	return nil
}
