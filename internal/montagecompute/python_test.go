//go:build linux

package montagecompute_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montagecompute"
)

// The Go client and the Python Worker in ai/anas_montage must agree on
// framing, arrays in memfds and answers. The Worker needs NumPy and Pillow,
// which the AI runtime has: ANAS_MONTAGE_PYTHON names its python, or the
// test looks for one on PATH that imports them.
func TestPythonWorkerSpeaksTheProtocol(t *testing.T) {
	python := os.Getenv("ANAS_MONTAGE_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import numpy, PIL").Run(); err != nil {
		t.Skipf("%s cannot import NumPy and Pillow: the AI runtime's python can (ANAS_MONTAGE_PYTHON)", python)
	}
	dir, err := os.MkdirTemp("", "compute")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "compute.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	inherited, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	start := func() *exec.Cmd {
		worker := exec.Command("sh", "-c", `export LISTEN_PID=$$; exec "$0" "$@"`, python, "-m", "anas_montage.worker")
		worker.Dir = filepath.Join("..", "..", "ai")
		worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "LISTEN_FDS=1")
		worker.ExtraFiles = []*os.File{inherited}
		worker.Stderr = os.Stderr
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		return worker
	}
	worker := start()
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = listener.Close(); _ = inherited.Close() })
	client := montagecompute.Client{SocketPath: socket}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Cards of 20 windows: two scenes, one flat window at the end.
	const dims = 8
	windows := make([]montagecompute.Window, 20)
	vectors := make([]float32, 20*dims)
	flat := make([]bool, 20)
	for i := range windows {
		windows[i] = montagecompute.Window{Start: float64(2 * i), End: float64(2*i + 2)}
		scene := 0
		if i >= 10 {
			scene = 1
		}
		vectors[i*dims+scene] = 1
	}
	flat[19] = true
	cards, err := client.Cards(ctx, "v", windows, vectors, dims, flat)
	if err != nil || len(cards) != 2 || cards[0].Start != 0 || cards[1].End != 38 || len(cards[0].Times) < 3 {
		t.Fatalf("Cards() = %+v, %v", cards, err)
	}

	// A keyframe as a JPEG, and a grey frame for its sharp points.
	img := image.NewRGBA(image.Rect(0, 0, 512, 288))
	for y := range 288 {
		for x := range 512 {
			img.Set(x, y, color.RGBA{R: 30, G: 30, B: 40, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	measure, err := client.Keyframe(ctx, buffer.Bytes())
	if err != nil || measure.Light > 60 {
		t.Fatalf("Keyframe() = %+v, %v", measure, err)
	}
	grey := make([]byte, 64*36)
	grey[36*64/2+32] = 255
	points, err := client.SharpPoints(ctx, grey, 64, 36)
	if err != nil || points <= 0 {
		t.Fatalf("SharpPoints() = %v, %v", points, err)
	}
	shake := make([]byte, 3*montagecompute.ShakeHeight*montagecompute.ShakeWidth)
	for i := range shake {
		shake[i] = byte(i * 31 % 251)
	}
	moves, err := client.ShakeMoves(ctx, shake, 3)
	if err != nil || len(moves) != 2 {
		t.Fatalf("ShakeMoves() = %v, %v", moves, err)
	}
	blur := 3.0
	quality, err := client.Quality(ctx, montagecompute.QualityInput{Moves: moves, Blurs: []*float64{&blur, &blur, &blur},
		Blocked: []float64{0, 0, 0}, Points: []*float64{nil, nil, nil}})
	if err != nil || quality.Blur == nil || math.Abs(*quality.Blur-3) > 1e-9 || len(quality.Reasons) != 0 {
		t.Fatalf("Quality() = %+v, %v", quality, err)
	}

	// A refused request, then the Worker killed: the next connection starts
	// a new one, as systemd starts the unit again.
	if _, err := client.SharpPoints(ctx, grey, 64, 35); !errors.Is(err, montagecompute.ErrRefused) {
		t.Fatalf("a frame of the wrong size = %v", err)
	}
	_ = worker.Process.Kill()
	_ = worker.Wait()
	worker = start()
	if _, err := client.Keyframe(ctx, buffer.Bytes()); err != nil {
		t.Fatalf("after the Worker was killed: %v", err)
	}
}
