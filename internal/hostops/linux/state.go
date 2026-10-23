package linux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The Host Agent keeps its records on the system disk in a directory only
// root can open, the Host Agent's own StateDirectory (issue #176). Releases
// before it kept them in /var/lib/a-nas, the Product Service's
// StateDirectory; NewExecutor moves them from there once.
const (
	stateDirectoryName       = "a-nas-host"
	legacyStateDirectoryName = "a-nas"
	// maxStateFileBytes bounds what a record may grow to; the largest, the
	// space registry, holds one line per account.
	maxStateFileBytes = 16 << 20
)

// prepareState makes the state directory and moves each file in paths from
// the legacy directory into it. It returns the reason a file cannot be used,
// by path; that file's registry then refuses the operations that need it.
func (e *Executor) prepareState(directory, legacy string, paths []string) map[string]error {
	failures := make(map[string]error)
	if err := ensureStateDirectory(directory, e.stateOwner); err != nil {
		err = fmt.Errorf("Host Agent state directory: %w", err)
		for _, path := range paths {
			failures[path] = err
		}
		return failures
	}
	moved := false
	for _, path := range paths {
		changed, err := e.moveStateFile(filepath.Join(legacy, filepath.Base(path)), path)
		if err != nil {
			failures[path] = err
		}
		moved = moved || changed
	}
	if moved {
		e.logger.Info("moved Host Agent records to their own directory", "from", legacy, "to", directory)
	}
	return failures
}

// ensureStateDirectory creates directory for root alone, or checks that the
// one there is a directory of the Host Agent's.
func ensureStateDirectory(directory string, owner int) error {
	if err := os.MkdirAll(filepath.Dir(directory), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Lstat(directory, &stat); err != nil {
		return &os.PathError{Op: "lstat", Path: directory, Err: err}
	}
	switch {
	case stat.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%s is not a directory", directory)
	case int(stat.Uid) != owner:
		return fmt.Errorf("%s is owned by UID %d, not %d", directory, stat.Uid, owner)
	case stat.Mode&0o077 != 0:
		return os.Chmod(directory, 0o700)
	}
	return nil
}

// moveStateFile moves the record at legacy to path unless path exists, and
// reports whether it did. The copy is durable before the original goes, so
// a crash in between leaves both, which the next start resolves.
func (e *Executor) moveStateFile(legacy, path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		e.dropLegacyCopy(legacy, path)
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	contents, err := readStateFile(legacy, e.stateOwner)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("move %s: %w", filepath.Base(legacy), err)
	}
	if err := writeAtomic(path, contents, 0o600); err != nil {
		return false, fmt.Errorf("move %s: %w", filepath.Base(legacy), err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return false, fmt.Errorf("move %s: %w", filepath.Base(legacy), err)
	}
	if err := os.Remove(legacy); err != nil {
		return true, fmt.Errorf("remove %s after moving it: %w", legacy, err)
	}
	if err := syncDirectory(filepath.Dir(legacy)); err != nil {
		e.logger.Warn("flush the removal of a moved Host Agent record", "file", legacy, "error", err)
	}
	return true, nil
}

// dropLegacyCopy removes the original that a move cut short left behind. A
// file there that differs was written by an older release after the move;
// it is never read, so it is reported and left for the administrator.
func (e *Executor) dropLegacyCopy(legacy, path string) {
	original, err := readStateFile(legacy, e.stateOwner)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		if current, currentErr := readStateFile(path, e.stateOwner); currentErr == nil && bytes.Equal(original, current) {
			if err := os.Remove(legacy); err != nil {
				e.logger.Warn("remove a moved Host Agent record", "file", legacy, "error", err)
			}
			return
		}
	}
	e.logger.Warn("a Host Agent record in the old directory differs from the one in use and is ignored",
		"file", legacy, "in_use", path, "error", err)
}

// readState reads one of the Host Agent's records. A missing record reads as
// os.ErrNotExist.
func (e *Executor) readState(path string) ([]byte, error) {
	if err := e.stateFailures[path]; err != nil {
		return nil, err
	}
	return readStateFile(path, e.stateOwner)
}

// readStateFile reads path if it is a regular file with one link, owned by
// owner. The checks apply to the opened file, so nothing can be swapped in
// between; O_NONBLOCK keeps a FIFO from blocking the open.
func readStateFile(path string, owner int) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil, fmt.Errorf("%s is a symbolic link", path)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	switch {
	case stat.Mode&unix.S_IFMT != unix.S_IFREG:
		return nil, fmt.Errorf("%s is not a regular file", path)
	case int(stat.Uid) != owner:
		return nil, fmt.Errorf("%s is owned by UID %d, not %d", path, stat.Uid, owner)
	case stat.Nlink != 1:
		return nil, fmt.Errorf("%s has %d links", path, stat.Nlink)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxStateFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxStateFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxStateFileBytes)
	}
	return contents, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
