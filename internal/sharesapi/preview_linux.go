//go:build linux

package sharesapi

import (
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The decoder receives a sealed anonymous descriptor it cannot write, never
// a pathname into managed content or a writable staging file on the system
// disk.
func anonymousInput(reader io.Reader) (*os.File, error) {
	return unixmsg.WriteSealedFile("a-nas-share-preview", func(w io.Writer) error {
		n, err := io.Copy(w, reader)
		if err == nil && n > filepreview.MaxHEIFSource {
			return shares.ErrInvalid
		}
		return err
	})
}
