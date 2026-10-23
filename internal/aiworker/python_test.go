package aiworker_test

import (
	"context"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/aiworker"
	"github.com/zhongwater123/A-NAS/internal/aiworker/aiworkertest"
)

// The Go client and the Python Worker in ai/ must agree on framing, vector
// and box encoding, descriptor passing and release; the fakes need no model.
func TestPythonWorkerSpeaksTheProtocol(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	dir, err := os.MkdirTemp("", "ai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ai.sock")
	// The Worker gets the socket already listening, as systemd's anas-ai.socket
	// passes it in production, so connections queue until it accepts them.
	// LISTEN_PID must be the Worker's own, which only the shell it execs from knows.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	inherited, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	worker := exec.Command("sh", "-c", `export LISTEN_PID=$$; exec "$0" "$@"`, python, "-m", "anas_ai.worker",
		"--fake", "--fake-detector", "--fake-faces", "--fake-animals", "--idle-seconds", "60")
	worker.Dir = filepath.Join("..", "..", "ai")
	worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "LISTEN_FDS=1")
	worker.ExtraFiles = []*os.File{inherited}
	worker.Stderr = os.Stderr
	err = worker.Start()
	// Only the Worker holds the socket now: if it dies, connections are
	// refused at once instead of waiting in the queue.
	_ = inherited.Close()
	_ = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill() })
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()

	client := aiworker.Client{SocketPath: socket}
	starting, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := client.Info(starting)
	if err != nil || info.Model != "fake-sha256-768" || info.Dimensions != 768 || info.Detector != "fake-detector-sha256" ||
		info.Faces != "fake-faces-sha256" || info.Animals != "fake-animals-sha256" {
		t.Fatalf("Info() = %+v, %v", info, err)
	}
	open := func(content string) *os.File {
		t.Helper()
		path := filepath.Join(t.TempDir(), "thumbnail.jpg")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	embed := func(content string) []float32 {
		t.Helper()
		vector, err := client.EmbedImage(context.Background(), open(content))
		if err != nil {
			t.Fatalf("EmbedImage() error = %v", err)
		}
		return vector
	}
	query := func(text string) []float32 {
		t.Helper()
		vector, err := client.EmbedQuery(context.Background(), text)
		if err != nil || len(vector) != 768 {
			t.Fatalf("EmbedQuery(%q) = %d values, %v", text, len(vector), err)
		}
		return vector
	}
	if a, b, c := query("海边的猫"), query("海边的猫"), query("车库里的电动车"); a[0] != b[0] || a[0] == c[0] {
		t.Fatal("equal queries must give equal vectors and different queries different ones")
	}
	first, again, other := embed("thumbnail"), embed("thumbnail"), embed("another thumbnail")
	var norm float64
	for _, value := range first {
		norm += float64(value) * float64(value)
	}
	if len(first) != 768 || math.Abs(norm-1) > 1e-4 {
		t.Fatalf("vector of %d values with squared norm %f", len(first), norm)
	}
	if first[0] != again[0] || first[767] != again[767] || first[0] == other[0] {
		t.Fatal("equal images must give equal vectors and different images different ones")
	}

	// The Python fake detector and aiworkertest derive the same boxes.
	for _, content := range []string{"photo 1", "photo 2", "photo 3", "photo 4", "photo 5"} {
		model, boxes, err := client.DetectObjects(context.Background(), open(content))
		want := aiworkertest.HashBoxes([]byte(content))
		if err != nil || model != "fake-detector-sha256" || len(boxes) != len(want) {
			t.Fatalf("DetectObjects(%q) = %q, %v, %v; want %d boxes", content, model, boxes, err, len(want))
		}
		for i, box := range boxes {
			if box.Class != int(want[i][0]) || math.Abs(float64(box.Score)-want[i][1]) > 1e-4 ||
				math.Abs(float64(box.X0)-want[i][2]) > 1e-4 || math.Abs(float64(box.Y1)-want[i][5]) > 1e-4 {
				t.Fatalf("DetectObjects(%q) box %d = %+v, want %v", content, i, box, want[i])
			}
		}
	}
	// Faces and animal vectors arrive whole: unit vectors, boxes in the image.
	unit := func(vector []float32) bool {
		var norm float64
		for _, value := range vector {
			norm += float64(value) * float64(value)
		}
		return math.Abs(norm-1) < 1e-3
	}
	faces := 0
	for _, content := range []string{"photo 1", "photo 2", "photo 3", "photo 4", "photo 5", "photo 6"} {
		model, found, err := client.DetectFaces(context.Background(), open(content))
		if err != nil || model != "fake-faces-sha256" {
			t.Fatalf("DetectFaces(%q) = %q, %v", content, model, err)
		}
		for _, face := range found {
			if len(face.Vector) != 128 || !unit(face.Vector) || face.Box[2] <= face.Box[0] || face.Landmarks[9] == 0 {
				t.Fatalf("DetectFaces(%q) face = %+v", content, face)
			}
		}
		faces += len(found)
	}
	if faces == 0 {
		t.Fatal("the fake found no faces in six photos")
	}
	model, vectors, err := client.EmbedAnimals(context.Background(), open("photo 1"), [][4]float32{{0.1, 0.2, 0.5, 0.9}, {0.5, 0, 1, 0.5}})
	if err != nil || model != "fake-animals-sha256" || len(vectors) != 2 || len(vectors[1]) != 64 || !unit(vectors[1]) {
		t.Fatalf("EmbedAnimals() = %q, %d vectors, %v", model, len(vectors), err)
	}

	if err := client.Release(context.Background(), false); err != nil {
		t.Fatalf("Release(detector) error = %v", err)
	}
	if err := client.Release(context.Background(), true); err != nil {
		t.Fatalf("Release(all) error = %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the Worker did not exit cleanly after releasing everything: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the Worker was still running 10 s after releasing everything")
	}
}
