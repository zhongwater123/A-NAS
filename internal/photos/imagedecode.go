package photos

import (
	"context"
	"errors"
	"image"
	"io"

	"github.com/zhongwater123/A-NAS/internal/imagerender"
)

// checkImageDecodeBudget and withDecodedImage bound both new and already
// stored images before Decode (internal/imagerender), reporting the budget in
// this package's errors. FFmpeg Worker calls happen outside the decode slot;
// their small JPEG results enter it here.
func checkImageDecodeBudget(config image.Config) error {
	return decodeError(imagerender.CheckDecodeBudget(config))
}

func withDecodedImage(ctx context.Context, r io.ReadSeeker, use func(image.Image) error) error {
	return decodeError(imagerender.WithDecoded(ctx, r, use))
}

func decodeError(err error) error {
	switch {
	case errors.Is(err, imagerender.ErrTooLarge):
		return ErrTooLarge
	case errors.Is(err, imagerender.ErrUnsupported):
		return ErrUnsupportedType
	}
	return err
}
