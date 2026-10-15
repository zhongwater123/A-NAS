//go:build linux

package sharesapi

import (
	"fmt"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"golang.org/x/sys/unix"
	"io"
	"os"
)

// The decoder receives a sealed read-only anonymous descriptor, never a
// pathname into managed content or a writable staging file on the system disk.
func anonymousInput(reader io.Reader) (*os.File, error) {
	fd, err := unix.MemfdCreate("a-nas-share-preview", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "share-preview")
	defer file.Close()
	n, err := io.Copy(file, reader)
	if err != nil {
		return nil, err
	}
	if n > maxPreviewSource {
		return nil, shares.ErrInvalid
	}
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		return nil, err
	}
	return os.Open(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
}
