package filebroker

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

type sharePublicationKey struct{}

type sharePublication struct{ epoch, id string }

// WithSharePublication attaches a server-issued lifetime and share fence to
// ordinary file operations; session authorization and the user's UID still apply.
func WithSharePublication(ctx context.Context, epoch, id string) context.Context {
	return context.WithValue(ctx, sharePublicationKey{}, sharePublication{epoch, id})
}

// ResetShares drains previously accepted publication operations and invalidates
// even delayed requests from the old Product/Sharing Service process lifetime.
// Control operations are reachable only on the existing product-only UDS.
func (c *Client) ResetShares(ctx context.Context) (string, error) {
	result, file, err := c.call(ctx, wire.Request{Op: wire.OpShareReset})
	wire.CloseFile(file)
	return result.Path, err
}

func (c *Client) FenceShare(ctx context.Context, epoch, id string) error {
	_, file, err := c.call(ctx, wire.Request{Op: wire.OpShareFence, ShareEpoch: epoch, ShareID: id})
	wire.CloseFile(file)
	return err
}

// CheckShareEpoch checks the publication lifetime without mutating it.
func (c *Client) CheckShareEpoch(ctx context.Context, epoch string) error {
	_, file, err := c.call(ctx, wire.Request{Op: wire.OpShareCheck, ShareEpoch: epoch})
	wire.CloseFile(file)
	return err
}
