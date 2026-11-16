// Package imagerender decodes a user's image under a memory budget and
// re-encodes it as an upright JPEG without the original's metadata. The photo
// service and the photo scoring module both render pictures they did not
// create, so both bound the decode the same way (OVERVIEW invariant 18).
package imagerender

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"time"

	"github.com/disintegration/imaging"
	"github.com/evanoberholster/imagemeta"
	"golang.org/x/sync/semaphore"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

var (
	// ErrUnsupported reports a picture without usable dimensions or colours.
	ErrUnsupported = errors.New("unsupported image")
	// ErrTooLarge reports a picture whose decoded pixels exceed the budget.
	ErrTooLarge = errors.New("image exceeds the decode budget")
)

const (
	// Leave room for resizing, the Catalog and search indexes below the
	// photo service's 1 GiB Go soft limit and 2 GiB systemd hard limit.
	maxDecodedImageBytes = 256 << 20
	// A thin PNG can have few pixels but enormous scanlines and Lanczos
	// weight tables. Match JPEG's maximum side length for local decoding.
	maxDecodeDimension = 65535
)

// All local decoders in a process share this slot, including independent
// services in development. Keep it until resizing and encoding end, not just
// until Decode returns, while the original pixels are still live.
var decodeSlot = semaphore.NewWeighted(1)

// CheckDecodeBudget rejects a picture before decoding when its pixels would
// not fit the budget.
func CheckDecodeBudget(config image.Config) error {
	if config.Width <= 0 || config.Height <= 0 || config.ColorModel == nil {
		return ErrUnsupported
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

// WithDecoded checks the budget, decodes r and hands the pixels to use while
// holding the process's decode slot. use must finish with the pixels before it
// returns. A decoder panic propagates to the caller, which logs it with
// decodepanic; the slot is released either way.
func WithDecoded(ctx context.Context, r io.ReadSeeker, use func(image.Image) error) error {
	if err := decodeSlot.Acquire(ctx, 1); err != nil {
		return err
	}
	defer decodeSlot.Release(1)
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	config, _, err := image.DecodeConfig(r)
	if err != nil {
		return err
	}
	if err := CheckDecodeBudget(config); err != nil {
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

// UprightJPEG scales an image to fit edge, turns it upright and encodes a
// JPEG of the given quality on a white background. Scaling happens before
// the orientation transform so that only the small image is copied; images
// already within edge keep their size. The standard library's encoder writes
// no EXIF, XMP or ICC segments, so none of the original's metadata survives.
func UprightJPEG(src image.Image, orientation, edge, quality int) ([]byte, int, int, error) {
	scaled := Orient(imaging.Fit(src, edge, edge, imaging.Lanczos), orientation)
	bounds := scaled.Bounds()
	flattened := imaging.Overlay(imaging.New(bounds.Dx(), bounds.Dy(), color.White), scaled, image.Point{}, 1)
	var buffer bytes.Buffer
	if err := imaging.Encode(&buffer, flattened, imaging.JPEG, imaging.JPEGQuality(quality)); err != nil {
		return nil, 0, 0, err
	}
	return buffer.Bytes(), bounds.Dx(), bounds.Dy(), nil
}

// FullJPEG encodes an image at its own size as a JPEG of the given quality,
// in its stored orientation, which callers apply when they show it. Like
// UprightJPEG it writes none of the original's metadata. An opaque image is
// encoded as it is, without the copies scaling and turning would make, so
// that a large photo fits a small memory limit; one with transparency is
// first flattened on white.
func FullJPEG(src image.Image, quality int) ([]byte, error) {
	if opaque, ok := src.(interface{ Opaque() bool }); !ok || !opaque.Opaque() {
		bounds := src.Bounds()
		src = imaging.Overlay(imaging.New(bounds.Dx(), bounds.Dy(), color.White), src, image.Point{}, 1)
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, src, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Orient applies an EXIF orientation; imaging rotates counter-clockwise.
func Orient(img *image.NRGBA, orientation int) *image.NRGBA {
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

// ReadEXIF returns the orientation and capture time of a picture. Missing or
// malformed EXIF yields orientation 1 and a zero time, including when the
// parser panics on hostile input; site names the caller in that panic's log
// line. A capture time recorded without an offset is the camera's wall clock
// and is read in location.
func ReadEXIF(r io.ReadSeeker, location *time.Location, site string) (orientation int, takenAt time.Time) {
	orientation = 1
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log(site, recovered)
			orientation, takenAt = 1, time.Time{}
		}
	}()
	parsed, err := imagemeta.Decode(r)
	if err != nil {
		return 1, time.Time{}
	}
	if value := int(parsed.IFD0.Orientation); value >= 1 && value <= 8 {
		orientation = value
	}
	takenAt = parsed.ExifIFD.GetDateTimeOriginal()
	if takenAt.IsZero() || takenAt.Year() < 1900 {
		return orientation, time.Time{}
	}
	if parsed.ExifIFD.OffsetTimeOriginal == nil {
		takenAt = time.Date(takenAt.Year(), takenAt.Month(), takenAt.Day(),
			takenAt.Hour(), takenAt.Minute(), takenAt.Second(), takenAt.Nanosecond(), location)
	}
	return orientation, takenAt
}
