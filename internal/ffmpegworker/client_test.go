package ffmpegworker

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
)

// A descriptor open for writing never reaches the worker (ADR 0017): the
// request is refused before it takes a slot or connects.
func TestWritableInputsAreRefusedBeforeDialing(t *testing.T) {
	t.Parallel()
	readable := openFile(t, "")
	writable, err := os.OpenFile(readable.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writable.Close() }()
	dial := func(context.Context, string) (*net.UnixConn, func(), error) {
		t.Fatal("a request with a writable input dialled the worker")
		return nil, nil, nil
	}
	if _, _, err := send(context.Background(), dial, OpAssemble, Request{Op: OpAssemble, Ack: true}, []*os.File{readable, writable}); err == nil || !strings.Contains(err.Error(), "open for writing") {
		t.Fatalf("send() = %v, want the writable input refused", err)
	}
	if err := ReadOnly(readable); err != nil {
		t.Fatalf("ReadOnly(read-only) = %v", err)
	}
}

// A missing input is an error, not a panic: in a background job a panic would
// take the whole service down.
func TestMissingInputIsRefusedBeforeDialing(t *testing.T) {
	t.Parallel()
	dial := func(context.Context, string) (*net.UnixConn, func(), error) {
		t.Fatal("a request with a missing input dialled the worker")
		return nil, nil, nil
	}
	if _, _, err := send(context.Background(), dial, OpProbe, Request{Op: OpProbe}, []*os.File{nil}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("send() = %v, want the missing input refused", err)
	}
}
