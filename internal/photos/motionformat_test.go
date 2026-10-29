package photos

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMotionMetadataRejectsFalsePositivesAndBadOffsets(t *testing.T) {
	cases := []struct {
		xml             string
		marked, invalid bool
	}{
		{`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MotionPhoto="1"><broken>`, true, true},
		{`<x xmlns:g="wrong" g:MicroVideo="1" g:MicroVideoOffset="123"/>`, false, false},
		{`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MotionPhoto="0" g:MicroVideo="1" g:MicroVideoOffset="123"/>`, false, false},
		{`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MicroVideo="1" g:MicroVideoOffset="-1"/>`, true, true},
		{`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MicroVideo="1" g:MicroVideoOffset="99999999999999999999"/>`, true, true},
		{`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MotionPhoto="1"/>`, true, true},
	}
	for _, c := range cases {
		_, marked, err := motionLength([]byte(c.xml))
		if marked != c.marked || (err != nil) != c.invalid {
			t.Fatalf("%s => %v %v", c.xml, marked, err)
		}
	}
}
func box(kind string, body ...[]byte) []byte {
	data := bytes.Join(body, nil)
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, uint32(8+len(data)))
	copy(out[4:], kind)
	return append(out, data...)
}
func dimensions(w, h uint32) []byte {
	data := make([]byte, 12)
	binary.BigEndian.PutUint32(data[4:], w)
	binary.BigEndian.PutUint32(data[8:], h)
	return box("ispe", data)
}

// primaryGridHEIF is a grid image whose primary item uses property 2, not the
// first tile's 64x64 property, and a quarter turn. Version 1 boxes carry
// 32-bit item IDs and 15-bit property indices.
func primaryGridHEIF(version byte) []byte {
	pitm := []byte{0, 0, 0, 0, 0, 2}
	ipma := []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 2, 2, 2, 3}
	if version == 1 {
		pitm = []byte{1, 0, 0, 0, 0, 0, 0, 2}
		ipma = []byte{1, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2, 2, 0x80, 2, 0, 3}
	}
	data := box("ftyp", []byte("heic\x00\x00\x00\x00heic"))
	meta := box("meta", make([]byte, 4), box("pitm", pitm), box("iprp", box("ipco", dimensions(64, 64), dimensions(4000, 3000), box("irot", []byte{1})), box("ipma", ipma)))
	return append(data, meta...)
}
func openHEIF(t *testing.T, data []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "primary.heic")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
func TestHEIFUsesPrimaryPropertiesAndDisplayRotation(t *testing.T) {
	for _, version := range []byte{0, 1} {
		w, h, err := heifDimensions(openHEIF(t, primaryGridHEIF(version)))
		if err != nil || w != 3000 || h != 4000 {
			t.Fatalf("version %d: dimensions %dx%d %v", version, w, h, err)
		}
	}
	truncated := primaryGridHEIF(0)
	if _, _, err := heifDimensions(openHEIF(t, truncated[:len(truncated)-1])); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("truncated HEIF: %v", err)
	}
}
func FuzzHeifDimensions(f *testing.F) {
	for _, version := range []byte{0, 1} {
		data := primaryGridHEIF(version)
		f.Add(data)
		f.Add(data[:len(data)-1])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		w, h, err := heifDimensions(openHEIF(t, data))
		switch {
		case errors.Is(err, ErrUnsupportedType), errors.Is(err, ErrTooLarge):
		case err != nil:
			t.Fatalf("unexpected error %v", err)
		case w <= 0 || h <= 0 || int64(w)*int64(h) > maxImagePixels:
			t.Fatalf("dimensions %dx%d", w, h)
		}
	})
}
func FuzzMotionMetadata(f *testing.F) {
	f.Add([]byte(`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MicroVideo="1" g:MicroVideoOffset="40"/>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65535 {
			return
		}
		_, _, _ = motionLength(data)
	})
}
func TestModernMotionDirectoryWithGainMapAndElementFlag(t *testing.T) {
	xmp := `<x xmlns:g="http://ns.google.com/photos/1.0/camera/" xmlns:c="http://ns.google.com/photos/1.0/container/" xmlns:i="http://ns.google.com/photos/1.0/container/item/"><g:MotionPhoto>1</g:MotionPhoto><c:Directory><c:Item i:Semantic="Primary"/><c:Item i:Semantic="GainMap" i:Length="80"/><c:Item i:Semantic="MotionPhoto" i:Mime="video/mp4" i:Length="1234"/></c:Directory></x>`
	n, marked, err := motionLength([]byte(xmp))
	if err != nil || !marked || n != 1234 {
		t.Fatalf("%d %v %v", n, marked, err)
	}
	broken := strings.Replace(xmp, `i:Mime="video/mp4"`, `i:Mime="image/jpeg"`, 1)
	if _, _, err := motionLength([]byte(broken)); err == nil {
		t.Fatal("accepted nonvideo motion item")
	}
}
