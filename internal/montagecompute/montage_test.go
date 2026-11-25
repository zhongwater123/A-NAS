//go:build linux

package montagecompute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montagecompute"
)

// startWorker starts the Python Worker on a socket of its own, bound before
// it starts as its unit's socket is, after prelude (Python with the Worker's
// module imported as worker); or skips without the AI runtime's python.
func startWorker(t *testing.T, prelude string) montagecompute.Client {
	t.Helper()
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
	inherited, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	program := "from anas_montage import worker\n" + prelude + "\nworker.main([])"
	worker := exec.Command("sh", "-c", `export LISTEN_PID=$$; exec "$0" "$@"`, python, "-c", program)
	worker.Dir = filepath.Join("..", "..", "ai")
	worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "LISTEN_FDS=1")
	worker.ExtraFiles = []*os.File{inherited}
	worker.Stderr = os.Stderr
	// The Worker ends with the test binary, which a test timeout ends without cleanups.
	worker.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait(); _ = listener.Close(); _ = inherited.Close() })
	return montagecompute.Client{SocketPath: socket}
}

// The montage service's calls (montage.Compute) against the Worker: the
// case is ai/tests/test_montage_worker.py's tiny_case, one picture of two
// two-second slots from a card of two beats.
func TestTheMontageServicesCallsReachTheWorker(t *testing.T) {
	client := startWorker(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	card := montage.Card{ID: "c1", Video: "v", Start: 0, End: 20, Times: []float64{3, 12}, Frames: []string{"f1", "f2"},
		Answer: json.RawMessage(`{"beats":[{"shot_size":"远景","what":"海"},{"shot_size":"全景","what":"船"}]}`)}
	usable, err := client.Catalog(ctx, montage.CatalogRequest{Cards: []montage.Card{card}, Measures: map[string]json.RawMessage{}})
	if err != nil || len(usable) != 1 || usable[0] != "c1" {
		t.Fatalf("Catalog() = %v, %v", usable, err)
	}

	const dims = 8
	request := montage.FillRequest{FPS: 30, Cards: map[string]montage.Card{"c1": card}, Catalog: []string{"c1"},
		Music:   json.RawMessage(`{"parts":[{"level":"主歌"}]}`),
		Planned: json.RawMessage(`{"answer":{"parts":[{"name":"开场","pictures":[{"beats":["c1/1","c1/2"],"shows":"海","link":""}]}]},"slots":[[[[0.0,2.0],[2.0,4.0]]]]}`),
		Picked:  json.RawMessage(`{"parts":[{"scenes":[{"cards":["c1"],"query":"海边"}]}]}`),
		Known:   map[string][2]float64{}}
	var vectors []float32
	for i := range 10 {
		request.Rows = append(request.Rows, montage.FillRow{Video: "v", Start: float64(2 * i), End: float64(2*i + 2)})
		row := make([]float32, dims)
		var norm float64
		for d := range dims {
			row[d] = 0.1
			if d == i {
				row[d] = 1.1
			}
			norm += float64(row[d] * row[d])
		}
		for d := range row {
			row[d] /= float32(math.Sqrt(norm))
		}
		vectors = append(vectors, row...)
	}
	var answer montage.FillAnswer
	for range 10 {
		if answer, err = client.Fill(ctx, request, vectors, dims); err != nil {
			t.Fatal(err)
		}
		if answer.Need == nil {
			break
		}
		// The keyframe's whole span is clear.
		key := make([]byte, montage.FrameWidth*montage.FrameHeight)
		after := make([]byte, 3*len(key))
		span, err := client.Clear(ctx, montage.ClearRequest{Key: answer.Need.Key, Lo: answer.Need.Lo, Hi: answer.Need.Hi}, key, after, nil, 3, 0)
		if err != nil {
			t.Fatal(err)
		}
		if span != [2]float64{answer.Need.Lo, answer.Need.Hi} {
			t.Fatalf("Clear() = %v for %+v", span, answer.Need)
		}
		request.Known[answer.Need.Name] = span
	}
	if answer.Need != nil || len(answer.Short) != 0 || len(answer.Clips) == 0 {
		t.Fatalf("Fill() = %+v", answer)
	}
	frames := 0
	for _, raw := range answer.Clips {
		var clip montage.Clip
		if err := json.Unmarshal(raw, &clip); err != nil {
			t.Fatal(err)
		}
		frames += int(math.Round(clip.Share * 30))
	}
	if frames != 120 {
		t.Fatalf("the clips take %d frames: %s", frames, answer.Clips)
	}

	// Pinned the other way round (#596), each slot is led by its pin.
	request.Pins = []montage.FillPin{{Part: 1, Picture: 1, Slot: [2]float64{0, 2}, Beat: "c1/2"}, {Part: 1, Picture: 1, Slot: [2]float64{2, 4}, Beat: "c1/1"}}
	request.Known = map[string][2]float64{}
	for range 10 {
		if answer, err = client.Fill(ctx, request, vectors, dims); err != nil {
			t.Fatal(err)
		}
		if answer.Need == nil {
			break
		}
		request.Known[answer.Need.Name] = [2]float64{answer.Need.Lo, answer.Need.Hi}
	}
	var leads []string
	for _, raw := range answer.Clips {
		var clip struct {
			Beat string     `json:"beat"`
			Slot [2]float64 `json:"slot"`
		}
		if err := json.Unmarshal(raw, &clip); err != nil {
			t.Fatal(err)
		}
		leads = append(leads, fmt.Sprintf("%s@%v", clip.Beat, clip.Slot))
	}
	if answer.Need != nil || len(answer.Short) != 0 || !slices.Equal(leads, []string{"c1/2@[0 2]", "c1/1@[2 4]"}) {
		t.Fatalf("Fill() with pins = %v, %+v", leads, answer)
	}
	request.Pins[1].Slot = [2]float64{2.5, 4}
	if _, err := client.Fill(ctx, request, vectors, dims); !errors.Is(err, montage.ErrWorkerRefused) {
		t.Fatalf("Fill() with a gap between pins = %v", err)
	}

	alike, err := client.Likeness(ctx, vectors[:2*dims], vectors[:dims], dims)
	if err != nil {
		t.Fatal(err)
	}
	want := 0.0
	for d := range dims {
		want += float64((vectors[d] + vectors[dims+d]) / 2 * vectors[d])
	}
	var norm float64
	for d := range dims {
		mean := float64((vectors[d] + vectors[dims+d]) / 2)
		norm += mean * mean
	}
	if want /= math.Sqrt(norm); math.Abs(alike-want) > 1e-5 {
		t.Fatalf("Likeness() = %v, want %v", alike, want)
	}
}
