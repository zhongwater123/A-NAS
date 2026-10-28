package mediatype

import "testing"

func TestVideosAreKnownByExtensionInAnyCase(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"film.MKV": true, "show/episode.ts": true, "old.rmvb": true, "dvd/VTS_01_1.VOB": true,
		"photo.jpg": false, "notes.ts.txt": false, "mkv": false,
	} {
		if got := IsVideo(name); got != want {
			t.Errorf("IsVideo(%q) = %v, want %v", name, got, want)
		}
	}
}
