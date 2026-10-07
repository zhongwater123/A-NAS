package photos_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

var (
	alice = photos.Principal{UserID: "user:alice"}
	bob   = photos.Principal{UserID: "user:bob"}
	admin = photos.Principal{UserID: "user:admin", Admin: true}
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func openService(t *testing.T, root string, now func() time.Time) *photos.Service {
	t.Helper()
	service, err := photos.Open(root, photos.Options{Now: now, DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func newService(t *testing.T) (*photos.Service, *clock, string) {
	t.Helper()
	c := newClock()
	root := filepath.Join(t.TempDir(), "photos")
	return openService(t, root, c.Now), c, root
}

// pngBytes returns a distinct small PNG for each shade.
func pngBytes(t *testing.T, shade uint8) []byte {
	t.Helper()
	return encodePNG(shade)
}

func encodePNG(shade uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for x := range 4 {
		for y := range 3 {
			img.Set(x, y, color.RGBA{R: shade, G: 0x40, B: 0x80, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buffer.Bytes()
}

func libraries(t *testing.T, service *photos.Service, p photos.Principal) (private, shared photos.Library) {
	t.Helper()
	all, err := service.Libraries(context.Background(), p)
	if err != nil {
		t.Fatalf("Libraries(%s) error = %v", p.UserID, err)
	}
	for _, lib := range all {
		switch {
		case lib.Kind == photos.LibraryKindShared:
			shared = lib
		case lib.OwnerUserID == p.UserID:
			private = lib
		}
	}
	if private.ID == "" || shared.ID == "" {
		t.Fatalf("Libraries(%s) = %+v, want own private and shared", p.UserID, all)
	}
	return private, shared
}

func importPhoto(t *testing.T, service *photos.Service, p photos.Principal, libraryID, directoryID, name string, content []byte) photos.Asset {
	t.Helper()
	asset, err := service.Import(context.Background(), p, photos.ImportRequest{
		LibraryID: libraryID, DirectoryID: directoryID, Name: name, Content: bytes.NewReader(content),
	})
	if err != nil {
		t.Fatalf("Import(%s, %s) error = %v", p.UserID, name, err)
	}
	return asset
}

func objectFiles(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "objects", "*", "*", "*"))
	if err != nil {
		t.Fatalf("glob objects: %v", err)
	}
	return matches
}
