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
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

type sourceStub struct {
	mu         sync.Mutex
	path, name string
	denied     bool
	opens      int
	opened     chan struct{}
}

func (s *sourceStub) OpenContent(_ context.Context, _ accounts.User, _ string) (files.Content, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	if s.opened != nil {
		select {
		case s.opened <- struct{}{}:
		default:
		}
	}
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
	for range 2 {
		result, err := service.Render(ctx, actor, "photo", 320)
		if err != nil || result.ContentType != "image/png" {
			t.Fatalf("render = %v, %v", result, err)
		}
	}
	if worker.calls != 1 || source.opens != 3 {
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
	for i := range 70 {
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

func TestDirectoryRevisitDoesNotDecodeAgain(t *testing.T) {
	_, source, worker := fixture(t, "movie.mp4")
	service, err := NewWithCache(source, worker, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for i := range 70 {
			if _, err := service.Render(context.Background(), accounts.User{}, fmt.Sprint(i), 320); err != nil {
				t.Fatal(err)
			}
		}
	}
	if worker.calls != 70 {
		t.Fatalf("two visits decoded %d videos; want 70", worker.calls)
	}
}

func TestCachedPreviewDoesNotWaitForDecoder(t *testing.T) {
	service, _, _ := fixture(t, "movie.mp4")
	if _, err := service.Render(context.Background(), accounts.User{}, "warm", 320); err != nil {
		t.Fatal(err)
	}
	service.gate <- struct{}{}
	defer func() { <-service.gate }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := service.Render(ctx, accounts.User{}, "warm", 320); err != nil {
		t.Fatalf("cached preview waited behind decode: %v", err)
	}
}

func TestPersistentPreviewSurvivesRestartAndRevalidatesSource(t *testing.T) {
	_, source, worker := fixture(t, "movie.mp4")
	dir := t.TempDir()
	actor := accounts.User{ID: "alice"}
	open := func() *Service {
		s, err := NewWithCache(source, worker, dir)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	original, err := open().Render(context.Background(), actor, "movie", 320)
	if err != nil {
		t.Fatal(err)
	}
	restarted := open()
	result, err := restarted.Render(context.Background(), actor, "movie", 320)
	if err != nil || worker.calls != 1 || !bytes.Equal(original.Data, result.Data) || original.ETag != result.ETag {
		t.Fatalf("restart: calls=%d err=%v", worker.calls, err)
	}
	source.denied = true
	if _, err := open().Render(context.Background(), actor, "movie", 320); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("disk cache bypassed access: %v", err)
	}
	source.denied = false
	if _, err := open().Render(context.Background(), accounts.User{ID: "bob"}, "movie", 320); err != nil {
		t.Fatal(err)
	}
	if worker.calls != 2 {
		t.Fatal("disk cache shared across users")
	}
	info, _ := os.Stat(source.path)
	if err := os.WriteFile(source.path, []byte("SMB edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source.path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	result, err = open().Render(context.Background(), actor, "movie", 320)
	if err != nil || worker.calls != 3 || result.ETag == original.ETag {
		t.Fatalf("SMB revision: calls=%d err=%v", worker.calls, err)
	}
	if err := os.Remove(source.path); err != nil {
		t.Fatal(err)
	}
	if _, err := open().Render(context.Background(), actor, "movie", 320); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disk cache bypassed deletion: %v", err)
	}
}

func TestPersistentPreviewCorruptionAndUnavailableDiskAreMisses(t *testing.T) {
	_, source, worker := fixture(t, "movie.mp4")
	dir := filepath.Join(t.TempDir(), "previews")
	service, err := NewWithCache(source, worker, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Render(context.Background(), accounts.User{}, "movie", 320); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("cache entries=%d", len(entries))
	}
	path := filepath.Join(dir, entries[0].Name())
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode=%v", info.Mode())
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(info.Size())), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithCache(source, worker, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Render(context.Background(), accounts.User{}, "movie", 320); err != nil || worker.calls != 2 {
		t.Fatalf("corrupt cache: calls=%d err=%v", worker.calls, err)
	}
	// A cache directory removed while the service is running only loses caching.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Render(context.Background(), accounts.User{}, "other", 320); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPreviewMissesDecodeOnce(t *testing.T) {
	service, _, worker := fixture(t, "movie.mp4")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := service.Render(context.Background(), accounts.User{}, "movie", 320); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if worker.calls != 1 {
		t.Fatalf("concurrent requests decoded %d times", worker.calls)
	}
}

func TestQueuedPreviewRechecksRevokedAccess(t *testing.T) {
	service, source, worker := fixture(t, "movie.mp4")
	source.opened = make(chan struct{}, 1)
	service.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.Render(ctx, accounts.User{}, "movie", 320); done <- err }()
	select {
	case <-source.opened:
	case <-ctx.Done():
		t.Fatal("request never opened source")
	}
	source.mu.Lock()
	source.denied = true
	source.mu.Unlock()
	<-service.gate
	if err := <-done; !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("queued access revocation: %v", err)
	}
	if worker.calls != 0 {
		t.Fatal("revoked source was decoded")
	}
}
