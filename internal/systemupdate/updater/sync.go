package updater

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// SyncResult says what Sync copied.
type SyncResult struct {
	Files int
	Bytes int64
}

// Sync copies the repository a development machine pushed into staging, the
// staging account's directory, into local, the directory apt reads as the
// file: source (docs/specs/system-update.md#开发机推送了新版本).
//
// staging belongs to an account that is not trusted, so Sync reads it
// through os.Root, which no link can lead out of; it takes only regular
// files under dists and pool, opens none that could block, and executes
// nothing. A file local has at the same size and modification time is not
// copied again. Packages go first and the index last, so apt never reads an
// index that names a file not there yet; apt then checks every file against
// the archive key's signature.
func Sync(staging, local string) (SyncResult, error) {
	var result SyncResult
	root, err := os.OpenRoot(staging)
	if err != nil {
		return result, fmt.Errorf("open the pushed repository: %w", err)
	}
	defer func() { _ = root.Close() }()
	var files []string
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		top, _, _ := strings.Cut(name, "/")
		if top != "dists" && top != "pool" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("read the pushed repository: %w", err)
	}
	if !slices.ContainsFunc(files, func(name string) bool { return strings.HasPrefix(name, "dists/") }) {
		return result, errors.New("the pushed repository has no dists directory")
	}
	slices.SortStableFunc(files, func(a, b string) int { return syncOrder(a) - syncOrder(b) })
	for _, name := range files {
		copied, size, err := syncFile(root, name, filepath.Join(local, filepath.FromSlash(name)))
		if err != nil {
			return result, err
		}
		if copied {
			result.Files++
			result.Bytes += size
		}
	}
	return result, nil
}

// syncOrder puts packages first, then the indexes, then the signed Release
// files that name the indexes.
func syncOrder(name string) int {
	switch {
	case strings.HasPrefix(name, "pool/"):
		return 0
	case path.Base(name) == "InRelease" || path.Base(name) == "Release" || path.Base(name) == "Release.gpg":
		return 2
	default:
		return 1
	}
}

// syncFile copies one file unless target has it already.
func syncFile(root *os.Root, name, target string) (bool, int64, error) {
	// O_NONBLOCK: a pipe put in place of the file since the walk saw it must
	// not hold the open; it is refused below as not a regular file.
	source, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, 0, fmt.Errorf("open %s in the pushed repository: %w", name, err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return false, 0, err
	}
	if !info.Mode().IsRegular() {
		return false, 0, fmt.Errorf("%s in the pushed repository is not a regular file", name)
	}
	if have, err := os.Lstat(target); err == nil && have.Mode().IsRegular() &&
		have.Size() == info.Size() && have.ModTime().Equal(info.ModTime()) {
		return false, 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	size, err := io.Copy(temporary, source)
	if err == nil && size != info.Size() {
		err = fmt.Errorf("%s changed while it was copied", name)
	}
	if err == nil {
		err = temporary.Chmod(0o644)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chtimes(temporary.Name(), time.Time{}, info.ModTime())
	}
	if err == nil {
		err = os.Rename(temporary.Name(), target)
	}
	if err != nil {
		return false, 0, fmt.Errorf("copy %s from the pushed repository: %w", name, err)
	}
	return true, size, nil
}
