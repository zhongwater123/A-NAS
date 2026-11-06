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

// The companion's animations are listed by name, one URL per format, at
// dsh-pet's /thumb route with the pool in the query (install-companion-animations.sh).
func TestHandlerListsCompanionAnimationsAndServesByteRanges(t *testing.T) {
	store := t.TempDir()
	const pool, previous = "0123456789abcdef", "fedcba9876543210"
	writeCompanionPool(t, store, pool, map[string]string{
		"东张西望.webm": "0123456789", "东张西望.mov": "quicktime", "待机.webm": "idle",
		"notes.txt": "not an animation", ".hidden.webm": "hidden",
	})
	writeCompanionPool(t, store, previous, map[string]string{"东张西望.webm": "previous"})
	if err := os.WriteFile(filepath.Join(store, "outside.webm"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside.webm", filepath.Join(store, "pools", pool, "escape.webm")); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(store, "current")
	if err := os.Symlink("pools/"+pool, current); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithOptions(t, webui.Options{CompanionDirectory: current})

	manifest := companionManifest(t, handler)
	if manifest.Pool != pool {
		t.Fatalf("pool = %q, want %q", manifest.Pool, pool)
	}
	if got, want := len(manifest.Animations), 2; got != want {
		t.Fatalf("animations = %v, want %d", manifest.Animations, want)
	}
	look := manifest.Animations["东张西望"]
	if look["webm"] != "/companion/thumb/main/%E4%B8%9C%E5%BC%A0%E8%A5%BF%E6%9C%9B.webm?v="+pool || look["mov"] == "" {
		t.Fatalf("东张西望 = %v", look)
	}

	request := httptest.NewRequest(http.MethodGet, look["webm"], nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()
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
	if got, want := recorder.Header().Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	recorder = getCompanion(handler, look["mov"])
	if got := recorder.Header().Get("Content-Type"); recorder.Code != http.StatusOK || got != "video/quicktime" {
		t.Fatalf("mov status = %d, Content-Type = %q", recorder.Code, got)
	}
	// Like dsh-pet, an animation without an HEVC file is served as VP9.
	recorder = getCompanion(handler, "/companion/thumb/main/待机.mov?v="+pool)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "idle" || recorder.Header().Get("Content-Type") != "video/webm" {
		t.Fatalf("mov fallback status = %d, body = %q, Content-Type = %q", recorder.Code, recorder.Body.String(), recorder.Header().Get("Content-Type"))
	}

	// A rollback names the other pool; a page opened before it keeps its files.
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pools/"+previous, current); err != nil {
		t.Fatal(err)
	}
	if recorder := getCompanion(handler, look["webm"]); recorder.Body.String() != "0123456789" {
		t.Fatalf("pool %s after the switch = %q", pool, recorder.Body.String())
	}
	manifest = companionManifest(t, handler)
	rolledBack := manifest.Animations["东张西望"]["webm"]
	if manifest.Pool != previous || rolledBack != "/companion/thumb/main/%E4%B8%9C%E5%BC%A0%E8%A5%BF%E6%9C%9B.webm?v="+previous {
		t.Fatalf("manifest after the switch = %+v", manifest)
	}
	if recorder := getCompanion(handler, rolledBack); recorder.Body.String() != "previous" {
		t.Fatalf("pool %s = %q", previous, recorder.Body.String())
	}
	// Without a pool the current one is served and revalidated.
	recorder = getCompanion(handler, "/companion/thumb/main/东张西望.webm")
	if recorder.Body.String() != "previous" || recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned body = %q, Cache-Control = %q", recorder.Body.String(), recorder.Header().Get("Cache-Control"))
	}

	for _, path := range []string{
		"/companion/thumb/main/notes.txt?v=" + pool, "/companion/thumb/main/.hidden.webm?v=" + pool,
		"/companion/thumb/main/escape.webm?v=" + pool, "/companion/thumb/main/../outside.webm",
		"/companion/thumb/main/%2E%2E%2Foutside.webm", "/companion/thumb/main/东张西望.webm?v=..%2F..",
		"/companion/thumb/main/东张西望.webm?v=aaaaaaaaaaaaaaaa", "/companion/thumb/main/东张西望.webm?v=0123456789ABCDEF",
		"/companion/thumb/other/东张西望.webm", "/companion/animations/东张西望.webm",
	} {
		if recorder := getCompanion(handler, path); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestHandlerShowsNoCompanionAnimationsUntilTheyAreInstalled(t *testing.T) {
	handler := newHandlerWithOptions(t, webui.Options{CompanionDirectory: filepath.Join(t.TempDir(), "missing")})
	recorder := getCompanion(handler, "/companion/animations")
	if got, want := recorder.Body.String(), "{\"pool\":\"\",\"animations\":{}}\n"; recorder.Code != http.StatusOK || got != want {
		t.Fatalf("status = %d, body = %q, want %q", recorder.Code, got, want)
	}
	if recorder := getCompanion(handler, "/companion/thumb/main/待机.webm"); recorder.Code != http.StatusNotFound {
		t.Fatalf("animation status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if _, err := webui.NewWithOptions(http.NotFoundHandler(), webui.Options{CompanionDirectory: "relative"}); err == nil {
		t.Fatal("NewWithOptions() accepted a relative companion directory")
	}
}

func writeCompanionPool(t *testing.T, store, pool string, files map[string]string) {
	t.Helper()
	directory := filepath.Join(store, "pools", pool)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type servedCompanionManifest struct {
	Pool       string                       `json:"pool"`
	Animations map[string]map[string]string `json:"animations"`
}

func companionManifest(t *testing.T, handler http.Handler) servedCompanionManifest {
	t.Helper()
	recorder := getCompanion(handler, "/companion/animations")
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("manifest status = %d, want %d; body = %s", got, want, recorder.Body.String())
	}
	var manifest servedCompanionManifest
	if err := json.Unmarshal(recorder.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func getCompanion(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
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
