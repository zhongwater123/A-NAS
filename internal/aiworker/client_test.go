package aiworker_test

import (
	"context"
	"errors"
	"io"
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
