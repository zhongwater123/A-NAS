package montageapi

import (
	"errors"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

var errBadHeader = errors.New("bad song header")

func DecodeHeader(header string, source *montage.SongSource) error {
	decoded, ok := decodeSource(header)
	if !ok {
		return errBadHeader
	}
	*source = decoded
	return nil
}
