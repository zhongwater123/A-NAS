package unixmsg

import (
	"bufio"
	"errors"
	"fmt"
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
// returned file, positioned at its start and opened read-only. The seals
// are what keep the content fixed: a receiver can still open the memfd for
// writing through its own /proc/self/fd, and every write then fails. The
// read-only descriptor is a second layer, as ADR 0017 hands FFmpeg its input
// and as systemd's memfd_seal_and_reopen does.
func WriteSealedFile(name string, write func(io.Writer) error) (*os.File, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	buffered := bufio.NewWriter(file)
	if err := errors.Join(write(buffered), buffered.Flush()); err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL); err != nil {
		return nil, err
	}
	// Opening the memfd again through /proc gives a new open file
	// description of the same sealed content, read-only and at offset 0.
	readOnly, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", file.Fd()), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("reopen sealed %s read-only: %w", name, err)
	}
	return os.NewFile(uintptr(readOnly), name), nil
}
