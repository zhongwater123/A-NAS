package linux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// Create takes a read-only Btrfs snapshot of a space. A snapshot that already
// exists counts as taken, so a caller that lost the answer may ask again.
func (e *Executor) Create(ctx context.Context, spaceID, snapshotID string) error {
	spaceRoot, err := e.resolveSpaceRoot(spaceID)
	if err != nil {
		return err
	}
	snapshotRoot := e.snapshotRoot(snapshotID)
	if _, err := os.Lstat(snapshotRoot); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(snapshotRoot), 0o700); err != nil {
		return err
	}
	_, err = e.runner.Run(ctx, "btrfs", []string{"subvolume", "snapshot", "-r", spaceRoot, snapshotRoot}, "")
	return err
}

// Objects walks a read-only snapshot, so the same snapshot always lists the
// same objects.
func (e *Executor) Objects(ctx context.Context, snapshotID string, visit func(filesystem.SnapshotObject) error) error {
	root := e.snapshotRoot(snapshotID)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return filesystem.ErrNotFound
	} else if err != nil {
		return err
	}
	return filesystem.WalkSnapshot(ctx, root, visit)
}

// Open opens one object of a snapshot. The kernel resolves the key beneath
// the snapshot root and refuses ".." out of it and any symbolic link on the
// way, as Objects skips them; O_NONBLOCK keeps a FIFO from blocking the open.
func (e *Executor) Open(_ context.Context, snapshotID, key string) (io.ReadCloser, filesystem.SnapshotObject, error) {
	root := e.snapshotRoot(snapshotID)
	relative := filepath.FromSlash(key)
	if strings.ContainsRune(relative, 0) {
		return nil, filesystem.SnapshotObject{}, filesystem.ErrNotFound
	}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, filesystem.SnapshotObject{}, snapshotOpenError(root, err)
	}
	defer unix.Close(rootFD)
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, filesystem.SnapshotObject{}, snapshotOpenError(key, err)
	}
	reader := os.NewFile(uintptr(fd), filepath.Join(root, relative))
	info, err := reader.Stat()
	if err != nil {
		_ = reader.Close()
		return nil, filesystem.SnapshotObject{}, err
	}
	if !info.Mode().IsRegular() {
		_ = reader.Close()
		return nil, filesystem.SnapshotObject{}, filesystem.ErrUnsupportedType
	}
	return reader, filesystem.SnapshotObject{Key: key, Name: info.Name(), Kind: filesystem.EntryKindFile, SizeBytes: info.Size()}, nil
}

// snapshotOpenError reads a missing entry, a symbolic link on the way, or a
// path out of the snapshot as not found; anything else is a failure.
func snapshotOpenError(path string, err error) error {
	for _, missing := range []error{unix.ENOENT, unix.ENOTDIR, unix.ELOOP, unix.EXDEV} {
		if errors.Is(err, missing) {
			return filesystem.ErrNotFound
		}
	}
	return &os.PathError{Op: "open", Path: path, Err: err}
}

func (e *Executor) Delete(ctx context.Context, spaceID, snapshotID string) error {
	if _, err := e.resolveSpaceRoot(spaceID); err != nil {
		return err
	}
	root := e.snapshotRoot(snapshotID)
	if !withinDirectory(e.snapshotsRoot(), root) {
		return filesystem.ErrNotFound
	}
	// Already gone counts as deleted, so a deletion that stopped half way
	// can be finished.
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	_, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "delete", root}, "")
	return err
}

// Prune deletes the snapshot subvolumes that keep does not name, such as one
// the Product Service was taking when it failed or stopped, and reports
// which of keep exist. It touches only entries named the way snapshotRoot
// names them, and only on the mounted data volume. A subvolume it cannot
// delete is logged and left for the next prune.
func (e *Executor) Prune(ctx context.Context, keep []string) (filesystem.SnapshotPrune, error) {
	if !e.dataVolumeReady() {
		return filesystem.SnapshotPrune{}, filesystem.ErrVolumeUnavailable
	}
	wanted := make(map[string]string, len(keep))
	for _, id := range keep {
		wanted[filepath.Base(e.snapshotRoot(id))] = id
	}
	pruned := filesystem.SnapshotPrune{Present: []string{}}
	items, err := os.ReadDir(e.snapshotsRoot())
	if errors.Is(err, os.ErrNotExist) {
		return pruned, nil
	}
	if err != nil {
		return filesystem.SnapshotPrune{}, err
	}
	for _, item := range items {
		if id, ok := wanted[item.Name()]; ok {
			pruned.Present = append(pruned.Present, id)
			continue
		}
		if !item.IsDir() || !snapshotDirectoryName.MatchString(item.Name()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return filesystem.SnapshotPrune{}, err
		}
		if _, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "delete", filepath.Join(e.snapshotsRoot(), item.Name())}, ""); err != nil {
			e.logger.WarnContext(ctx, "delete a snapshot nobody keeps failed", "subvolume", item.Name(), "error", err)
			continue
		}
		pruned.Removed++
	}
	return pruned, nil
}

func (e *Executor) resolveSpaceRoot(spaceID string) (string, error) {
	if e.registryError != nil {
		return "", e.registryError
	}
	e.spaceRootsMu.RLock()
	defer e.spaceRootsMu.RUnlock()
	root, ok := e.spaceRoots[spaceID]
	if !ok || !withinDirectory(e.mountPoint, root) {
		return "", filesystem.ErrNotFound
	}
	return root, nil
}

func (e *Executor) snapshotsRoot() string { return filepath.Join(e.mountPoint, ".a-nas-snapshots") }

func (e *Executor) snapshotRoot(snapshotID string) string {
	digest := sha256.Sum256([]byte("a-nas:snapshot:v1\x00" + snapshotID))
	return filepath.Join(e.snapshotsRoot(), hex.EncodeToString(digest[:16]))
}

// snapshotDirectoryName matches the names snapshotRoot gives.
var snapshotDirectoryName = regexp.MustCompile(`^[0-9a-f]{32}$`)

var _ filesystem.SnapshotBackend = (*Executor)(nil)
