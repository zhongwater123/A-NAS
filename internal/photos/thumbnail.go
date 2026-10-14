package photos

import (
	"bytes"
	"image"
	"image/color"
	"io"

	"github.com/disintegration/imaging"
)

// A grid thumbnail's longer side is at most thumbnailEdge.
const (
	thumbnailEdge    = 512
	thumbnailQuality = 82
)

// renderThumbnail scales an original to fit thumbnailEdge, turns it upright
// according to its EXIF orientation and encodes a JPEG on a white background.
func renderThumbnail(r io.Reader, orientation int) ([]byte, int, int, error) {
	return renderUpright(r, orientation, thumbnailEdge, thumbnailQuality)
}

// renderUpright decodes an original and renders it with uprightJPEG. The
// original was already bounded by maxImagePixels at import.
func renderUpright(r io.Reader, orientation, edge, quality int) ([]byte, int, int, error) {
	src, err := imaging.Decode(r)
	if err != nil {
		return nil, 0, 0, err
	}
	return uprightJPEG(src, orientation, edge, quality)
}

// uprightJPEG scales an image to fit edge, turns it upright and encodes a
// JPEG of the given quality on a white background. Scaling happens before
// the orientation transform so that only the small image is copied; images
// already within edge keep their size.
func uprightJPEG(src image.Image, orientation, edge, quality int) ([]byte, int, int, error) {
	scaled := orient(imaging.Fit(src, edge, edge, imaging.Lanczos), orientation)
	bounds := scaled.Bounds()
	flattened := imaging.Overlay(imaging.New(bounds.Dx(), bounds.Dy(), color.White), scaled, image.Point{}, 1)
	var buffer bytes.Buffer
	if err := imaging.Encode(&buffer, flattened, imaging.JPEG, imaging.JPEGQuality(quality)); err != nil {
		return nil, 0, 0, err
	}
	return buffer.Bytes(), bounds.Dx(), bounds.Dy(), nil
}

// orient applies an EXIF orientation; imaging rotates counter-clockwise.
func orient(img *image.NRGBA, orientation int) *image.NRGBA {
	switch orientation {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	default:
		return img
	}
}
