package unixmsg

import (
	"bufio"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// SealedFile returns an anonymous read-only payload positioned at its start.
// The caller owns the file and must close it.
func SealedFile(name string, data []byte) (*os.File, error) {
	return WriteSealedFile(name, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteSealedFile streams a payload into a sealed file. The caller owns the
// returned file, positioned at its start.
func WriteSealedFile(name string, write func(io.Writer) error) (*os.File, error) {
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
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
