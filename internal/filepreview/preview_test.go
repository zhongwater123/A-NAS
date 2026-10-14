package filepreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

type sourceStub struct {
	path, name string
	denied     bool
	opens      int
}

func (s *sourceStub) OpenContent(_ context.Context, _ accounts.User, _ string) (files.Content, error) {
	s.opens++
	if s.denied {
		return files.Content{}, files.ErrForbidden
	}
	f, err := os.Open(s.path)
	return files.Content{Name: s.name, Reader: f}, err
}

type workerStub struct {
	mediaworker.Worker
	data  []byte
	calls int
	at    float64
	size  int
}

func (w *workerStub) Image(_ context.Context, _ *os.File, size int) ([]byte, error) {
	w.calls++
	w.size = size
	return w.data, nil
}
func (w *workerStub) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	var p mediaworker.Probe
	p.Format.Duration = 200
	return p, nil
}
func (w *workerStub) Frame(_ context.Context, _ *os.File, at float64, size int) ([]byte, error) {
	w.at = at
	return w.Image(context.Background(), nil, size)
}
func fixture(t *testing.T, name string) (*Service, *sourceStub, *workerStub) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if name == "movie.mp4" {
		_ = jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	} else {
		_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	}
	source := &sourceStub{path: path, name: name}
	worker := &workerStub{data: data.Bytes()}
	return New(source, worker), source, worker
}
func TestPreviewRechecksAccessAndFileIdentityBeforeCacheHit(t *testing.T) {
	service, source, worker := fixture(t, "photo.HEIC")
	ctx := context.Background()
	actor := accounts.User{ID: "alice"}
	for i := 0; i < 2; i++ {
		result, err := service.Render(ctx, actor, "photo", 320)
		if err != nil || result.ContentType != "image/png" {
			t.Fatalf("render = %v, %v", result, err)
		}
	}
	if worker.calls != 1 || source.opens != 2 {
		t.Fatalf("decode/open calls = %d/%d", worker.calls, source.opens)
	}
	source.denied = true
	if _, err := service.Render(ctx, actor, "photo", 320); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("revoked access: %v", err)
	}
	source.denied = false
	replacement := source.path + ".replacement"
	if err := os.WriteFile(replacement, []byte("replaced"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source.path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Render(ctx, actor, "photo", 320); err != nil {
		t.Fatal(err)
	}
	if worker.calls != 2 {
		t.Fatal("same-size replacement reused stale preview")
	}
	if _, err := service.Render(ctx, accounts.User{ID: "bob"}, "photo", 320); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Render(ctx, actor, "photo", 1600); err != nil {
		t.Fatal(err)
	}
	if worker.calls != 4 || worker.size != 1600 {
		t.Fatal("user or size cache keys were conflated")
	}
	info, err := os.Stat(source.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.path, []byte("SMB edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source.path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Render(ctx, actor, "photo", 320); err != nil {
		t.Fatal(err)
	}
	if worker.calls != 5 {
		t.Fatal("same-size SMB edit with restored mtime reused stale preview")
	}
	if err := os.Remove(source.path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Render(ctx, actor, "photo", 320); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file: %v", err)
	}
}
func TestVideoPreviewUsesBoundedRepresentativeFrame(t *testing.T) {
	service, _, worker := fixture(t, "movie.mp4")
	result, err := service.Render(context.Background(), accounts.User{}, "video", 320)
	if err != nil || result.ContentType != "image/jpeg" || worker.at != 10 || worker.size != 320 {
		t.Fatalf("video=%v err=%v worker=%+v", result, err, worker)
	}
}
func TestPreviewRejectsBadInputAndCancelledQueue(t *testing.T) {
	service, source, worker := fixture(t, "photo.heif")
	ctx := context.Background()
	if _, err := service.Render(ctx, accounts.User{}, "photo", 99999); !errors.Is(err, mediaworker.ErrInvalid) {
		t.Fatal(err)
	}
	if source.opens != 0 {
		t.Fatal("invalid size opened source")
	}
	worker.data = []byte("not a PNG")
	if _, err := service.Render(ctx, accounts.User{}, "photo", 320); !errors.Is(err, mediaworker.ErrUnreadable) {
		t.Fatal(err)
	}
	source.name = "notes.txt"
	if _, err := service.Render(ctx, accounts.User{}, "photo", 320); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	source.name = "photo.heif"
	service.worker = nil
	if _, err := service.Render(ctx, accounts.User{}, "photo", 320); !errors.Is(err, mediaworker.ErrUnavailable) {
		t.Fatal(err)
	}
	service.gate <- struct{}{}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Render(cancelled, accounts.User{}, "photo", 320); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-service.gate
}

func TestPreviewCacheIsBounded(t *testing.T) {
	service, _, worker := fixture(t, "photo.heic")
	for i := 0; i < 70; i++ {
		if _, err := service.Render(context.Background(), accounts.User{}, fmt.Sprint(i), 320); err != nil {
			t.Fatal(err)
		}
	}
	if len(service.cache) != 64 || service.bytes > 32<<20 {
		t.Fatalf("cache=%d bytes=%d", len(service.cache), service.bytes)
	}
	calls := worker.calls
	if _, err := service.Render(context.Background(), accounts.User{}, "69", 320); err != nil {
		t.Fatal(err)
	}
	if worker.calls != calls {
		t.Fatal("most recent preview was evicted")
	}
	if _, err := service.Render(context.Background(), accounts.User{}, "0", 320); err != nil {
		t.Fatal(err)
	}
	if worker.calls != calls+1 {
		t.Fatal("oldest preview remained in cache")
	}
}
