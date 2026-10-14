package photos_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Self-generated fixtures exercise the real codec boundary without personal
// photos or network fixtures. Run in the disposable Debian codec test image.
func TestRealMotionPhotoCodecs(t *testing.T) {
	encoder, err := exec.LookPath("heif-enc")
	if err != nil {
		t.Skip("heif-enc not installed")
	}
	tools, ok := mediaworker.LocalTools()
	if !ok {
		t.Skip("media codecs not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.png")
	heic := filepath.Join(dir, "live.heic")
	movie := filepath.Join(dir, "live.mov")
	if err := os.WriteFile(source, widePNG(t, 640, 480), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(encoder, "-o", heic, source).CombinedOutput(); err != nil {
		t.Fatalf("HEIC encoder: %v %s", err, output)
	}
	if output, err := exec.Command(tools.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=12:duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", movie).CombinedOutput(); err != nil {
		t.Fatalf("movie encoder: %v %s", err, output)
	}
	still, _ := os.ReadFile(heic)
	clip, _ := os.ReadFile(movie)
	local := mediaworker.Local{Tools: tools}
	service, err := photos.Open(filepath.Join(dir, "store"), photos.Options{Media: local, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	private, _ := libraries(t, service, alice)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	xmp := fmt.Sprintf(`<x xmlns:g="http://ns.google.com/photos/1.0/camera/" g:MicroVideo="1" g:MicroVideoOffset="%d"/>`, len(clip))
	for _, kind := range []string{"heic", "live", "android"} {
		t.Run(kind, func(t *testing.T) {
			request := photos.ImportRequest{LibraryID: private.ID, Name: "live.heic", Content: bytes.NewReader(still)}
			if kind == "live" {
				request.MotionName = "live.mov"
				request.MotionContent = bytes.NewReader(clip)
			}
			if kind == "android" {
				request.Name = "android.jpg"
				request.Content = bytes.NewReader(androidJPEG(t, xmp, clip))
			}
			asset, err := service.Import(ctx, alice, request)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "android" && (asset.Width != 640 || asset.Height != 480) {
				t.Fatalf("HEIC dimensions: %+v", asset)
			}
			if _, err := service.ProcessMediaJob(ctx); err != nil {
				t.Fatal(err)
			}
			preview, err := service.Preview(ctx, alice, asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			config, format, err := image.DecodeConfig(preview.Reader)
			preview.Reader.Close()
			if err != nil || format != "jpeg" || config.Width == 0 {
				t.Fatalf("preview %+v %s %v", config, format, err)
			}
			thumb, err := service.Thumbnail(ctx, alice, asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			thumb.Reader.Close()
			if kind == "heic" {
				return
			}
			stream, err := service.PlayMotion(ctx, alice, asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(io.LimitReader(stream, 4<<20))
			stream.Close()
			if err != nil || len(data) < 32 {
				t.Fatalf("playback: %d bytes %v", len(data), err)
			}
			output := filepath.Join(dir, kind+".mp4")
			if err := os.WriteFile(output, data, 0600); err != nil {
				t.Fatal(err)
			}
			file, _ := os.Open(output)
			defer file.Close()
			probe, err := local.Probe(ctx, file)
			if err != nil {
				t.Fatal(err)
			}
			if len(probe.Streams) == 0 || probe.Streams[0].Codec != "h264" {
				t.Fatalf("browser playback codec: %+v", probe)
			}
		})
	}
}
