package files

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// The file system port, its errors and the snapshot objects live in
// internal/filesystem, which the root file broker links without this package
// (#221); these names keep the catalog's callers as they were.
type (
	FileSystem      = filesystem.FileSystem
	FileInfo        = filesystem.FileInfo
	ScanEntry       = filesystem.ScanEntry
	ScanSkip        = filesystem.ScanSkip
	RootFileSystem  = filesystem.RootFileSystem
	EntryKind       = filesystem.EntryKind
	SnapshotObject  = filesystem.SnapshotObject
	SnapshotBackend = filesystem.SnapshotBackend
	SnapshotSurvey  = filesystem.SnapshotSurvey
)

const (
	KindFile           = filesystem.KindFile
	KindDirectory      = filesystem.KindDirectory
	KindSymlink        = filesystem.KindSymlink
	KindOther          = filesystem.KindOther
	EntryKindFile      = filesystem.EntryKindFile
	EntryKindDirectory = filesystem.EntryKindDirectory
)

var (
	ErrForbidden         = filesystem.ErrForbidden
	ErrNotFound          = filesystem.ErrNotFound
	ErrInvalidName       = filesystem.ErrInvalidName
	ErrNameTooLong       = filesystem.ErrNameTooLong
	ErrUnsupportedType   = filesystem.ErrUnsupportedType
	ErrVolumeUnavailable = filesystem.ErrVolumeUnavailable
	ErrInsufficientSpace = filesystem.ErrInsufficientSpace
	ErrInvalidSelection  = filesystem.ErrInvalidSelection
)

// NewRootFileSystem is filesystem.NewRootFileSystem.
func NewRootFileSystem(base string, createBase bool) *RootFileSystem {
	return filesystem.NewRootFileSystem(base, createBase)
}

// WalkSnapshot is filesystem.WalkSnapshot.
func WalkSnapshot(ctx context.Context, root string, visit func(SnapshotObject) error) error {
	return filesystem.WalkSnapshot(ctx, root, visit)
}
