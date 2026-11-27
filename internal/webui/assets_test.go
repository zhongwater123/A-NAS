package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTheLicenseListIsAskedForAgainAndTheBundleIsKept(t *testing.T) {
	for name, want := range map[string]string{
		"assets/third-party-licenses.txt": "no-cache",
		"assets/workspace-frame.js":       "no-cache",
		"assets/index-BpHiypLE.js":        "public, max-age=31536000, immutable",
		"assets/AppCenterPanel-LITkG.css": "public, max-age=31536000, immutable",
	} {
		if got := assetCacheControl(name); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", name, got, want)
		}
	}
}

// Every shared page loads the frame helper (ADR 0040): a browser asks for it
// again each time and gets a 304 while it is unchanged.
func TestTheFrameHelperIsRevalidated(t *testing.T) {
	assets := fstest.MapFS{workspaceFrame: {Data: []byte("(()=>{})();")}}
	h := &handler{assets: assets, etags: assetTags(assets)}
	first := httptest.NewRecorder()
	h.serveAsset(first, httptest.NewRequest(http.MethodGet, "/"+workspaceFrame, nil))
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "no-cache" || tag == "" ||
		first.Body.String() != "(()=>{})();" || !strings.HasPrefix(first.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("first: %d %v %q", first.Code, first.Header(), first.Body.String())
	}
	again := httptest.NewRequest(http.MethodGet, "/"+workspaceFrame, nil)
	again.Header.Set("If-None-Match", tag)
	second := httptest.NewRecorder()
	h.serveAsset(second, again)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("again: %d %q", second.Code, second.Body.String())
	}
}
