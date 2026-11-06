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

// Installed apps are served from other ports of the device's address, which
// browsers count as the same site, so only the desktop itself may frame its
// pages (issue #161).
func TestHandlerKeepsOtherOriginsFromFramingTheDesktop(t *testing.T) {
	handler := newHandler(t)
	for _, path := range []string{"/", "/local-console/screensavers", "/missing"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s X-Frame-Options = %q, want DENY", path, got)
		}
		policy := recorder.Header().Get("Content-Security-Policy")
		for _, directive := range []string{"frame-ancestors 'none'", "default-src 'self'", "script-src 'self'", "img-src 'self' data: blob:", "connect-src 'self' data: blob:", "frame-src 'self'", "object-src 'none'"} {
			if !strings.Contains(policy, directive) {
				t.Errorf("%s Content-Security-Policy = %q, want %q", path, policy, directive)
			}
		}
	}
	// File previews, such as PDFs, are framed by the desktop itself.
	for _, path := range []string{"/api/v1/missing", "/healthz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if got := recorder.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("%s X-Frame-Options = %q, want SAMEORIGIN", path, got)
		}
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

// The Product Service reads a "current" link that install-screensavers.sh
// moves between pools (ADR 0014); a move must take effect without a restart.
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

// The companion's animations are listed by name, one URL per format, and
// served from the current pool only (install-companion-animations.sh).
func TestHandlerListsCompanionAnimationsAndServesByteRanges(t *testing.T) {
	root := t.TempDir()
	pool := filepath.Join(root, "pool")
	if err := os.Mkdir(pool, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"东张西望.webm": "0123456789", "东张西望.mov": "quicktime", "待机.webm": "idle",
		"notes.txt": "not an animation", ".hidden.webm": "hidden",
	} {
		if err := os.WriteFile(filepath.Join(pool, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "outside.webm"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside.webm", filepath.Join(pool, "escape.webm")); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithOptions(t, webui.Options{CompanionDirectory: pool})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/companion/animations", nil))

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("manifest status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	var manifest struct {
		Animations map[string]map[string]string `json:"animations"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if got, want := len(manifest.Animations), 2; got != want {
		t.Fatalf("animations = %v, want %d", manifest.Animations, want)
	}
	look := manifest.Animations["东张西望"]
	if look["webm"] != "/companion/animations/%E4%B8%9C%E5%BC%A0%E8%A5%BF%E6%9C%9B.webm" || look["mov"] == "" {
		t.Fatalf("东张西望 = %v", look)
	}

	request := httptest.NewRequest(http.MethodGet, look["webm"], nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got, want := recorder.Code, http.StatusPartialContent; got != want {
		t.Fatalf("status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	if got, want := recorder.Body.String(), "2345"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/webm" {
		t.Fatalf("Content-Type = %q, want video/webm", got)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, look["mov"], nil))
	if got := recorder.Header().Get("Content-Type"); recorder.Code != http.StatusOK || got != "video/quicktime" {
		t.Fatalf("mov status = %d, Content-Type = %q", recorder.Code, got)
	}

	for _, path := range []string{
		"/companion/animations/notes.txt", "/companion/animations/.hidden.webm", "/companion/animations/escape.webm",
		"/companion/animations/../outside.webm", "/companion/animations/%2E%2E%2Foutside.webm",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestHandlerShowsNoCompanionAnimationsUntilTheyAreInstalled(t *testing.T) {
	handler := newHandlerWithOptions(t, webui.Options{CompanionDirectory: filepath.Join(t.TempDir(), "missing")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/companion/animations", nil))
	if got, want := recorder.Body.String(), "{\"animations\":{}}\n"; recorder.Code != http.StatusOK || got != want {
		t.Fatalf("status = %d, body = %q, want %q", recorder.Code, got, want)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/companion/animations/待机.webm", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("animation status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if _, err := webui.NewWithOptions(http.NotFoundHandler(), webui.Options{CompanionDirectory: "relative"}); err == nil {
		t.Fatal("NewWithOptions() accepted a relative companion directory")
	}
}
func newHandler(t *testing.T) http.Handler {
	return newHandlerWithOptions(t, webui.Options{})
}

func newHandlerWithOptions(t *testing.T, options webui.Options) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, ProductVersion: "test-version", Logger: logger,
	})
	handler, err := webui.NewWithOptions(api, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}
