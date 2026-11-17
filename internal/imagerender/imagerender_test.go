package imagerender

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/disintegration/imaging"

	"github.com/zhongwater123/A-NAS/internal/imagetest"
)

// imaging has no release without CVE-2023-36308: a pixel past the end of a
// paletted picture's palette panics in imaging's own goroutines and ends the
// process. Decode refuses such a picture whatever decoder made it, and
// WithDecoded decodes with it.
func TestAPixelOutsideThePaletteNeverReachesImaging(t *testing.T) {
	err := WithDecoded(context.Background(), bytes.NewReader(imagetest.OutsidePalettePicture()), func(image.Image) error {
		t.Error("a pixel outside the palette was decoded and would reach imaging")
		return nil
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("WithDecoded() error = %v, want ErrUnsupported", err)
	}
	// With AutoOrientation, imaging turns a picture before Decode checks it,
	// but only a JPEG, and a JPEG is never paletted.
	if _, err := Decode(bytes.NewReader(imagetest.OutsidePalettePicture()), imaging.AutoOrientation(true)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Decode() error = %v, want ErrUnsupported", err)
	}

	// A paletted picture whose pixels are all in its palette decodes.
	var inside bytes.Buffer
	if err := png.Encode(&inside, &image.Paletted{Pix: []uint8{1, 0}, Stride: 2, Rect: image.Rect(0, 0, 2, 1),
		Palette: color.Palette{color.Black, color.White}}); err != nil {
		t.Fatal(err)
	}
	if src, err := Decode(&inside); err != nil {
		t.Errorf("Decode() of a paletted PNG = %v", err)
	} else if _, ok := src.(*image.Paletted); !ok {
		t.Errorf("Decode() of a paletted PNG = %T, want *image.Paletted", src)
	}
}

func TestOutsidePalette(t *testing.T) {
	twoColours := color.Palette{color.Black, color.White}
	full := make(color.Palette, 256)
	for i := range full {
		full[i] = color.Gray{Y: uint8(i)}
	}
	paletted := func(palette color.Palette, pixels ...uint8) *image.Paletted {
		return &image.Paletted{Pix: pixels, Stride: len(pixels), Rect: image.Rect(0, 0, len(pixels), 1), Palette: palette}
	}
	for _, picture := range []struct {
		name    string
		picture image.Image
		want    bool
	}{
		{"the last colour", paletted(twoColours, 1, 0), false},
		{"one past the last colour", paletted(twoColours, 0, 2), true},
		{"the advisory's pixel", paletted(twoColours, 1, 70), true},
		{"no palette", paletted(nil, 0), true},
		{"every index of a full palette", paletted(full, 0, 255), false},
		{"no pixels", paletted(nil), false},
		{"not paletted", image.NewNRGBA(image.Rect(0, 0, 2, 1)), false},
	} {
		if got := outsidePalette(picture.picture); got != picture.want {
			t.Errorf("%s: outsidePalette() = %v, want %v", picture.name, got, picture.want)
		}
	}
}
