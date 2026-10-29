package personalization_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/imagetest"
	"github.com/zhongwater123/A-NAS/internal/personalization"
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

// sidewaysJPEG is a wide JPEG whose EXIF says to turn it a quarter clockwise,
// as cameras store portrait photos.
func sidewaysJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	exif := []byte{
		0xff, 0xe1, 0x00, 0x22, 'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0x00, 0x2a, 0x00, 0x00, 0x00, 0x08,
		0x00, 0x01, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x06, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	data := encoded.Bytes()
	return append(append(append([]byte{}, data[:2]...), exif...), data[2:]...)
}

func TestWallpapersAreUprightAtMost4KAndOpaque(t *testing.T) {
	// A banner wider than 4K whose right half is transparent.
	banner := image.NewNRGBA(image.Rect(0, 0, 6000, 1000))
	for x := range 3000 {
		for y := range 1000 {
			banner.Set(x, y, color.NRGBA{R: 230, G: 200, B: 40, A: 255})
		}
	}
	data, width, height, err := personalization.NormalizeWallpaper(encodePNG(t, banner))
	if err != nil {
		t.Fatalf("NormalizeWallpaper() error = %v", err)
	}
	result := decodeJPEG(t, data)
	if width != 3840 || height != 640 || result.Bounds().Dx() != 3840 || result.Bounds().Dy() != 640 {
		t.Fatalf("size = %dx%d (%v), want 3840x640", width, height, result.Bounds())
	}
	if r, g, b, _ := result.At(3500, 320).RGBA(); r>>8 > 20 || g>>8 > 35 || b>>8 < 25 || b>>8 > 60 {
		t.Fatalf("transparent pixel became %d,%d,%d, want the default wallpaper's navy", r>>8, g>>8, b>>8)
	}

	// Small pictures keep their size, so a tiled pattern stays as drawn.
	if _, width, height, err := personalization.NormalizeWallpaper(encodePNG(t, image.NewGray(image.Rect(0, 0, 64, 48)))); err != nil || width != 64 || height != 48 {
		t.Fatalf("small picture = %dx%d, %v", width, height, err)
	}

	// A portrait photo stored sideways is turned upright.
	if _, width, height, err := personalization.NormalizeWallpaper(sidewaysJPEG(t, 40, 20)); err != nil || width != 20 || height != 40 {
		t.Fatalf("sideways photo = %dx%d, %v; want 20x40", width, height, err)
	}
}

func TestOnlyReasonableWallpapersAreAccepted(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	// A PNG header that claims 20000x20000 pixels is refused before decoding.
	huge := encodePNG(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	binary.BigEndian.PutUint32(huge[16:20], 20000)
	binary.BigEndian.PutUint32(huge[20:24], 20000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for name, test := range map[string]struct {
		picture []byte
		want    error
	}{
		"text":       {[]byte("not a picture"), personalization.ErrUnsupportedImage},
		"svg":        {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), personalization.ErrUnsupportedImage},
		"empty":      {nil, personalization.ErrUnsupportedImage},
		"dimensions": {huge, personalization.ErrImageTooLarge},
		"bytes":      {make([]byte, personalization.MaxWallpaperUploadBytes+1), personalization.ErrImageTooLarge},
		"decoder":    {imagetest.PanickingPicture(), personalization.ErrUnsupportedImage},
	} {
		if _, _, _, err := personalization.NormalizeWallpaper(test.picture); !errors.Is(err, test.want) {
			t.Errorf("%s: NormalizeWallpaper() error = %v, want %v", name, err, test.want)
		}
	}
	// The decoder's panic is logged; pictures refused before or by the
	// decoder are not.
	imagetest.RequirePanicLogged(t, panics(), "wallpaper")
}

func TestTheWallpaperIsKeptUntilTheDefaultIsRestored(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "personalization.db")
	store, err := personalization.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if wallpaper, err := store.Wallpaper(ctx); err != nil || wallpaper.Style != "fill" || wallpaper.Image != nil {
		t.Fatalf("default = %#v, %v", wallpaper, err)
	}
	if _, err := store.WallpaperPicture(ctx); !errors.Is(err, personalization.ErrNoWallpaperImage) {
		t.Fatalf("default picture error = %v", err)
	}
	// The default wallpaper can be drawn in another style too.
	if wallpaper, err := store.SetWallpaperStyle(ctx, "tile"); err != nil || wallpaper.Style != "tile" || wallpaper.Image != nil {
		t.Fatalf("tiled default = %#v, %v", wallpaper, err)
	}
	if _, err := store.SetWallpaperStyle(ctx, "zoom"); !errors.Is(err, personalization.ErrInvalidStyle) {
		t.Fatalf("unknown style error = %v", err)
	}

	data, width, height, err := personalization.NormalizeWallpaper(encodePNG(t, image.NewGray(image.Rect(0, 0, 400, 300))))
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.SetWallpaperImage(ctx, data, width, height)
	if err != nil {
		t.Fatal(err)
	}
	if set.Style != "tile" || set.Image == nil || len(set.Image.Version) != 16 || set.Image.Width != 400 || set.Image.Height != 300 {
		t.Fatalf("after setting a picture = %#v", set)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := personalization.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	picture, err := reopened.WallpaperPicture(ctx)
	if err != nil || !bytes.Equal(picture.Data, data) || picture.Version != set.Image.Version || picture.UpdatedAt.IsZero() {
		t.Fatalf("stored picture = %d bytes, version %q, at %v, %v", len(picture.Data), picture.Version, picture.UpdatedAt, err)
	}
	reset, err := reopened.ResetWallpaper(ctx)
	if err != nil || reset.Style != "fill" || reset.Image != nil {
		t.Fatalf("after restoring the default = %#v, %v", reset, err)
	}
	if _, err := reopened.WallpaperPicture(ctx); !errors.Is(err, personalization.ErrNoWallpaperImage) {
		t.Fatalf("picture after restoring the default: %v", err)
	}
	// The screen's sleep time is a separate setting and stays.
	if display, err := reopened.LocalDisplay(ctx); err != nil || display.SleepAfterMinutes != 30 {
		t.Fatalf("local display = %#v, %v", display, err)
	}
}
