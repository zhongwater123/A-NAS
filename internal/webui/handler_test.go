package webui_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/webui"
)

func TestHandlerServesEmbeddedDesktopAndImmutableAssets(t *testing.T) {
	handler := newHandler(t)
	indexRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	indexRecorder := httptest.NewRecorder()

	handler.ServeHTTP(indexRecorder, indexRequest)

	if got, want := indexRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("index status = %d, want %d; body = %s", got, want, indexRecorder.Body.String())
	}
	if got := indexRecorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("index Content-Type = %q", got)
	}
	if got := indexRecorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("index Cache-Control = %q, want no-store", got)
	}

	assetPattern := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	match := assetPattern.FindStringSubmatch(indexRecorder.Body.String())
	if len(match) != 2 {
		t.Fatalf("index does not reference a hashed asset: %s", indexRecorder.Body.String())
	}
	assetRequest := httptest.NewRequest(http.MethodGet, match[1], nil)
	assetRecorder := httptest.NewRecorder()
	handler.ServeHTTP(assetRecorder, assetRequest)
	if got, want := assetRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("asset status = %d, want %d", got, want)
	}
	if got := assetRecorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", got)
	}
}

func TestHandlerKeepsUnknownAPIErrorsAsJSON(t *testing.T) {
	handler := newHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusNotFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestHandlerListsConfiguredScreenSaverPoolAndServesByteRanges(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "alpha.mp4"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "beta.MP4"), []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested.mp4"), 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithOptions(t, webui.Options{ScreensaverDirectory: directory})
	manifestRequest := httptest.NewRequest(http.MethodGet, "/local-console/screensavers", nil)
	manifestRecorder := httptest.NewRecorder()

	handler.ServeHTTP(manifestRecorder, manifestRequest)

	if got, want := manifestRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("manifest status = %d, want %d; body = %s", got, want, manifestRecorder.Body.String())
	}
	if got := manifestRecorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("manifest Content-Type = %q, want application/json", got)
	}
	var manifest struct {
		Videos []string `json:"videos"`
	}
	if err := json.Unmarshal(manifestRecorder.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if got, want := len(manifest.Videos), 2; got != want {
		t.Fatalf("manifest videos = %v, want %d entries", manifest.Videos, want)
	}
	if strings.Contains(manifestRecorder.Body.String(), "alpha") || strings.Contains(manifestRecorder.Body.String(), "beta") {
		t.Fatalf("manifest leaks host filenames: %s", manifestRecorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, manifest.Videos[0], nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusPartialContent; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	if got, want := recorder.Body.String(), "2345"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	legacyRequest := httptest.NewRequest(http.MethodGet, "/local-console/screensaver.mp4", nil)
	legacyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(legacyRecorder, legacyRequest)
	if got, want := legacyRecorder.Code, http.StatusOK; got != want {
		t.Fatalf("legacy status = %d, want %d", got, want)
	}
}

// The installer points the Product Service at a "current" link and moves it
// between pools (issue #49); a move must take effect without a restart.
func TestHandlerFollowsTheCurrentScreenSaverPoolLink(t *testing.T) {
	root := t.TempDir()
	for pool, videos := range map[string][]string{"old": {"a.mp4"}, "new": {"a.mp4", "b.mp4"}} {
		if err := os.Mkdir(filepath.Join(root, pool), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, video := range videos {
			if err := os.WriteFile(filepath.Join(root, pool, video), []byte(pool), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink("old", current); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithOptions(t, webui.Options{ScreensaverDirectory: current})
	manifest := func() []string {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/local-console/screensavers", nil))
		var body struct {
			Videos []string `json:"videos"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Videos
	}

	if got := manifest(); len(got) != 1 {
		t.Fatalf("manifest through current -> old = %v, want 1 entry", got)
	}
	if err := os.Symlink("new", filepath.Join(root, ".current.next")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".current.next"), current); err != nil {
		t.Fatal(err)
	}
	videos := manifest()
	if len(videos) != 2 {
		t.Fatalf("manifest through current -> new = %v, want 2 entries", videos)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, videos[0], nil))
	if got, want := recorder.Body.String(), "new"; got != want {
		t.Fatalf("video body = %q, want %q", got, want)
	}
}

func TestHandlerRejectsRelativeScreenSaverDirectory(t *testing.T) {
	_, err := webui.NewWithOptions(http.NotFoundHandler(), webui.Options{ScreensaverDirectory: "relative"})
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("NewWithOptions() error = %v, want absolute-directory error", err)
	}
}

func TestHandlerReturnsNotFoundWhenScreenSaverIsUnavailable(t *testing.T) {
	handler := newHandlerWithOptions(t, webui.Options{ScreensaverDirectory: filepath.Join(t.TempDir(), "missing")})
	request := httptest.NewRequest(http.MethodGet, "/local-console/screensavers/not-a-video.mp4", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusNotFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHandlerReturnsAnEmptyManifestWhenScreenSaverPoolIsMissing(t *testing.T) {
	handler := newHandlerWithOptions(t, webui.Options{ScreensaverDirectory: filepath.Join(t.TempDir(), "missing")})
	request := httptest.NewRequest(http.MethodGet, "/local-console/screensavers", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := recorder.Body.String(), "{\"videos\":[]}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func newHandler(t *testing.T) http.Handler {
	return newHandlerWithOptions(t, webui.Options{})
}

func newHandlerWithOptions(t *testing.T, options webui.Options) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.New(fake.NewHealthy(), httpapi.DataSourceSimulated, "test-version", logger)
	handler, err := webui.NewWithOptions(api, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}
