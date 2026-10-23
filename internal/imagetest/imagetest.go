// Package imagetest gives tests of in-process image decoding a picture whose
// decoder panics, as a decoder with a bug may on a malformed picture.
package imagetest

import (
	"image"
	"image/color"
	"io"
	"slices"
	"sync"
)

var panicking = []byte("\x00anas-panicking-picture")

var register = sync.OnceFunc(func() {
	// The format calls itself webp, which every caller accepts, and its
	// header is a small picture, so callers get past their checks to Decode.
	image.RegisterFormat("webp", string(panicking[:8]),
		func(io.Reader) (image.Image, error) { panic("imagetest: decoder bug") },
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 64, Height: 48}, nil
		})
})

// PanickingPicture returns a picture whose header reads as a 64x48 WebP and
// whose decoder panics.
func PanickingPicture() []byte {
	register()
	return slices.Clone(panicking)
}
