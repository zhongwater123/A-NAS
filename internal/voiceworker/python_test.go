package voiceworker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/voiceworker"
)

// The Go client and the Python Worker in ai/ must agree on framing, the
// samples' descriptor, segments and vector encoding; the fake needs no model.
func TestPythonWorkerSpeaksTheProtocol(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	dir, err := os.MkdirTemp("", "voice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "voice.sock")
	worker := exec.Command(python, "-m", "anas_ai.voice", "--fake", "--socket", socket, "--idle-seconds", "60")
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
	client := voiceworker.Client{SocketPath: socket}
	ctx := context.Background()
	info, err := client.Info(ctx)
	if err != nil || info.Model != "fake-speakers-sha256" || info.Dimensions != 32 || info.SampleRate != 16000 {
		t.Fatalf("Info() = %+v, %v", info, err)
	}
	second := voiceworker.SampleRate
	samples := make([]int16, 4*second)
	for i := range samples {
		samples[i] = 1000
		if i >= 2*second {
			samples[i] = -2000
		}
	}
	model, vectors, err := client.EmbedSpeakers(ctx, samples, [][]voiceworker.Segment{
		{{Offset: 0, Count: second}, {Offset: second, Count: second}},
		{{Offset: 2 * second, Count: 2 * second}},
	})
	if err != nil || model != "fake-speakers-sha256" || len(vectors) != 2 || len(vectors[0]) != 32 {
		t.Fatalf("EmbedSpeakers() = %s %d vectors, %v", model, len(vectors), err)
	}
	if dot(vectors[0], vectors[0]) < 0.999 || dot(vectors[0], vectors[1]) > 0.9 {
		t.Fatalf("vectors are not unit vectors of two voices: %f %f", dot(vectors[0], vectors[0]), dot(vectors[0], vectors[1]))
	}
	model, labels, centroids, err := client.ClusterSpeech(ctx, samples, []voiceworker.Segment{
		{Offset: 0, Count: second}, {Offset: 2 * second, Count: second}, {Offset: second, Count: second},
	}, 0.3)
	if err != nil || len(labels) != 3 || labels[0] != labels[2] || labels[0] == labels[1] || len(centroids) != 2 {
		t.Fatalf("ClusterSpeech() = %s %v %d centroids, %v", model, labels, len(centroids), err)
	}
	if _, _, err := client.EmbedSpeakers(ctx, samples, [][]voiceworker.Segment{{{Offset: 3 * second, Count: 2 * second}}}); !errors.Is(err, voiceworker.ErrRejected) {
		t.Fatalf("a segment past the samples = %v", err)
	}
	if err := client.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err != nil {
		t.Fatalf("the Worker did not exit after release: %v", err)
	}
	if _, err := client.Info(ctx); !errors.Is(err, voiceworker.ErrUnavailable) {
		t.Fatalf("Info() after release = %v", err)
	}
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
