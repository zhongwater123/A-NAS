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

// SyncResult says what Sync copied and removed.
type SyncResult struct {
	Files   int
	Bytes   int64
	Removed int
}

// Sync makes local, the directory apt reads as the file: source, a copy of
// the repository a development machine pushed into staging, the staging
// account's directory (docs/specs/system-update.md#开发机推送了新版本).
//
// staging belongs to an account that is not trusted, so Sync reads it
// through os.Root, which no link can lead out of; it takes only regular
// files under dists and pool, opens none that could block, and executes
// nothing. A file local has at the same size and modification time is not
// copied again. room, when set, is asked first whether the system disk has
// the bytes the copy needs, so that a push of new models cannot fill it.
// Packages go first and the index after them, so apt never reads an index
// that names a file not there yet; apt then checks every file against the
// archive key's signature. Last, the files the push no longer has go, so
// that releases do not pile up on the system disk: the index the push
// brought names none of them.
//
// A push that has room only once those files are gone, as one that
// replaces a model, removes them before it copies. Until the copy ends, the
// index local had may then name a file that is gone; apt refuses to install
// from it before it changes anything, and the next sync finishes the copy.
func Sync(staging, local string, room func(need int64) error) (SyncResult, error) {
	var result SyncResult
	root, err := os.OpenRoot(staging)
	if err != nil {
		return result, fmt.Errorf("open the pushed repository: %w", err)
	}
	defer func() { _ = root.Close() }()
	var files []string
	var need int64
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
		if !entry.Type().IsRegular() {
			return nil
		}
		files = append(files, name)
		if info, err := entry.Info(); err == nil && !have(filepath.Join(local, filepath.FromSlash(name)), info) {
			need += info.Size()
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("read the pushed repository: %w", err)
	}
	if !slices.ContainsFunc(files, func(name string) bool { return strings.HasPrefix(name, "dists/") }) {
		return result, errors.New("the pushed repository has no dists directory")
	}
	gone, freed, err := stale(local, files)
	if err != nil {
		return result, err
	}
	pruned := false
	if room != nil && need > 0 && room(need) != nil {
		// Room only once the files the push no longer has are gone, as
		// for a push that replaces a model: they go first.
		if err := room(need - freed); err != nil {
			return result, err
		}
		if result.Removed, err = prune(local, gone); err != nil {
			return result, err
		}
		pruned = true
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
	if !pruned {
		result.Removed, err = prune(local, gone)
	}
	return result, err
}

// have reports whether target is a regular file of the size and
// modification time a pushed file has, which Sync does not copy again.
func have(target string, info fs.FileInfo) bool {
	got, err := os.Lstat(target)
	return err == nil && got.Mode().IsRegular() && got.Size() == info.Size() && got.ModTime().Equal(info.ModTime())
}

// stale lists the files under local's dists and pool that are not among
// pushed, and the bytes they hold. It reads local through os.Root, as Sync
// reads staging, so that no link in local leads it, or prune after it, out.
func stale(local string, pushed []string) ([]string, int64, error) {
	root, err := os.OpenRoot(local)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()
	keep := make(map[string]bool, len(pushed))
	for _, name := range pushed {
		keep[name] = true
	}
	var names []string
	var size int64
	for _, top := range []string{"dists", "pool"} {
		err := fs.WalkDir(root.FS(), top, func(name string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || keep[name] {
				return nil
			}
			names = append(names, name)
			if info, err := entry.Info(); err == nil {
				size += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, 0, fmt.Errorf("read the local repository: %w", err)
		}
	}
	return names, size, nil
}

// prune removes the files stale found from local, and the directories under
// dists and pool that leaves empty, through os.Root.
func prune(local string, names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	root, err := os.OpenRoot(local)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	removed := 0
	for _, name := range names {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("remove %s, which the push no longer has: %w", name, err)
		}
		removed++
	}
	var directories []string
	for _, top := range []string{"dists", "pool"} {
		_ = fs.WalkDir(root.FS(), top, func(name string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				directories = append(directories, name)
			}
			return nil
		})
	}
	// Deepest first; a directory that still holds files stays.
	for _, directory := range slices.Backward(directories) {
		_ = root.Remove(directory)
	}
	return removed, nil
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
	if have(target, info) {
		return false, 0, nil
	}
	//nolint:gosec // apt reads the local repository as _apt, as the runbook installs it
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
