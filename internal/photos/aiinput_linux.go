package photos

import (
	"bufio"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// memoryFile returns content as a sealed in-memory file read from its start:
// it cannot be written, grown or shrunk, so the Worker sees exactly what was
// rendered, and it disappears with its last descriptor.
func memoryFile(content []byte) (*os.File, error) {
	return sealedFile("anas-ai-input", func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

// sealedFile returns what write writes as a sealed in-memory file read from
// its start, as memoryFile does, for content too large to hold twice.
func sealedFile(name string, write func(io.Writer) error) (*os.File, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	buffered := bufio.NewWriter(file)
	if err := errors.Join(write(buffered), buffered.Flush()); err != nil {
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
