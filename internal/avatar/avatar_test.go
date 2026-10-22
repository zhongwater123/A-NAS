package avatar_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/avatar"
	"github.com/zhongwater123/A-NAS/internal/imagetest"
)

func encodePNG(t *testing.T, picture image.Image) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	picture, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("result is not a JPEG: %v", err)
	}
	return picture
}

func TestPhotosBecomeSmallCentredSquares(t *testing.T) {
	// A wide photo: red on the left and right thirds, blue in the middle.
	wide := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
	for x := range 3000 {
		for y := range 1000 {
			fill := color.RGBA{R: 220, A: 255}
			if x >= 1000 && x < 2000 {
				fill = color.RGBA{B: 220, A: 255}
			}
			wide.Set(x, y, fill)
		}
	}
	original := encodePNG(t, wide)
	normalized, err := avatar.Normalize(original)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	result := decodeJPEG(t, normalized)
	if got := result.Bounds(); got.Dx() != avatar.Size || got.Dy() != avatar.Size {
		t.Fatalf("size = %v, want %dx%d", got, avatar.Size, avatar.Size)
	}
	// Only the blue centre square is kept.
	for _, point := range []image.Point{{4, 4}, {128, 128}, {251, 251}} {
		r, _, b, _ := result.At(point.X, point.Y).RGBA()
		if b>>8 < 180 || r>>8 > 60 {
			t.Fatalf("pixel %v = r%d b%d, want the blue centre", point, r>>8, b>>8)
		}
	}
	if len(normalized) > 40<<10 || len(normalized) >= len(original) {
		t.Fatalf("avatar is %d bytes from a %d byte original", len(normalized), len(original))
	}
}

func TestTransparencyTurnsWhiteAndSmallPicturesAreNotEnlarged(t *testing.T) {
	transparent := image.NewNRGBA(image.Rect(0, 0, 64, 80))
	normalized, err := avatar.Normalize(encodePNG(t, transparent))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	result := decodeJPEG(t, normalized)
	if got := result.Bounds(); got.Dx() != 64 || got.Dy() != 64 {
		t.Fatalf("size = %v, want 64x64", got)
	}
	if r, g, b, _ := result.At(32, 32).RGBA(); r>>8 < 245 || g>>8 < 245 || b>>8 < 245 {
		t.Fatalf("transparent pixel became %d,%d,%d, want white", r>>8, g>>8, b>>8)
	}
}

func TestOnlyReasonablePicturesAreAccepted(t *testing.T) {
	// A PNG header that claims 20000x20000 pixels is refused before decoding.
	huge := encodePNG(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	binary.BigEndian.PutUint32(huge[16:20], 20000)
	binary.BigEndian.PutUint32(huge[20:24], 20000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for name, test := range map[string]struct {
		picture []byte
		want    error
	}{
		"text":       {[]byte("not a picture"), avatar.ErrUnsupported},
		"svg":        {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), avatar.ErrUnsupported},
		"empty":      {nil, avatar.ErrUnsupported},
		"dimensions": {huge, avatar.ErrTooLarge},
		"bytes":      {make([]byte, avatar.MaxUploadBytes+1), avatar.ErrTooLarge},
		"decoder":    {imagetest.PanickingPicture(), avatar.ErrUnsupported},
	} {
		if _, err := avatar.Normalize(test.picture); !errors.Is(err, test.want) {
			t.Errorf("%s: Normalize() error = %v, want %v", name, err, test.want)
		}
	}
}
