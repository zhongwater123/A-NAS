package wire

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// FileOps is a filesystem.FileSystem over one way of sending a request: to
// the broker with the session (filebroker.Client), or straight to a worker
// (the executor of the assistant's changes). Either way Call returns the
// whole reply, with a scan's pages gathered.
type FileOps struct {
	Call func(context.Context, Request) (Response, *os.File, error)
}

func (c *FileOps) simple(ctx context.Context, req Request) error {
	_, file, err := c.Call(ctx, req)
	CloseFile(file)
	return err
}

func (c *FileOps) Lstat(ctx context.Context, path string) (filesystem.FileInfo, error) {
	result, file, err := c.Call(ctx, Request{Op: OpLstat, Path: path})
	CloseFile(file)
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	if result.Info == nil {
		return filesystem.FileInfo{}, fmt.Errorf("%w: missing file information", ErrUnavailable)
	}
	return *result.Info, nil
}

func (c *FileOps) Scan(ctx context.Context, path string, skip filesystem.ScanSkip) ([]filesystem.ScanEntry, error) {
	result, file, err := c.Call(ctx, Request{Op: OpScan, Path: path, Skip: skip, Paged: true})
	CloseFile(file)
	return result.Entries, err
}

func (c *FileOps) Mkdir(ctx context.Context, path string) error {
	return c.simple(ctx, Request{Op: OpMkdir, Path: path})
}

func (c *FileOps) MkdirAll(ctx context.Context, path string) error {
	return c.simple(ctx, Request{Op: OpMkdirAll, Path: path})
}

func (c *FileOps) Rename(ctx context.Context, from, to string) error {
	return c.simple(ctx, Request{Op: OpRename, Path: from, Target: to})
}

func (c *FileOps) Remove(ctx context.Context, path string) error {
	return c.simple(ctx, Request{Op: OpRemove, Path: path})
}

func (c *FileOps) RemoveAll(ctx context.Context, path string) error {
	return c.simple(ctx, Request{Op: OpRemoveAll, Path: path})
}

func (c *FileOps) Open(ctx context.Context, path string) (*os.File, error) {
	_, file, err := c.Call(ctx, Request{Op: OpOpen, Path: path})
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("%w: no file descriptor returned", ErrUnavailable)
	}
	return file, nil
}

func (c *FileOps) CreateTemp(ctx context.Context, dir, prefix string) (*os.File, string, error) {
	result, file, err := c.Call(ctx, Request{Op: OpCreateTemp, Path: dir, Prefix: prefix})
	if err != nil {
		return nil, "", err
	}
	if file == nil || result.Path == "" {
		CloseFile(file)
		return nil, "", fmt.Errorf("%w: no file descriptor returned", ErrUnavailable)
	}
	return file, result.Path, nil
}

func (c *FileOps) Copy(ctx context.Context, from, to string) error {
	return c.simple(ctx, Request{Op: OpCopy, Path: from, Target: to})
}

func (c *FileOps) Usage(ctx context.Context, path string) (int64, error) {
	result, file, err := c.Call(ctx, Request{Op: OpUsage, Path: path})
	CloseFile(file)
	return result.Size, err
}

func (c *FileOps) SetModTime(ctx context.Context, path string, modTime time.Time) error {
	return c.simple(ctx, Request{Op: OpSetModTime, Path: path, ModTime: modTime})
}
