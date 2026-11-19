package photos

import (
	"context"
	"image"
	"io"

	"github.com/zhongwater123/A-NAS/internal/imagerender"
)

// A grid thumbnail's longer side is at most thumbnailEdge.
const (
	thumbnailEdge    = 512
	thumbnailQuality = 82
)

// renderUpright scales an original to fit edge, turns it upright according to
// its EXIF orientation and encodes a JPEG on a white background. It rechecks
// the original's memory budget, then holds the shared decode slot until its
// pixels have been resized and encoded.
func renderUpright(ctx context.Context, r io.ReadSeeker, orientation, edge, quality int) (data []byte, width, height int, err error) {
	err = withDecodedImage(ctx, r, func(src image.Image) error {
		data, width, height, err = uprightJPEG(src, orientation, edge, quality)
		return err
	})
	return
}

// uprightJPEG is imagerender's; see there.
var uprightJPEG = imagerender.UprightJPEG
