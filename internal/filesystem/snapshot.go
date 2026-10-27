package filesystem

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SnapshotObject struct {
	Key        string    `json:"key"`
	ParentKey  string    `json:"parentKey,omitempty"`
	Name       string    `json:"name"`
	Kind       EntryKind `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt,omitzero"`
}

// SnapshotBackend accepts stable space and snapshot identities. Implementations
// own the mapping to Btrfs subvolumes or deterministic development directories.
// Create and Delete may be repeated: taking a snapshot that exists and
// deleting one that does not both succeed, so a caller that lost an answer
// retries or cleans up by the snapshot's ID.
type SnapshotBackend interface {
	Create(ctx context.Context, spaceID, snapshotID string) error
	// Objects calls visit for every file and folder a snapshot holds, each
	// folder before its contents, and stops at the first error. A snapshot
	// that does not exist fails with ErrNotFound.
	Objects(ctx context.Context, snapshotID string, visit func(SnapshotObject) error) error
	Open(ctx context.Context, snapshotID, key string) (io.ReadCloser, SnapshotObject, error)
	Delete(ctx context.Context, spaceID, snapshotID string) error
	// Survey reports which of known are on the data volume and how many
	// other snapshots it holds. It deletes nothing.
	Survey(ctx context.Context, known []string) (SnapshotSurvey, error)
}

// SnapshotSurvey is what a Survey found: the known snapshots that exist and
// how many others the data volume holds.
type SnapshotSurvey struct {
	Present []string `json:"present"`
	Unknown int      `json:"unknown"`
}

// WalkSnapshot calls visit for the files and folders below root in
// pre-order, so each folder comes before its contents. It leaves out A-NAS
// bookkeeping, symlinks and special files, and stops when ctx ends.
func WalkSnapshot(ctx context.Context, root string, visit func(SnapshotObject) error) error {
	return filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
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
		kind := EntryKindFile
		if info.IsDir() {
			kind = EntryKindDirectory
		}
		return visit(SnapshotObject{
			Key: filepath.ToSlash(relative), ParentKey: parentKey, Name: info.Name(), Kind: kind, SizeBytes: info.Size(),
			ModifiedAt: info.ModTime().UTC(),
		})
	})
}
