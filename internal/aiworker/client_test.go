package aiworker_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/aiworker"
	"github.com/zhongwater123/A-NAS/internal/aiworker/aiworkertest"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestInfoAndImageEmbeddingCrossTheSocket(t *testing.T) {
	socket := aiworkertest.Serve(t, func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			return &aiworker.Response{OK: true, Model: "fake-1", Dimensions: 2}
		case aiworker.OpEmbedImage:
			if file == nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			content, _ := io.ReadAll(file)
			return &aiworker.Response{OK: true, Vector: aiworkertest.Encode([]float32{float32(len(content)), 0.5})}
		case aiworker.OpEmbedQuery:
			if file != nil {
				return nil
			}
			return &aiworker.Response{OK: true, Vector: aiworkertest.Encode([]float32{float32(len([]rune(request.Text))), 1})}
		}
		return nil
	})
	client := aiworker.Client{SocketPath: socket}

	info, err := client.Info(context.Background())
	if err != nil || info != (photos.EmbedderInfo{Model: "fake-1", Dimensions: 2}) {
		t.Fatalf("Info() = %+v, %v", info, err)
	}
	image := filepath.Join(t.TempDir(), "thumbnail.jpg")
	if err := os.WriteFile(image, []byte("seven b"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	vector, err := client.EmbedImage(context.Background(), file)
	if err != nil || len(vector) != 2 || vector[0] != 7 || vector[1] != 0.5 {
		t.Fatalf("EmbedImage() = %v, %v; want the Worker to read the 7 bytes behind the descriptor", vector, err)
	}
	vector, err = client.EmbedQuery(context.Background(), "海边的猫")
	if err != nil || len(vector) != 2 || vector[0] != 4 {
		t.Fatalf("EmbedQuery() = %v, %v; want the Worker to receive the four characters", vector, err)
	}
}

func TestOnlyAnUnreachableWorkerIsUnavailable(t *testing.T) {
	missing := aiworker.Client{SocketPath: filepath.Join(t.TempDir(), "absent.sock")}
	if _, err := missing.Info(context.Background()); !errors.Is(err, photos.ErrAIUnavailable) {
		t.Fatalf("Info() without a Worker error = %v, want ErrAIUnavailable", err)
	}

	socket := aiworkertest.Serve(t, func(request aiworker.Request, _ *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeUnavailable, Message: "model file missing"}}
		default:
			return nil // the Worker dies after reading the request
		}
	})
	client := aiworker.Client{SocketPath: socket}
	if _, err := client.Info(context.Background()); !errors.Is(err, photos.ErrAIUnavailable) {
		t.Fatalf("Info() without a model error = %v, want ErrAIUnavailable", err)
	}
	_, err := client.EmbedImage(context.Background(), os.Stdin)
	if err == nil || errors.Is(err, photos.ErrAIUnavailable) {
		t.Fatalf("EmbedImage() after the Worker died error = %v; it must spend an attempt", err)
	}
}

func TestRejectedInputAndTimeouts(t *testing.T) {
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	socket := aiworkertest.Serve(t, func(request aiworker.Request, _ *os.File) *aiworker.Response {
		if request.Op == aiworker.OpInfo {
			<-stall
			return nil
		}
		return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "not an image"}}
	})
	client := aiworker.Client{SocketPath: socket, Timeout: 200 * time.Millisecond}

	if _, err := client.EmbedImage(context.Background(), os.Stdin); !errors.Is(err, photos.ErrAIRejected) {
		t.Fatalf("EmbedImage() of a bad input error = %v, want ErrAIRejected", err)
	}
	started := time.Now()
	if _, err := client.Info(context.Background()); err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("Info() from a stalled Worker = %v after %s; want a timeout", err, time.Since(started))
	}
}

func TestDetectionAndReleaseCrossTheSocket(t *testing.T) {
	var released []string
	socket := aiworkertest.Serve(t, func(request aiworker.Request, file *os.File) *aiworker.Response {
		if request.Op == aiworker.OpRelease {
			released = append(released, request.What)
		}
		return aiworkertest.WithDetector(aiworkertest.Deterministic("fake-1", 2), "detector-1")(request, file)
	})
	client := aiworker.Client{SocketPath: socket}
	info, err := client.Info(context.Background())
	if err != nil || info.Detector != "detector-1" {
		t.Fatalf("Info() = %+v, %v; want the detector named", info, err)
	}
	image := filepath.Join(t.TempDir(), "input.jpg")
	content := []byte("an upright photo with things in it")
	if err := os.WriteFile(image, content, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	model, boxes, err := client.DetectObjects(context.Background(), file)
	want := aiworkertest.HashBoxes(content)
	if err != nil || model != "detector-1" || len(boxes) != len(want) {
		t.Fatalf("DetectObjects() = %q, %v, %v; want %d boxes", model, boxes, err, len(want))
	}
	for i, box := range boxes {
		if box.Class != int(want[i][0]) || box.Score != float32(want[i][1]) || box.X1 != float32(want[i][4]) {
			t.Fatalf("box %d = %+v, want %v", i, box, want[i])
		}
	}
	if err := client.Release(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := client.Release(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(released) != 2 || released[0] != "detector" || released[1] != "all" {
		t.Fatalf("released %v", released)
	}

	malformed := aiworkertest.Serve(t, func(aiworker.Request, *os.File) *aiworker.Response {
		return &aiworker.Response{OK: true, Model: "detector-1", Boxes: [][]float64{{1.5, 0.9, 0, 0, 1, 1}}}
	})
	if _, _, err := (aiworker.Client{SocketPath: malformed}).DetectObjects(context.Background(), file); err == nil {
		t.Fatal("DetectObjects() accepted a box whose class is not a whole number")
	}
}

// The photo service stops a detection when someone starts using the device by
// cancelling its context: the client hangs up, which the Worker watches for.
func TestCancellingADetectionHangsUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "ai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ai.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	hungUp := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the request, then wait for the client to go away.
		_, _ = io.Copy(io.Discard, conn)
		close(hungUp)
	}()
	image := filepath.Join(t.TempDir(), "input.jpg")
	if err := os.WriteFile(image, []byte("photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, _, err = (aiworker.Client{SocketPath: socket}).DetectObjects(ctx, file)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DetectObjects() error = %v, want context.Canceled", err)
	}
	select {
	case <-hungUp:
	case <-time.After(5 * time.Second):
		t.Fatal("the Worker never saw the client hang up")
	}
}
