package aiworker_test

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/aiworker"
)

// The Go client and the Python Worker in ai/ must agree on framing, vector
// encoding and descriptor passing; the fake provider needs no model.
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
	worker := exec.Command(python, "-m", "anas_ai.worker", "--fake", "--socket", socket, "--idle-seconds", "60")
	worker.Dir = filepath.Join("..", "..", "ai")
	worker.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	worker.Stderr = os.Stderr
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Python Worker did not start listening")
		}
		time.Sleep(20 * time.Millisecond)
	}

	client := aiworker.Client{SocketPath: socket}
	info, err := client.Info(context.Background())
	if err != nil || info.Model != "fake-sha256-768" || info.Dimensions != 768 {
		t.Fatalf("Info() = %+v, %v", info, err)
	}
	embed := func(content string) []float32 {
		t.Helper()
		path := filepath.Join(t.TempDir(), "thumbnail.jpg")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		vector, err := client.EmbedImage(context.Background(), file)
		if err != nil {
			t.Fatalf("EmbedImage() error = %v", err)
		}
		return vector
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
}
