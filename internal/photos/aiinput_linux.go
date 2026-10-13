package photos

import (
	"os"

	"golang.org/x/sys/unix"
)

// memoryFile returns content as a sealed in-memory file read from its start:
// it cannot be written, grown or shrunk, so the Worker sees exactly what was
// rendered, and it disappears with its last descriptor.
func memoryFile(content []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("anas-ai-input", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "anas-ai-input")
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS,
		unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
