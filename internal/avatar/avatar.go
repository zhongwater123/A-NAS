// Package avatar turns a picture a user chose into the small square JPEG that
// every page shows as their avatar.
package avatar

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

// Size is the edge of a stored avatar in pixels: twice the largest place a
// page shows one, so it stays sharp on high-density screens.
const Size = 256

// ContentType is the type of every stored avatar.
const ContentType = "image/jpeg"

// MaxUploadBytes bounds the picture a user may send.
const MaxUploadBytes = 12 << 20

// maxPixels bounds decoded dimensions before any allocation: a 24-megapixel
// photo decodes to about 100 MB. Pages send at most 512 px.
const maxPixels = 25_000_000

var (
	ErrUnsupported = errors.New("the picture is not a JPEG, PNG, GIF or WebP image")
	ErrTooLarge    = errors.New("the picture is too large")
)

// Normalize decodes picture, turns it upright, crops the centre square,
// scales it to at most Size pixels and flattens transparency onto white. The
// result is a JPEG of a few dozen kilobytes, whatever was uploaded, so
// showing avatars never loads a user's original photo.
func Normalize(picture []byte) ([]byte, error) {
	if len(picture) > MaxUploadBytes {
		return nil, ErrTooLarge
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(picture))
	if err != nil {
		return nil, ErrUnsupported
	}
	switch format {
	case "jpeg", "png", "gif", "webp":
	default:
		return nil, ErrUnsupported
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, ErrUnsupported
	}
	if config.Width*config.Height > maxPixels {
		return nil, ErrTooLarge
	}
	source, err := imaging.Decode(bytes.NewReader(picture), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	bounds := source.Bounds()
	edge := min(Size, bounds.Dx(), bounds.Dy())
	square := imaging.Fill(source, edge, edge, imaging.Center, imaging.Lanczos)
	flat := imaging.Overlay(imaging.New(edge, edge, color.White), square, image.Point{}, 1)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, flat, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
