package photos_test

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"io"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// What a member's AI assistant hands its model to look at (ADR 0024
// decision 6): the photo upright, at most 1600 pixels on its long edge, and
// nothing of the original's metadata.
func TestAnAssistantLooksAtAPhotoOnlyThroughItsView(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	alicePrivate, shared := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	// Taken sideways: 2400×1200 pixels that EXIF turns upright to 1200×2400.
	own := importPhoto(t, service, alice, alicePrivate.ID, "", "IMG_2031.JPG", exifJPEG(t, 2400, 1200, 6, "2024:08:18 07:30:00", "+08:00"))
	family := importPhoto(t, service, bob, shared.ID, "", "family.png", pngBytes(t, 7))
	bobs := importPhoto(t, service, bob, bobPrivate.ID, "", "bob.png", pngBytes(t, 8))
	assistant := photos.Principal{UserID: alice.UserID, Username: "alice", Assistant: true}

	view, err := service.View(ctx, assistant, own.ID)
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	data, err := io.ReadAll(view.Reader)
	_ = view.Reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the view is not a JPEG: %v", err)
	}
	if config.Width != 800 || config.Height != 1600 {
		t.Fatalf("view = %d×%d, want the upright photo scaled to 800×1600", config.Width, config.Height)
	}
	if bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("2024:08:18")) {
		t.Fatalf("the view carries the original's metadata")
	}
	if view.MediaType != "image/jpeg" || view.Name != "IMG_2031.jpg" || int64(len(data)) != view.SizeBytes {
		t.Fatalf("view content = %q %q %d bytes, read %d", view.MediaType, view.Name, view.SizeBytes, len(data))
	}

	// The shared library's photos too; small ones keep their size.
	small, err := service.View(ctx, assistant, family.ID)
	if err != nil {
		t.Fatalf("View() of a shared photo error = %v", err)
	}
	_ = small.Reader.Close()
	// Another member's private photo stays out of sight.
	if _, err := service.View(ctx, assistant, bobs.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("View() of bob's photo error = %v, want ErrNotFound", err)
	}
	// The original and the thumbnail are still not the assistant's.
	if _, err := service.Open(ctx, assistant, own.ID); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("Open() error = %v, want ErrForbidden", err)
	}
}
