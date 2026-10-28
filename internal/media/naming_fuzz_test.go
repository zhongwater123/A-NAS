package media

import "testing"

// FuzzParseName reads any file name in every library mode without panicking
// (#223).
func FuzzParseName(f *testing.F) {
	for _, name := range []string{"The.Show.S01E02.1080p", "[字幕组] 某剧 第05集", "01", "Movie (2019)", "IMG_0001", "S99E999999999999", ""} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		for _, mode := range []nameMode{nameMovie, nameAuto, nameShow} {
			_ = parseName(name, mode)
		}
	})
}
