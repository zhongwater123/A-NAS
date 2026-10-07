package linux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/files"
)

func (e *Executor) Create(ctx context.Context, spaceID, snapshotID string) ([]files.SnapshotObject, error) {
	spaceRoot, err := e.resolveSpaceRoot(spaceID)
	if err != nil {
		return nil, err
	}
	snapshotRoot := e.snapshotRoot(snapshotID)
	if _, err := os.Lstat(snapshotRoot); err == nil {
		return nil, files.ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(snapshotRoot), 0o700); err != nil {
		return nil, err
	}
	if _, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "snapshot", "-r", spaceRoot, snapshotRoot}, ""); err != nil {
		return nil, err
	}
	objects, err := walkSnapshot(snapshotRoot)
	if err != nil {
		_, _ = e.runner.Run(ctx, "btrfs", []string{"subvolume", "delete", snapshotRoot}, "")
		return nil, err
	}
	return objects, nil
}

func (e *Executor) Open(_ context.Context, snapshotID, key string) (io.ReadCloser, files.SnapshotObject, error) {
	root := e.snapshotRoot(snapshotID)
	path := filepath.Join(root, filepath.FromSlash(key))
	if !withinDirectory(root, path) {
		return nil, files.SnapshotObject{}, files.ErrNotFound
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, files.SnapshotObject{}, files.ErrNotFound
	}
	if !info.Mode().IsRegular() {
		return nil, files.SnapshotObject{}, files.ErrUnsupportedType
	}
	reader, err := os.Open(path)
	if err != nil {
		return nil, files.SnapshotObject{}, err
	}
	return reader, files.SnapshotObject{Key: key, Name: info.Name(), Kind: files.EntryKindFile, SizeBytes: info.Size()}, nil
}

func (e *Executor) Delete(ctx context.Context, spaceID, snapshotID string) error {
	if _, err := e.resolveSpaceRoot(spaceID); err != nil {
		return err
	}
	root := e.snapshotRoot(snapshotID)
	if !withinDirectory(filepath.Join(e.mountPoint, ".a-nas-snapshots"), root) {
		return files.ErrNotFound
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return files.ErrNotFound
	} else if err != nil {
		return err
	}
	_, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "delete", root}, "")
	return err
}

func (e *Executor) resolveSpaceRoot(spaceID string) (string, error) {
	if e.registryError != nil {
		return "", e.registryError
	}
	e.spaceRootsMu.RLock()
	defer e.spaceRootsMu.RUnlock()
	root, ok := e.spaceRoots[spaceID]
	if !ok || !withinDirectory(e.mountPoint, root) {
		return "", files.ErrNotFound
	}
	return root, nil
}

func (e *Executor) snapshotRoot(snapshotID string) string {
	digest := sha256.Sum256([]byte("a-nas:snapshot:v1\x00" + snapshotID))
	return filepath.Join(e.mountPoint, ".a-nas-snapshots", hex.EncodeToString(digest[:16]))
}

func walkSnapshot(root string) ([]files.SnapshotObject, error) {
	var objects []files.SnapshotObject
	err := filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(item.Name(), ".a-nas-") {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parentKey := filepath.ToSlash(filepath.Dir(relative))
		if parentKey == "." {
			parentKey = ""
		}
		kind := files.EntryKindFile
		if info.IsDir() {
			kind = files.EntryKindDirectory
		}
		objects = append(objects, files.SnapshotObject{
			Key: filepath.ToSlash(relative), ParentKey: parentKey, Name: info.Name(), Kind: kind, SizeBytes: info.Size(),
		})
		return nil
	})
	return objects, err
}

var _ files.SnapshotBackend = (*Executor)(nil)
