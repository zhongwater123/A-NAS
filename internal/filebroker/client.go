package filebroker

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// Client is the Product Service's files.FileSystem. Every call carries the
// session token from the request context; the broker decides who it is.
type Client struct {
	socket string
}

func NewClient(socket string) *Client { return &Client{socket: socket} }

func (c *Client) call(ctx context.Context, req request) (response, *os.File, error) {
	token := accounts.SessionToken(ctx)
	if token == "" {
		return response{}, nil, fmt.Errorf("%w: no session for file operation", files.ErrForbidden)
	}
	req.Token = token
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return response{}, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	conn := connection.(*net.UnixConn)
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeMessage(conn, req, nil); err != nil {
		return response{}, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var result response
	file, err := readMessage(conn, &result)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, nil, ctx.Err()
		}
		return response{}, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if result.Error != nil {
		closeFile(file)
		return response{}, nil, result.Error
	}
	return result, file, nil
}

func (c *Client) simple(ctx context.Context, req request) error {
	_, file, err := c.call(ctx, req)
	closeFile(file)
	return err
}

func (c *Client) Lstat(ctx context.Context, path string) (files.FileInfo, error) {
	result, file, err := c.call(ctx, request{Op: opLstat, Path: path})
	closeFile(file)
	if err != nil {
		return files.FileInfo{}, err
	}
	if result.Info == nil {
		return files.FileInfo{}, fmt.Errorf("%w: missing file information", ErrUnavailable)
	}
	return *result.Info, nil
}

func (c *Client) Scan(ctx context.Context, path string, skip files.ScanSkip) ([]files.ScanEntry, error) {
	result, file, err := c.call(ctx, request{Op: opScan, Path: path, Skip: skip})
	closeFile(file)
	return result.Entries, err
}

func (c *Client) Mkdir(ctx context.Context, path string) error {
	return c.simple(ctx, request{Op: opMkdir, Path: path})
}

func (c *Client) MkdirAll(ctx context.Context, path string) error {
	return c.simple(ctx, request{Op: opMkdirAll, Path: path})
}

func (c *Client) Rename(ctx context.Context, from, to string) error {
	return c.simple(ctx, request{Op: opRename, Path: from, Target: to})
}

func (c *Client) Remove(ctx context.Context, path string) error {
	return c.simple(ctx, request{Op: opRemove, Path: path})
}

func (c *Client) RemoveAll(ctx context.Context, path string) error {
	return c.simple(ctx, request{Op: opRemoveAll, Path: path})
}

func (c *Client) Open(ctx context.Context, path string) (*os.File, error) {
	_, file, err := c.call(ctx, request{Op: opOpen, Path: path})
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("%w: no file descriptor returned", ErrUnavailable)
	}
	return file, nil
}

func (c *Client) CreateTemp(ctx context.Context, dir, prefix string) (*os.File, string, error) {
	result, file, err := c.call(ctx, request{Op: opCreateTemp, Path: dir, Prefix: prefix})
	if err != nil {
		return nil, "", err
	}
	if file == nil || result.Path == "" {
		closeFile(file)
		return nil, "", fmt.Errorf("%w: no file descriptor returned", ErrUnavailable)
	}
	return file, result.Path, nil
}

func (c *Client) Copy(ctx context.Context, from, to string) error {
	return c.simple(ctx, request{Op: opCopy, Path: from, Target: to})
}

func (c *Client) Usage(ctx context.Context, path string) (int64, error) {
	result, file, err := c.call(ctx, request{Op: opUsage, Path: path})
	closeFile(file)
	return result.Size, err
}

var _ files.FileSystem = (*Client)(nil)
