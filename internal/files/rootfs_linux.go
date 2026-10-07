package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// RootFileSystem confines every operation beneath one base directory. The
// File Broker's worker uses it as the signed-in user; development mode uses it
// as the Product Service.
//
// Paths are resolved by the kernel with openat2(RESOLVE_BENEATH |
// RESOLVE_NO_SYMLINKS): ".." and symlinks cannot leave the base, and passing
// through a directory needs only search permission. Space containers and
// trash roots are deliberately traverse-only (ADR 0008), which a
// directory-by-directory open (such as os.Root) would not allow.
type RootFileSystem struct {
	base       string
	createBase bool
}

// NewRootFileSystem confines operations to base. createBase lets development
// volumes create base on first use; production workers never create it.
func NewRootFileSystem(base string, createBase bool) *RootFileSystem {
	return &RootFileSystem{base: filepath.Clean(base), createBase: createBase}
}

const resolveBeneath = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// relative converts an absolute path into a slash-separated path below base.
func (r *RootFileSystem) relative(name string) (string, error) {
	relative, err := filepath.Rel(r.base, filepath.Clean(name))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("%w: %s is outside the data volume", ErrForbidden, name)
	}
	return filepath.ToSlash(relative), nil
}

// openBase opens the base for one operation. A fresh descriptor per
// operation avoids writing through a stale one after the data volume is
// remounted.
func (r *RootFileSystem) openBase() (int, error) {
	if r.createBase {
		if err := os.MkdirAll(r.base, 0o770); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Open(r.base, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: r.base, Err: err}
	}
	return fd, nil
}

// openDirectory opens relative (a directory below base) with flags.
func (r *RootFileSystem) openDirectory(relative string, flags int) (int, error) {
	base, err := r.openBase()
	if err != nil {
		return -1, err
	}
	defer unix.Close(base)
	fd, err := unix.Openat2(base, relative, &unix.OpenHow{
		Flags: uint64(flags | unix.O_DIRECTORY | unix.O_CLOEXEC), Resolve: resolveBeneath,
	})
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: relative, Err: err}
	}
	return fd, nil
}

// parent opens the directory that holds name and returns it with the final
// path component. Only search permission is needed on the way there.
func (r *RootFileSystem) parent(name string) (int, string, string, error) {
	relative, err := r.relative(name)
	if err != nil {
		return -1, "", "", err
	}
	directory, last := path.Split(relative)
	if directory == "" {
		directory = "."
	}
	fd, err := r.openDirectory(directory, unix.O_PATH)
	if err != nil {
		return -1, "", "", err
	}
	return fd, last, relative, nil
}

func (r *RootFileSystem) Lstat(_ context.Context, name string) (FileInfo, error) {
	dir, last, relative, err := r.parent(name)
	if err != nil {
		return FileInfo{}, err
	}
	defer unix.Close(dir)
	var stat unix.Stat_t
	if err := unix.Fstatat(dir, last, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileInfo{}, &fs.PathError{Op: "lstat", Path: relative, Err: err}
	}
	return infoFromStat(path.Base(relative), &stat), nil
}

func infoFromStat(name string, stat *unix.Stat_t) FileInfo {
	kind := KindOther
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = KindFile
	case unix.S_IFDIR:
		kind = KindDirectory
	case unix.S_IFLNK:
		kind = KindSymlink
	}
	return FileInfo{
		Name: name, Kind: kind, Size: stat.Size, Inode: stat.Ino,
		ModTime: time.Unix(stat.Mtim.Unix()).UTC(),
	}
}

