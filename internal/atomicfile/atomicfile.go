// Package atomicfile replaces records so that a crash or power cut at any
// point leaves either the previous file or the whole new one, never an empty
// or partial file (#272). renameio writes and syncs a temporary file beside
// the target and renames it into place; Write then syncs the directory too,
// without which the rename itself may be lost on a power cut.
package atomicfile

import (
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

// Write replaces path with data, whose mode is perm whatever the umask or an
// existing file's mode. It returns once the new file is on disk under its
// name.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// Without WithTempDir, renameio probes os.TempDir on every call and may
	// write the temporary file there.
	pending, err := renameio.NewPendingFile(path, renameio.WithTempDir(dir), renameio.WithStaticPermissions(perm))
	if err != nil {
		return err
	}
	defer func() { _ = pending.Cleanup() }()
	if _, err := pending.Write(data); err != nil {
		return err
	}
	if err := pending.CloseAtomicallyReplace(); err != nil {
		return err
	}
	return SyncDir(dir)
}

// SyncDir writes a directory's entries to disk, so that a file renamed into
// or removed from it stays that way after a power cut.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
