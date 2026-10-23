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
	worker := exec.Command(python, "-m", "anas_ai.worker", "--fake", "--fake-detector", "--fake-faces", "--fake-animals",
		"--socket", socket, "--idle-seconds", "60")
	worker.Dir = filepath.Join("..", "..", "ai")
	worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	worker.Stderr = os.Stderr
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill() })
	// bind() creates the socket file before listen(), and a connection in
	// between is refused, so wait until one is accepted. The Worker drops
	// this empty connection with a warning and serves the next.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Python Worker did not start listening: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	client := aiworker.Client{SocketPath: socket}
	info, err := client.Info(context.Background())
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
	if err := worker.Wait(); err != nil {
		t.Fatalf("the Worker did not exit cleanly after releasing everything: %v", err)
	}
}
