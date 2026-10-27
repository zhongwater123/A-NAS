// Package filebroker is the Product Service's client of the root file broker
// (internal/filebroker/rootbroker): a filesystem.FileSystem whose every call
// the broker runs as the session's own Linux account, and the terminal,
// assistant and change requests that the broker checks the same way
// (ADR 0008, ADR 0024).
package filebroker

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

var (
	// ErrUnavailable reports that the File Broker or a worker could not be reached.
	ErrUnavailable = wire.ErrUnavailable
	// ErrChangeNotFound, ErrChangeInvalid and ErrChangeDecided are the
	// executor's refusals of the assistant's changes.
	ErrChangeNotFound = wire.ErrChangeNotFound
	ErrChangeInvalid  = wire.ErrChangeInvalid
	ErrChangeDecided  = wire.ErrChangeDecided
)

// Client is the Product Service's filesystem.FileSystem. Every call carries the
// session token from the request context; the broker decides who it is.
type Client struct {
	fileOps
	socket string
}

// fileOps gives Client the FileSystem methods of wire.FileOps under an
// unexported field, so no caller can replace how the client calls.
type fileOps = wire.FileOps

func NewClient(socket string) *Client {
	c := &Client{socket: socket}
	c.fileOps = wire.FileOps{Call: c.call}
	return c
}

func (c *Client) call(ctx context.Context, req wire.Request) (wire.Response, *os.File, error) {
	token := accountid.SessionToken(ctx)
	if token == "" && req.Op != wire.OpShareReset && req.Op != wire.OpShareFence && req.Op != wire.OpShareCheck {
		return wire.Response{}, nil, fmt.Errorf("%w: no session for file operation", filesystem.ErrForbidden)
	}
	req.Token = token
	if publication, ok := ctx.Value(sharePublicationKey{}).(sharePublication); ok {
		req.ShareEpoch, req.ShareID = publication.epoch, publication.id
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return wire.Response{}, nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	conn := connection.(*net.UnixConn)
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := wire.WriteMessage(conn, req, nil); err != nil {
		return wire.Response{}, nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	result, file, err := wire.ReadReply(conn)
	if err != nil {
		if ctx.Err() != nil {
			return wire.Response{}, nil, ctx.Err()
		}
		return wire.Response{}, nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	if result.Error != nil {
		wire.CloseFile(file)
		return wire.Response{}, nil, result.Error
	}
	return result, file, nil
}

var _ filesystem.FileSystem = (*Client)(nil)
