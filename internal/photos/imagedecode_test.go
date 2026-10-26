package photos

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/imagetest"
)

func TestImageDecodeBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		model         color.Model
		width, height int
		want          error
	}{
		{"RGBA8 boundary", color.RGBAModel, 8192, 8192, nil},
		{"RGBA16 boundary", color.RGBA64Model, 8192, 4096, nil},
		{"NRGBA16 boundary", color.NRGBA64Model, 8192, 4096, nil},
		{"RGBA16 over", color.NRGBA64Model, 8192, 4097, ErrTooLarge},
		{"gray reserves resize raster", color.GrayModel, 8192, 8193, ErrTooLarge},
		{"gray16 reserves resize raster", color.Gray16Model, 8192, 8193, ErrTooLarge},
		{"palette", color.Palette{color.Black}, 8192, 8192, nil},
		{"JPEG", color.YCbCrModel, 8192, 8192, nil},
		{"JPEG CMYK", color.CMYKModel, 8192, 8193, ErrTooLarge},
		{"thin image", color.RGBAModel, 1 << 24, 1, ErrTooLarge},
		{"overflow", color.RGBAModel, math.MaxInt, math.MaxInt, ErrTooLarge},
		{"zero", color.RGBAModel, 0, 8, ErrUnsupportedType},
		{"negative", color.RGBAModel, 8, -1, ErrUnsupportedType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkImageDecodeBudget(image.Config{ColorModel: tc.model, Width: tc.width, Height: tc.height}); !errors.Is(err, tc.want) {
				t.Fatalf("budget error = %v, want %v", err, tc.want)
			}
		})
	}
}

func decodeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, 2, 2))
	img.SetNRGBA64(0, 0, color.NRGBA64{R: 0x1234, A: 0x8888})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func decodeTestFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestStoredAndSearchImagesRecheckDecodeBudget(t *testing.T) {
	// A valid PNG header alone is enough to reject a formerly accepted
	// original; none of the 512 MiB raster needs to exist in this test.
	header := decodeTestPNG(t)[:33]
	binary.BigEndian.PutUint32(header[16:20], 8192)
	binary.BigEndian.PutUint32(header[20:24], 8192)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	var service Service
	if _, _, _, err := service.renderPhoto(context.Background(), decodeTestFile(t, header), 1, 512, 82); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("stored image error = %v, want ErrTooLarge", err)
	}
	if _, _, err := renderSearchImage(context.Background(), header); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("search image error = %v, want ErrTooLarge", err)
	}
	if class := decodeFailure(ErrTooLarge); class != jobErrorUndecodable {
		t.Fatalf("oversized job class = %q; must fail permanently", class)
	}
}

func TestLocalImageDecodersShareSlotUntilRenderingFinishes(t *testing.T) {
	data := decodeTestPNG(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- withDecodedImage(context.Background(), bytes.NewReader(data), func(image.Image) error {
			close(started)
			<-release
			return nil
		})
	}()
	finished := false
	defer func() {
		close(release)
		if !finished {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-started:
	case err := <-done:
		finished = true
		t.Fatalf("initial decode failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("initial decode did not start")
	}
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	service := Service{root: root}
	objectID := strings.Repeat("a", 64)
	sheet := filepath.Join(rootPath, derivedPath(facesDerivation, objectID))
	if err := os.MkdirAll(filepath.Dir(sheet), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sheet, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		render func(context.Context) error
	}{
		{"thumbnail and view", func(ctx context.Context) error {
			_, _, _, err := service.renderPhoto(ctx, decodeTestFile(t, data), 1, 1600, 85)
			return err
		}},
		{"search image", func(ctx context.Context) error { _, _, err := renderSearchImage(ctx, data); return err }},
		{"avatar sheet", func(ctx context.Context) error {
			_, err := avatarSheet(ctx, decodeTestFile(t, data), []found{{}}, 1.6)
			return err
		}},
		{"avatar tile", func(ctx context.Context) error {
			_, err := service.avatarTile(ctx, facesDerivation, objectID, 0)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := tc.render(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("render error = %v; must wait for the shared slot until cancelled", err)
			}
		})
	}
}

func TestImageDecodeSlotSurvivesDecoderPanicAndRejectsCancelledWork(t *testing.T) {
	func() {
		defer func() {
			if got := recover(); got != imagetest.PanicMessage {
				t.Errorf("panic = %v", got)
			}
		}()
		_ = withDecodedImage(context.Background(), bytes.NewReader(imagetest.PanickingPicture()), func(image.Image) error { return nil })
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := withDecodedImage(ctx, bytes.NewReader(decodeTestPNG(t)), func(image.Image) error { return nil }); err != nil {
		t.Fatalf("slot was not released after panic: %v", err)
	}
	cancel()
	if err := withDecodedImage(ctx, bytes.NewReader(decodeTestPNG(t)), func(image.Image) error { t.Error("cancelled work ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decode error = %v", err)
	}
}
