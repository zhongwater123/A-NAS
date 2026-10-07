package files

import (
	"context"
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
	// symlinks, does not descend into skipped names, and lists unreadable
	// directories without their contents.
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
}

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
}

func (s ScanSkip) matches(name string) bool {
	if slices.Contains(s.Names, name) {
		return true
	}
	return slices.ContainsFunc(s.Prefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}
