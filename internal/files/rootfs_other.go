//go:build !linux

package files

import (
	"context"
	"errors"
	"os"
)

// RootFileSystem needs openat2, so it only works on Linux; elsewhere every
// operation fails.
type RootFileSystem struct{}

func NewRootFileSystem(string, bool) *RootFileSystem { return &RootFileSystem{} }

var errRootFileSystemUnsupported = errors.New("the data-volume file system requires Linux")

func (*RootFileSystem) Lstat(context.Context, string) (FileInfo, error) {
	return FileInfo{}, errRootFileSystemUnsupported
}
func (*RootFileSystem) Scan(context.Context, string, ScanSkip) ([]ScanEntry, error) {
	return nil, errRootFileSystemUnsupported
}
func (*RootFileSystem) Mkdir(context.Context, string) error    { return errRootFileSystemUnsupported }
func (*RootFileSystem) MkdirAll(context.Context, string) error { return errRootFileSystemUnsupported }
func (*RootFileSystem) Rename(context.Context, string, string) error {
	return errRootFileSystemUnsupported
}
func (*RootFileSystem) Remove(context.Context, string) error    { return errRootFileSystemUnsupported }
func (*RootFileSystem) RemoveAll(context.Context, string) error { return errRootFileSystemUnsupported }
func (*RootFileSystem) Open(context.Context, string) (*os.File, error) {
	return nil, errRootFileSystemUnsupported
}
func (*RootFileSystem) CreateTemp(context.Context, string, string) (*os.File, string, error) {
	return nil, "", errRootFileSystemUnsupported
}
func (*RootFileSystem) Copy(context.Context, string, string) error {
	return errRootFileSystemUnsupported
}
func (*RootFileSystem) Usage(context.Context, string) (int64, error) {
	return 0, errRootFileSystemUnsupported
}

var _ FileSystem = (*RootFileSystem)(nil)
