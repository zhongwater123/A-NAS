package rootbroker

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

func (s *Server) shareControl(req wire.Request) wire.Response {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	if req.Op == wire.OpShareReset {
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return wire.Response{Error: encodeError(err)}
		}
		s.shareEpoch = hex.EncodeToString(nonce[:])
		s.shareFenced = map[string]bool{}
		return wire.Response{Path: s.shareEpoch}
	}
	if req.ShareEpoch == "" || req.ShareEpoch != s.shareEpoch || (req.Op != wire.OpShareCheck && (req.ShareID == "" || len(req.ShareID) > 256)) {
		return wire.Response{Error: &wire.Error{Code: wire.CodeForbidden}}
	}
	if req.Op == wire.OpShareFence {
		s.shareFenced[req.ShareID] = true
	}
	return wire.Response{}
}
