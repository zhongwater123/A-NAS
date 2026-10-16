package taskmanager

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestNormalizeInstalledIcon(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 128, 96))
	img.Set(2, 3, color.NRGBA{R: 200, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	id, source := NormalizeIcon(data.Bytes())
	if !validIcon(id, source) {
		t.Fatal("normalized PNG rejected")
	}
	_, encoded, _ := strings.Cut(source, ",")
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	config, _ := png.DecodeConfig(bytes.NewReader(decoded))
	if config.Width != 64 || config.Height != 48 {
		t.Fatalf("unexpected thumbnail: %+v", config)
	}
	s := TaskSnapshot{ObservedAt: time.Now(), SampleSeconds: 2, Icons: map[string]string{id: source}, Services: TaskGroup{Status: Available, Items: []Task{{ID: "service:app", Name: "app", IconID: id}}}, Processes: TaskGroup{Status: Available, Items: []Task{}}}
	if !s.Valid() {
		t.Fatal("icon snapshot invalid")
	}
	s.Services.Items[0].IconID = "missing"
	if s.Valid() {
		t.Fatal("dangling icon accepted")
	}
	if validIcon(strings.Repeat("0", 64), source) {
		t.Fatal("incorrect hash accepted")
	}
}
func TestStaticSVGAndUnsafeArtwork(t *testing.T) {
	safe := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><defs><linearGradient id="a"><stop offset="0" stop-color="#123"/></linearGradient></defs><path fill="url(#a)" d="M0 0h24v24z"/></svg>`
	id, source := NormalizeIcon([]byte(safe))
	if !validIcon(id, source) {
		t.Fatal("safe installed SVG rejected")
	}
	for _, body := range []string{`<script>alert(1)</script>`, `<foreignObject/>`, `<image href="https://example.com/x"/>`, `<use href="file:///etc/passwd"/>`, `<path onload="alert(1)"/>`, `<path fill="url(https://example.com/x)"/>`, `<path style="fill:uRL(https://x)"/>`, `<style>@import 'x';</style>`, `<path style="fill:u\72l(https://x)"/>`} {
		if id, _ := NormalizeIcon([]byte(`<svg xmlns="http://www.w3.org/2000/svg">` + body + `</svg>`)); id != "" {
			t.Fatalf("unsafe SVG accepted: %s", body)
		}
	}
	for _, invalid := range []string{"not an icon", strings.Repeat("x", MaxIconBytes+1), `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`} {
		if id, _ := NormalizeIcon([]byte(invalid)); id != "" {
			t.Fatal("invalid icon accepted")
		}
	}
}
