package taskicon

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/imagetest"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func TestNormalizeInstalledIcon(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 128, 96))
	img.Set(2, 3, color.NRGBA{R: 200, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	id, source := Normalize(data.Bytes())
	_, encoded, _ := strings.Cut(source, ",")
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	config, _ := png.DecodeConfig(bytes.NewReader(decoded))
	if config.Width != 64 || config.Height != 48 {
		t.Fatalf("unexpected thumbnail: %+v", config)
	}
	s := iconSnapshot(map[string]string{id: source}, id)
	if !s.Valid() {
		t.Fatal("icon snapshot invalid")
	}
	s.Services.Items[0].IconID = "missing"
	if s.Valid() {
		t.Fatal("dangling icon accepted")
	}
	if s := iconSnapshot(map[string]string{strings.Repeat("0", 64): source}, strings.Repeat("0", 64)); s.Valid() {
		t.Fatal("incorrect hash accepted")
	}
}
func TestIconTheDecoderPanicsOnIsDropped(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	if id, source := Normalize(imagetest.PanickingPicture()); id != "" || source != "" {
		t.Fatalf("Normalize() = %q, %q, want no icon", id, source)
	}
	imagetest.RequirePanicLogged(t, panics(), "task icon")
}
func TestStaticSVGAndUnsafeArtwork(t *testing.T) {
	safe := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><defs><linearGradient id="a"><stop offset="0" stop-color="#123"/></linearGradient></defs><path fill="url(#a)" d="M0 0h24v24z"/></svg>`
	id, source := Normalize([]byte(safe))
	if s := iconSnapshot(map[string]string{id: source}, id); id == "" || !s.Valid() {
		t.Fatal("safe installed SVG rejected")
	}
	for _, body := range []string{`<script>alert(1)</script>`, `<foreignObject/>`, `<image href="https://example.com/x"/>`, `<use href="file:///etc/passwd"/>`, `<path onload="alert(1)"/>`, `<path fill="url(https://example.com/x)"/>`, `<path style="fill:uRL(https://x)"/>`, `<style>@import 'x';</style>`, `<path style="fill:u\72l(https://x)"/>`} {
		if id, _ := Normalize([]byte(`<svg xmlns="http://www.w3.org/2000/svg">` + body + `</svg>`)); id != "" {
			t.Fatalf("unsafe SVG accepted: %s", body)
		}
	}
	for _, invalid := range []string{"not an icon", strings.Repeat("x", taskmanager.MaxIconBytes+1), `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`} {
		if id, _ := Normalize([]byte(invalid)); id != "" {
			t.Fatal("invalid icon accepted")
		}
	}
}

// iconSnapshot is a valid snapshot whose one service shows icon id.
func iconSnapshot(icons map[string]string, id string) taskmanager.TaskSnapshot {
	return taskmanager.TaskSnapshot{
		ObservedAt: time.Now(), SampleSeconds: 2, Icons: icons,
		Services:  taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{{ID: "service:app", Name: "app", IconID: id}}},
		Processes: taskmanager.TaskGroup{Status: taskmanager.Available, Items: []taskmanager.Task{}},
	}
}