func (r *RootFileSystem) Scan(ctx context.Context, name string, skip ScanSkip) ([]ScanEntry, error) {
	relative, err := r.relative(name)
	if err != nil {
		return nil, err
	}
	fd, err := r.openDirectory(relative, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	var entries []ScanEntry
	if err := scanDirectory(ctx, fd, "", skip, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// scanDirectory lists the directory open at fd, which it closes. A child
// directory the caller cannot read is listed without its contents.
func scanDirectory(ctx context.Context, fd int, prefix string, skip ScanSkip, entries *[]ScanEntry) error {
	directory := os.NewFile(uintptr(fd), prefix)
	defer directory.Close()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, child := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip.matches(child) {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			continue
		} else if err != nil {
			return &fs.PathError{Op: "lstat", Path: prefix + child, Err: err}
		}
		entry := ScanEntry{Path: prefix + child, Info: infoFromStat(child, &stat)}
		*entries = append(*entries, entry)
		if entry.Info.Kind != KindDirectory {
			continue
		}
		childFD, err := unix.Openat(fd, child, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "open", Path: entry.Path, Err: err}
		}
		if err := scanDirectory(ctx, childFD, entry.Path+"/", skip, entries); err != nil {
			return err
		}
	}
	return nil
}

func (r *RootFileSystem) Mkdir(_ context.Context, name string) error {
	dir, last, relative, err := r.parent(name)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	if err := unix.Mkdirat(dir, last, 0o770); err != nil {
		return &fs.PathError{Op: "mkdir", Path: relative, Err: err}
	}
	return nil
}

func (r *RootFileSystem) MkdirAll(_ context.Context, name string) error {
	relative, err := r.relative(name)
	if err != nil {
		return err
	}
	current, err := r.openBase()
	if err != nil {
		return err
	}
	defer func() { unix.Close(current) }()
	if relative == "." {
		return nil
	}
	walked := ""
	for _, component := range strings.Split(relative, "/") {
		walked = path.Join(walked, component)
		if err := unix.Mkdirat(current, component, 0o770); err != nil && !errors.Is(err, unix.EEXIST) {
			return &fs.PathError{Op: "mkdir", Path: walked, Err: err}
		}
		next, err := unix.Openat2(current, component, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: resolveBeneath,
		})
		if err != nil {
			return &fs.PathError{Op: "open", Path: walked, Err: err}
		}
		unix.Close(current)
		current = next
	}
	return nil
}

// Rename never replaces an existing target.
func (r *RootFileSystem) Rename(_ context.Context, from, to string) error {
	sourceDir, sourceName, source, err := r.parent(from)
	if err != nil {
		return err
	}
	defer unix.Close(sourceDir)
	targetDir, targetName, target, err := r.parent(to)
	if err != nil {
		return err
	}
	defer unix.Close(targetDir)
	err = unix.Renameat2(sourceDir, sourceName, targetDir, targetName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) {
		// Filesystems without RENAME_NOREPLACE: check, then rename.
		var stat unix.Stat_t
		if statErr := unix.Fstatat(targetDir, targetName, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
			err = unix.EEXIST
		} else {
			err = unix.Renameat(sourceDir, sourceName, targetDir, targetName)
		}
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: err}
	}
	return nil
}

func (r *RootFileSystem) Remove(_ context.Context, name string) error {
	dir, last, relative, err := r.parent(name)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	if err := removeAt(dir, last); err != nil {
		return &fs.PathError{Op: "remove", Path: relative, Err: err}
	}
	return nil
}

func removeAt(dir int, name string) error {
	err := unix.Unlinkat(dir, name, 0)
	if errors.Is(err, unix.EISDIR) {
		err = unix.Unlinkat(dir, name, unix.AT_REMOVEDIR)
	}
	return err
}

func (r *RootFileSystem) RemoveAll(ctx context.Context, name string) error {
	dir, last, relative, err := r.parent(name)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	if relative == "." {
		return fmt.Errorf("%w: refusing to remove the data-volume root", ErrForbidden)
	}
	if err := removeAllAt(ctx, dir, last); err != nil {
		return &fs.PathError{Op: "remove", Path: relative, Err: err}
	}
	return nil
}

