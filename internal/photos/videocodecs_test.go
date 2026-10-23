package photos_test

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Videos made by FFmpeg itself take the real decoders through import,
// thumbnail, poster and conversion. Run in the disposable Debian codec test
// image, as the motion photo codec test is.
func TestRealVideoCodecs(t *testing.T) {
	tools, ok := mediaworker.LocalTools()
	if !ok {
		t.Skip("media codecs not installed")
	}
	dir := t.TempDir()
	encode := func(name string, args ...string) []byte {
		t.Helper()
		path := filepath.Join(dir, name)
		command := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"}, args...)
		if output, err := exec.Command(tools.FFmpeg, append(command, path)...).CombinedOutput(); err != nil {
			t.Fatalf("encode %s: %v %s", name, err, output)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	source := []string{"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=3", "-f", "lavfi", "-i", "sine=frequency=440:duration=3"}
	h264 := []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest"}
	landscape := encode("landscape.mp4", append(append(append([]string{}, source...), h264...), "-metadata", "creation_time=2024-05-01T10:20:30Z")...)
	// A phone held upright stores the picture landscape and turns it.
	portrait := encode("portrait.mov", "-display_rotation", "90", "-i", filepath.Join(dir, "landscape.mp4"), "-c", "copy")
	files := map[string][]byte{
		"landscape.mp4": landscape,
		"portrait.mov":  portrait,
		"family.mkv":    encode("family.mkv", append(append([]string{}, source...), h264...)...),
		"camera.avi":    encode("camera.avi", append(append([]string{}, source...), "-c:v", "mjpeg", "-c:a", "pcm_s16le", "-shortest")...),
		"camcorder.mts": encode("camcorder.mts", append(append(append([]string{}, source...), h264...), "-f", "mpegts")...),
	}
	service, err := photos.Open(filepath.Join(dir, "store"), photos.Options{Media: mediaworker.Local{Tools: tools}, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	private, _ := libraries(t, service, alice)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for name, content := range files {
		t.Run(name, func(t *testing.T) {
			asset, err := service.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: name, Content: bytes.NewReader(content)})
			if err != nil {
				t.Fatalf("Import() error = %v", err)
			}
			if asset.Video == nil || asset.Video.Duration < 2.9 || asset.Video.Duration > 3.2 {
				t.Fatalf("video = %+v", asset.Video)
			}
			wantWidth, wantHeight := 320, 240
			if name == "portrait.mov" {
				wantWidth, wantHeight = 240, 320
			}
			if asset.Width != wantWidth || asset.Height != wantHeight {
				t.Fatalf("size %dx%d; want %dx%d", asset.Width, asset.Height, wantWidth, wantHeight)
			}
			if name == "landscape.mp4" && (asset.TakenAt == nil || !asset.TakenAt.Equal(time.Date(2024, 5, 1, 10, 20, 30, 0, time.UTC))) {
				t.Fatalf("taken at %v", asset.TakenAt)
			}
			for {
				processed, err := service.ProcessMediaJob(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !processed {
					break
				}
			}
			for _, open := range []func() (photos.Content, error){
				func() (photos.Content, error) { return service.Thumbnail(ctx, alice, asset.ID) },
				func() (photos.Content, error) { return service.Preview(ctx, alice, asset.ID) },
			} {
				content, err := open()
				if err != nil {
					t.Fatalf("thumbnail or poster: %v", err)
				}
				config, _, err := image.DecodeConfig(content.Reader)
				_ = content.Reader.Close()
				if err != nil || (config.Width > config.Height) != (wantWidth > wantHeight) {
					t.Fatalf("thumbnail or poster %dx%d, %v; want it upright", config.Width, config.Height, err)
				}
			}
			playback, err := service.VideoPlayback(ctx, alice, asset.ID, photos.BrowserCaps{})
			if err != nil {
				t.Fatal(err)
			}
			if playback.Mode == photos.PlayDirect {
				return
			}
			stream, err := service.StreamVideo(ctx, alice, asset.ID, photos.VideoStreamRequest{Start: 1})
			if err != nil {
				t.Fatalf("StreamVideo() error = %v", err)
			}
			head := make([]byte, 64<<10)
			read, _ := io.ReadAtLeast(stream, head, 1024)
			stream.Close()
			if !bytes.Contains(head[:read], []byte("ftyp")) || !bytes.Contains(head[:read], []byte("moov")) {
				t.Fatalf("%s stream does not start as fragmented MP4: %q", playback.Mode, head[:min(read, 64)])
			}
		})
	}
}
