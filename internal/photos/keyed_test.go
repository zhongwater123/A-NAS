package photos_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// heldReader gives its content only once released, like an upload whose
// bytes are still on their way.
type heldReader struct {
	content io.Reader
	reading chan struct{}
	release chan struct{}
}

func (r *heldReader) Read(buffer []byte) (int, error) {
	if r.reading != nil {
		close(r.reading)
		r.reading = nil
		<-r.release
	}
	return r.content.Read(buffer)
}

func TestImportWithAnUploadKeyMakesOnePhotoForItsRetries(t *testing.T) {
	ctx := context.Background()
	service := openService(t, filepath.Join(t.TempDir(), "photos"), newClock().Now)
	private, shared := libraries(t, service, alice)
	original := jpegBytes(t)
	send := func(libraryID, key string, content io.Reader) (photos.Asset, error) {
		return service.Import(ctx, alice, photos.ImportRequest{LibraryID: libraryID, Name: "IMG_0001.JPG", Content: content, UploadKey: key})
	}

	// The browser gives up on a slow first try and sends the photo again
	// while the NAS still stores the first.
	reading := make(chan struct{})
	held := &heldReader{content: bytes.NewReader(original), reading: reading, release: make(chan struct{})}
	first := make(chan photos.Asset, 1)
	go func() {
		asset, err := send(private.ID, "upload-1", held)
		if err != nil {
			t.Errorf("first try: %v", err)
		}
		first <- asset
	}()
	<-reading
	retried := make(chan photos.Asset, 1)
	go func() {
		asset, err := send(private.ID, "upload-1", bytes.NewReader(original))
		if err != nil {
			t.Errorf("retry: %v", err)
		}
		retried <- asset
	}()
	close(held.release)
	made, again := <-first, <-retried
	if made.ID == "" || again.ID != made.ID {
		t.Fatalf("retry made %q, first try %q", again.ID, made.ID)
	}
	// A later retry, its answer lost too, still gets the same photo.
	if later, err := send(private.ID, "upload-1", bytes.NewReader(original)); err != nil || later.ID != made.ID {
		t.Fatalf("later retry = %q, %v", later.ID, err)
	}
	page, err := service.Timeline(ctx, alice, private.ID, "", 50)
	if err != nil || len(page.Assets) != 1 {
		t.Fatalf("timeline = %d photos, %v; want 1", len(page.Assets), err)
	}

	// The same key elsewhere, or without a key, is another upload.
	if other, err := send(shared.ID, "upload-1", bytes.NewReader(original)); err != nil || other.ID == made.ID {
		t.Fatalf("same key in another library = %q, %v", other.ID, err)
	}
	// A try that failed leaves the key free for the next one.
	if _, err := send(private.ID, "upload-2", bytes.NewReader([]byte("not a photo"))); err == nil {
		t.Fatal("a non-image was imported")
	}
	if next, err := send(private.ID, "upload-2", bytes.NewReader(original)); err != nil || next.ID == made.ID {
		t.Fatalf("retry after a failed try = %q, %v", next.ID, err)
	}
	// A photo thrown away since is made again.
	if _, err := service.Trash(ctx, alice, made.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := send(private.ID, "upload-1", bytes.NewReader(original)); err != nil || again.ID == made.ID {
		t.Fatalf("retry after trashing = %q, %v", again.ID, err)
	}
}
