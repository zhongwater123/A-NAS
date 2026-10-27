// Package filesystem is the file system port that the files service and the
// file broker share: the operations, their values and errors, the data
// volume's Linux implementation and the objects a snapshot holds. The catalog,
// spaces and SQLite stay in internal/files, which the root Host Agent does not
// link (#221).
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// FileSystem performs the file operations of one request. In production the
// File Broker runs them as the signed-in user (ADR 0008), so the kernel
// decides every access; paths are absolute and must stay on the data volume.
type FileSystem interface {
	Lstat(ctx context.Context, path string) (FileInfo, error)
	// Scan lists everything below path in pre-order. It does not follow
	// symlinks, does not descend into skipped names, and lists opaque and
	// unreadable directories without their contents.
	Scan(ctx context.Context, path string, skip ScanSkip) ([]ScanEntry, error)
	Mkdir(ctx context.Context, path string) error
	MkdirAll(ctx context.Context, path string) error
	// Rename fails with fs.ErrExist instead of replacing an existing target.
	Rename(ctx context.Context, from, to string) error
	Remove(ctx context.Context, path string) error
	RemoveAll(ctx context.Context, path string) error
	// Open returns a regular file opened read-only.
	Open(ctx context.Context, path string) (*os.File, error)
	// CreateTemp creates a new file in dir and returns it opened for writing
	// together with its path.
	CreateTemp(ctx context.Context, dir, prefix string) (*os.File, string, error)
	// Copy copies a regular file or directory tree to a path that must not exist.
	Copy(ctx context.Context, from, to string) error
	// Usage returns the total size of the regular files at or below path.
	Usage(ctx context.Context, path string) (int64, error)
	// SetModTime sets the modification time of a regular file and leaves
	// its access time; it never follows a symlink. Restores use it so a
	// file comes back as it was saved.
	SetModTime(ctx context.Context, path string, modTime time.Time) error
}

var (
	ErrForbidden         = errors.New("file space access is forbidden")
	ErrNotFound          = errors.New("file entry not found")
	ErrInvalidName       = errors.New("invalid file name")
	ErrUnsupportedType   = errors.New("unsupported file type")
	ErrVolumeUnavailable = errors.New("data volume is unavailable")
	ErrInsufficientSpace = errors.New("data volume reserve would be exhausted")
	ErrInvalidSelection  = errors.New("download selection is invalid")
	// ErrNameTooLong reports a name over the 255 bytes Linux allows for one
	// path component: 85 Chinese characters fill it.
	ErrNameTooLong = fmt.Errorf("%w: longer than %d bytes", ErrInvalidName, MaxNameBytes)
)

// MaxNameBytes is the Linux limit for one path component.
const MaxNameBytes = 255

// EntryKind tells files from folders in catalogs and snapshots.
type EntryKind string

const (
	EntryKindFile      EntryKind = "file"
	EntryKindDirectory EntryKind = "directory"
)

const (
	KindFile      = "file"
	KindDirectory = "directory"
	KindSymlink   = "symlink"
	KindOther     = "other"
)

type FileInfo struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Inode   uint64    `json:"inode"`
}

type ScanEntry struct {
	// Path is slash-separated and relative to the scanned directory.
	Path string   `json:"path"`
	Info FileInfo `json:"info"`
}

type ScanSkip struct {
	Names    []string `json:"names,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
	// Opaque prefixes name directories that are listed but not entered.
	Opaque []string `json:"opaque,omitempty"`
	// Shallow lists the directory's children without entering any of them.
	Shallow bool `json:"shallow,omitempty"`
	// Strict refuses a disappearing or unreadable child instead of returning
	// an incomplete tree. Limit bounds memory for explicit share selections.
	Strict bool `json:"strict,omitempty"`
	Limit  int  `json:"limit,omitempty"`
}

func (s ScanSkip) matches(name string) bool {
	if slices.Contains(s.Names, name) {
		return true
	}
	return slices.ContainsFunc(s.Prefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func (s ScanSkip) opaque(name string) bool {
	return s.Shallow || slices.ContainsFunc(s.Opaque, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}
