package protocol

import (
	"errors"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
)

var errBadHeader = errors.New("bad song header")

func DecodeHeader(header string, source *app.SongSource) error {
	decoded, ok := decodeSource(header)
	if !ok {
		return errBadHeader
	}
	*source = decoded
	return nil
}
