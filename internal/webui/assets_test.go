package webui

import "testing"

func TestTheLicenseListIsAskedForAgainAndTheBundleIsKept(t *testing.T) {
	for name, want := range map[string]string{
		"assets/third-party-licenses.txt": "no-cache",
		"assets/index-BpHiypLE.js":        "public, max-age=31536000, immutable",
		"assets/AppCenterPanel-LITkG.css": "public, max-age=31536000, immutable",
	} {
		if got := assetCacheControl(name); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", name, got, want)
		}
	}
}
