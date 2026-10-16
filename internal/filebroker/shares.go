package filebroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

const opShareReset = "share_reset"
const opShareFence = "share_fence"
const opShareCheck = "share_check"

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
	result, file, err := c.call(ctx, request{Op: opShareReset})
	closeFile(file)
	return result.Path, err
}
func (c *Client) FenceShare(ctx context.Context, epoch, id string) error {
	return c.simple(ctx, request{Op: opShareFence, ShareEpoch: epoch, ShareID: id})
}

// CheckShareEpoch checks the publication lifetime without mutating it.
func (c *Client) CheckShareEpoch(ctx context.Context, epoch string) error {
	return c.simple(ctx, request{Op: opShareCheck, ShareEpoch: epoch})
}
func (s *Server) shareControl(req request) response {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	if req.Op == opShareReset {
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return response{Error: encodeError(err)}
		}
		s.shareEpoch = hex.EncodeToString(nonce[:])
		s.shareFenced = map[string]bool{}
		return response{Path: s.shareEpoch}
	}
	if req.ShareEpoch == "" || req.ShareEpoch != s.shareEpoch || (req.Op != opShareCheck && (req.ShareID == "" || len(req.ShareID) > 256)) {
		return response{Error: &wireError{Code: codeForbidden}}
	}
	if req.Op == opShareFence {
		s.shareFenced[req.ShareID] = true
	}
	return response{}
}
