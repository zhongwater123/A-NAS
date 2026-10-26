package photos

import (
	"context"
	"image"
	"image/color"
	"io"

	"github.com/disintegration/imaging"
	"golang.org/x/sync/semaphore"
)

const (
	// Leave room for resizing, the Catalog and search indexes below the
	// photo service's 1 GiB Go soft limit and 2 GiB systemd hard limit.
	maxDecodedImageBytes = 256 << 20
	// A thin PNG can have few pixels but enormous scanlines and Lanczos
	// weight tables. Match JPEG's maximum side length for local decoding.
	maxDecodeDimension = 65535
)

// All local decoders share this process-wide slot, including independent
// Service instances in development. Keep it until resizing and encoding end,
// not just until Decode returns, while the original pixels are still live.
var imageDecodeSlot = semaphore.NewWeighted(1)

func checkImageDecodeBudget(config image.Config) error {
	if config.Width <= 0 || config.Height <= 0 || config.ColorModel == nil {
		return ErrUnsupportedType
	}
	if config.Width > maxDecodeDimension || config.Height > maxDecodeDimension {
		return ErrTooLarge
	}
	// PNG's 16-bit RGB/RGBA decode to RGBA64/NRGBA64. Reserve at least
	// four bytes per pixel even for grayscale, palette and JPEG YCbCr:
	// imaging's intermediate raster is NRGBA regardless of the source.
	bytesPerPixel := int64(4)
	switch config.ColorModel {
	case color.RGBA64Model, color.NRGBA64Model:
		bytesPerPixel = 8
	}
	if int64(config.Width) > maxDecodedImageBytes/bytesPerPixel/int64(config.Height) {
		return ErrTooLarge
	}
	return nil
}

// withDecodedImage bounds both new and already-stored images before Decode.
// The callback must finish using the pixels before it returns. Media Worker
// calls happen outside this slot; their small JPEG results enter it here.
func withDecodedImage(ctx context.Context, r io.ReadSeeker, use func(image.Image) error) error {
	if err := imageDecodeSlot.Acquire(ctx, 1); err != nil {
		return err
	}
	defer imageDecodeSlot.Release(1)
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	config, _, err := image.DecodeConfig(r)
	if err != nil {
		return err
	}
	if err := checkImageDecodeBudget(config); err != nil {
		return err
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	src, err := imaging.Decode(r)
	if err != nil {
		return err
	}
	return use(src)
}
