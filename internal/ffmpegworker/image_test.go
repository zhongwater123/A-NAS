package ffmpegworker

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestImageUsesWorkerOwnedMemoryAndBoundedSize(t *testing.T) {
	input := openFile(t, "image bytes")
	if err := input.Chmod(0); err != nil {
		t.Fatal(err)
	}
	local := Local{Tools: Tools{HEIFThumbnailer: script(t, `test "$1" = -p && test "$2" = -s && test "$3" = 320 && cat "$4" > "$5"`)}}
	data, err := local.Image(context.Background(), input, 320)
	if err != nil || string(data) != "image bytes" {
		t.Fatalf("Image = %q, %v", data, err)
	}
	if _, err := local.Image(context.Background(), input, 99999); !errors.Is(err, ErrInvalid) {
		t.Fatalf("size bound = %v", err)
	}
	large := openFile(t, "")
	if err := os.Truncate(large.Name(), 257<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Image(context.Background(), large, 320); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("input bound = %v", err)
	}
}

// Runs in Debian with heif-thumbnailer and test-only libheif-examples / x265.
// The encoder creates the fixture; no personal photos or remote fixtures are used.
func TestRealHEIF(t *testing.T) {
	decoder, err := exec.LookPath("heif-thumbnailer")
	if err != nil {
		t.Skip("heif-thumbnailer is not installed")
	}
	encoder, err := exec.LookPath("heif-enc")
	if err != nil {
		t.Skip("heif-enc is not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.png")
	target := filepath.Join(dir, "photo.heic")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 640, 480))); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if output, err := exec.Command(encoder, "-o", target, source).CombinedOutput(); err != nil {
		t.Fatalf("generate HEIC: %v %s", err, output)
	}
	input, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	local := Local{Tools: Tools{HEIFThumbnailer: decoder}}
	for _, size := range []int{320, 1600} {
		data, err := local.Image(context.Background(), input, size)
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "png" || config.Width > size || config.Height > size || config.Width*3 != config.Height*4 {
			t.Fatalf("preview %d: %+v %s %v", size, config, format, err)
		}
	}
	if _, err := local.Image(context.Background(), openFile(t, strings.Repeat("invalid", 10)), 320); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("corrupt HEIC: %v", err)
	}
}

func TestCancellingImageStopsDecoder(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	decoder := script(t, "echo $$ > "+marker+"; exec sleep 30")
	local := Local{Tools: Tools{HEIFThumbnailer: decoder}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	input := openFile(t, "x")
	go func() { _, err := local.Image(ctx, input, 320); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(marker)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("decoder did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled decode succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller did not cancel")
	}
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("decoder survived caller cancellation")
}
