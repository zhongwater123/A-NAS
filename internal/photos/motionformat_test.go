package photos

import (
	"bytes"
	"encoding/binary"
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
func TestHEIFUsesPrimaryPropertiesAndDisplayRotation(t *testing.T) {
	// The primary grid uses property 2, not the first tile's 64x64 property.
	data := box("ftyp", []byte("heic\x00\x00\x00\x00heic"))
	meta := box("meta", make([]byte, 4), box("pitm", []byte{0, 0, 0, 0, 0, 2}), box("iprp", box("ipco", dimensions(64, 64), dimensions(4000, 3000), box("irot", []byte{1})), box("ipma", []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 2, 2, 2, 3})))
	data = append(data, meta...)
	path := filepath.Join(t.TempDir(), "primary.heic")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, _ := os.Open(path)
	defer func() { _ = file.Close() }()
	w, h, err := heifDimensions(file)
	if err != nil || w != 3000 || h != 4000 {
		t.Fatalf("dimensions %dx%d %v", w, h, err)
	}
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
