package webui_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
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

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httpapi.New(fake.NewHealthy(), httpapi.DataSourceSimulated, "test-version", logger)
	handler, err := webui.New(api)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}
