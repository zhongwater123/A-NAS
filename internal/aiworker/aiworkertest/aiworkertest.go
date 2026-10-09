// Package aiworkertest runs fake AI Workers that speak the aiworker
// protocol, for tests of the photo service and its client.
package aiworkertest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/aiworker"
)

// Handler answers one request; the descriptor is the image sent with it, if
// any. A nil response closes the connection without answering, as a Worker
// that dies would.
type Handler func(aiworker.Request, *os.File) *aiworker.Response

// Serve listens on a fresh Unix socket until the test ends and returns its
// path.
func Serve(t testing.TB, handle Handler) string {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
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
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveOne(conn.(*net.UnixConn), handle)
		}
	}()
	return socket
}

func serveOne(conn *net.UnixConn, handle Handler) {
	defer conn.Close()
	buffer, control := make([]byte, aiworker.MaxFrame+4), make([]byte, unix.CmsgSpace(4))
	n, controlLength, _, _, err := conn.ReadMsgUnix(buffer, control)
	if err != nil || n < 4 {
		return
	}
	var file *os.File
	if messages, err := unix.ParseSocketControlMessage(control[:controlLength]); err == nil && len(messages) == 1 {
		if fds, err := unix.ParseUnixRights(&messages[0]); err == nil && len(fds) == 1 {
			file = os.NewFile(uintptr(fds[0]), "image")
			defer file.Close()
		}
	}
	length := binary.BigEndian.Uint32(buffer)
	if int(length) > n-4 {
		return
	}
	var request aiworker.Request
	if err := json.Unmarshal(buffer[4:4+length], &request); err != nil {
		return
	}
	response := handle(request, file)
	if response == nil {
		return
	}
	payload, _ := json.Marshal(response)
	_, _ = conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...))
}

// Deterministic is a Worker whose image vectors come from the image bytes,
// so equal images get equal vectors.
func Deterministic(model string, dimensions int) Handler {
	return func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			return &aiworker.Response{OK: true, Model: model, Dimensions: dimensions}
		case aiworker.OpEmbedImage:
			if file == nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			content, err := io.ReadAll(file)
			if err != nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: err.Error()}}
			}
			sum := sha256.Sum256(content)
			vector := make([]float32, dimensions)
			for i := range vector {
				vector[i] = float32(sum[i%len(sum)]) / 255
			}
			return &aiworker.Response{OK: true, Model: model, Dimensions: dimensions, Vector: Encode(vector)}
		}
		return &aiworker.Response{Error: &aiworker.WireError{Code: "unknown_op", Message: request.Op}}
	}
}

// Encode returns a vector as the protocol carries it.
func Encode(vector []float32) []byte {
	encoded := make([]byte, 0, 4*len(vector))
	for _, value := range vector {
		encoded = binary.LittleEndian.AppendUint32(encoded, math.Float32bits(value))
	}
	return encoded
}
