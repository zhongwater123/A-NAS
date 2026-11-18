//go:build linux

package montagecompute_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montagecompute"
)

// pythonWorker starts the compute Worker as its unit does, after prelude, and
// returns its socket. Needs the AI runtime's python, as
// TestPythonWorkerSpeaksTheProtocol.
func pythonWorker(t *testing.T, prelude string) string {
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
	worker := exec.Command(python, "-c", "import sys\nfrom anas_montage import worker\n"+prelude+"\nworker.main(['--socket', sys.argv[1]])", socket)
	worker.Dir = filepath.Join("..", "..", "ai")
	worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	worker.Stderr = os.Stderr
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	for deadline := time.Now().Add(20 * time.Second); ; {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Worker did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return socket
}

// song is a downbeat every two seconds for 200 seconds, the choruses louder.
func song() (montage.Structure, []float32) {
	structure := montage.Structure{BPM: 120}
	for k := 0; k <= 100; k++ {
		structure.Downbeats = append(structure.Downbeats, float64(2*k))
		structure.Beats = append(structure.Beats, float64(2*k))
	}
	for _, s := range [][3]any{{"intro", 0, 16}, {"verse", 16, 48}, {"chorus", 48, 80}, {"verse", 80, 112}, {"chorus", 112, 144}, {"outro", 144, 200}} {
		structure.Segments = append(structure.Segments, montage.Section{Label: s[0].(string), Start: float64(s[1].(int)), End: float64(s[2].(int))})
	}
	loudness := make([]float32, 200*22050/512)
	for i := range loudness {
		at := float64(i) * 512 / 22050
		loudness[i] = 0.1
		if at >= 48 && at < 80 || at >= 112 && at < 144 {
			loudness[i] = 0.2
		}
	}
	return structure, loudness
}

// The music operation through the real Worker (ai/anas_montage/music_ops.py),
// as worker.py's OPS names it.
func TestTheMusicOperationAnswersFromPython(t *testing.T) {
	client := montagecompute.Client{SocketPath: pythonWorker(t, "")}
	structure, loudness := song()
	ctx := context.Background()
	selection, err := client.Music(ctx, montage.MusicRequest{Structure: structure, Loudness: loudness, Target: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Chorus || selection.Unusable != "" || selection.Seconds < 30 || selection.Seconds > 150 || len(selection.Parts) == 0 {
		t.Fatalf("selection = %+v", selection)
	}
	start := 21.0 // Equidistant downbeats snap to the earlier one, as in montage.nearest.
	dragged, err := client.Music(ctx, montage.MusicRequest{Structure: structure, Loudness: loudness, Target: 90, Start: &start})
	if err != nil || dragged.Excerpt != [2]float64{20, 110} || dragged.Seconds != 90 {
		t.Fatalf("dragged = %+v, %v", dragged, err)
	}
	broken := structure
	broken.Downbeats = []float64{3, 1}
	if _, err := client.Music(ctx, montage.MusicRequest{Structure: broken, Loudness: loudness, Target: 90}); !errors.Is(err, montage.ErrWorkerRefused) {
		t.Fatalf("a broken structure = %v; want refused", err)
	}
}

// While the device is updated, a compute Worker from before the operation
// existed may still be running. It is one to wait for: its refusal says
// nothing of the song.
func TestAWorkerWithoutTheMusicOperationIsWaitedFor(t *testing.T) {
	client := montagecompute.Client{SocketPath: pythonWorker(t, "del worker.OPS['music']")}
	structure, loudness := song()
	_, err := client.Music(context.Background(), montage.MusicRequest{Structure: structure, Loudness: loudness, Target: 90})
	if !errors.Is(err, montage.ErrWorkerUnavailable) || errors.Is(err, montage.ErrWorkerRefused) {
		t.Fatalf("Music() without the operation = %v; want unavailable", err)
	}
}
