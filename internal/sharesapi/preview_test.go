//go:build linux

package sharesapi

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/shares"
)

type heicBackend struct{ backend }

func (heicBackend) Capture(ctx context.Context, u shares.User, ids []string, visit func(shares.Entry, io.Reader) error) error {
	return visit(shares.Entry{Path: "photo.heic", Size: 5}, strings.NewReader("hello"))
}

type imageWorker struct {
	mediaworker.Worker
	image func(context.Context, *os.File, int) ([]byte, error)
}

func (w imageWorker) Image(ctx context.Context, file *os.File, size int) ([]byte, error) {
	return w.image(ctx, file, size)
}
func previewFixture(t *testing.T, worker mediaworker.Worker) (*shares.Store, http.Handler, shares.Share) {
	dir := t.TempDir()
	s, err := shares.Open(filepath.Join(dir, "db"), filepath.Join(dir, "content"), shares.Options{Backend: heicBackend{}, Active: func(context.Context, string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	v := published(t, s)
	return s, NewWithPreview(s, nil, nil, worker), v
}
func TestHEICPreviewUsesReadOnlyAnonymousInput(t *testing.T) {
	worker := imageWorker{image: func(ctx context.Context, file *os.File, size int) ([]byte, error) {
		if size != 1600 {
			t.Fatalf("preview size %d", size)
		}
		data, err := io.ReadAll(file)
		if err != nil || string(data) != "hello" {
			t.Fatalf("source %q %v", data, err)
		}
		if _, err = file.Write([]byte("bad")); err == nil {
			t.Fatal("worker received writable input")
		}
		var out bytes.Buffer
		err = png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 1, 1)))
		return out.Bytes(), err
	}}
	_, h, v := previewFixture(t, worker)
	w := request(h, "b", "GET", PathPrefix+"/"+v.ID+"/entries/"+v.Entries[0].ID+"/preview", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
}
func TestRevokeCancelsHEICConversionAndWithholdsResult(t *testing.T) {
	started := make(chan struct{})
	worker := imageWorker{image: func(ctx context.Context, file *os.File, size int) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s, h, v := previewFixture(t, worker)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", PathPrefix+"/"+v.ID+"/entries/"+v.Entries[0].ID+"/preview", nil)
		r = r.WithContext(accounts.WithUser(r.Context(), accounts.User{ID: "b"}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		done <- w
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("decoder not started")
	}
	if err := s.Revoke(context.Background(), shares.User{ID: "a"}, v.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 410 {
			t.Fatalf("revoked preview %d %s", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("decoder did not cancel")
	}
}