func removeAllAt(ctx context.Context, dir int, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := removeAt(dir, name)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.ENOTEMPTY) && !errors.Is(err, unix.EEXIST) {
		return err
	}
	childFD, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	child := os.NewFile(uintptr(childFD), name)
	names, err := child.Readdirnames(-1)
	if err == nil {
		for _, grandchild := range names {
			if err = removeAllAt(ctx, childFD, grandchild); err != nil {
				break
			}
		}
	}
	_ = child.Close()
	if err != nil {
		return err
	}
	return unix.Unlinkat(dir, name, unix.AT_REMOVEDIR)
}

func (r *RootFileSystem) Open(_ context.Context, name string) (*os.File, error) {
	dir, last, relative, err := r.parent(name)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, last, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: relative, Err: err}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, ErrUnsupportedType
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), relative), nil
}

func (r *RootFileSystem) CreateTemp(_ context.Context, directory, prefix string) (*os.File, string, error) {
	if strings.ContainsAny(prefix, "/\\\x00") {
		return nil, "", ErrInvalidName
	}
	relative, err := r.relative(directory)
	if err != nil {
		return nil, "", err
	}
	dir, err := r.openDirectory(relative, unix.O_PATH)
	if err != nil {
		return nil, "", err
	}
	defer unix.Close(dir)
	for attempt := 0; attempt < 16; attempt++ {
		suffix := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(suffix)
		fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o660)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", &fs.PathError{Op: "create", Path: path.Join(relative, name), Err: err}
		}
		created := filepath.Join(filepath.Clean(directory), name)
		return os.NewFile(uintptr(fd), created), created, nil
	}
	return nil, "", fs.ErrExist
}

// Copy copies a regular file or directory tree to a target that must not exist.
func (r *RootFileSystem) Copy(ctx context.Context, from, to string) error {
	sourceDir, sourceName, source, err := r.parent(from)
	if err != nil {
		return err
	}
	defer unix.Close(sourceDir)
	targetDir, targetName, _, err := r.parent(to)
	if err != nil {
		return err
	}
	defer unix.Close(targetDir)
	if err := copyAt(ctx, sourceDir, sourceName, targetDir, targetName); err != nil {
		return &fs.PathError{Op: "copy", Path: source, Err: err}
	}
	return nil
}

func copyAt(ctx context.Context, sourceDir int, sourceName string, targetDir int, targetName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(sourceDir, sourceName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	mode := uint32(stat.Mode & 0o777)
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return copyFileAt(sourceDir, sourceName, targetDir, targetName, mode)
	case unix.S_IFDIR:
		if err := unix.Mkdirat(targetDir, targetName, mode); err != nil {
			return err
		}
		sourceFD, err := unix.Openat(sourceDir, sourceName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		source := os.NewFile(uintptr(sourceFD), sourceName)
		defer source.Close()
		targetFD, err := unix.Openat(targetDir, targetName, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(targetFD)
		names, err := source.Readdirnames(-1)
		if err != nil {
			return err
		}
		for _, child := range names {
			if err := copyAt(ctx, sourceFD, child, targetFD, child); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrUnsupportedType
	}
}

func copyFileAt(sourceDir int, sourceName string, targetDir int, targetName string, mode uint32) error {
	inputFD, err := unix.Openat(sourceDir, sourceName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	input := os.NewFile(uintptr(inputFD), sourceName)
	defer input.Close()
	outputFD, err := unix.Openat(targetDir, targetName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	output := os.NewFile(uintptr(outputFD), targetName)
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = unix.Unlinkat(targetDir, targetName, 0)
	}
	return err
}

func (r *RootFileSystem) Usage(ctx context.Context, name string) (int64, error) {
	info, err := r.Lstat(ctx, name)
	if err != nil {
		return 0, err
	}
	if info.Kind != KindDirectory {
		return info.Size, nil
	}
	entries, err := r.Scan(ctx, name, ScanSkip{})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.Info.Kind == KindFile {
			total += entry.Info.Size
		}
	}
	return total, nil
}

var _ FileSystem = (*RootFileSystem)(nil)
