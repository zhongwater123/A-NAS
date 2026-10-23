package decodepanic_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/imagetest"
)

// guarded runs decode as a decode guard does.
func guarded(decode func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("test decoder", recovered)
		}
	}()
	decode()
}

//go:noinline
func readPastTheEnd(data []byte, at int) byte { return data[at] }

func TestLogRecordsThePanicAndWhereItStarted(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	guarded(func() { readPastTheEnd([]byte("abc"), 3) })

	logged := panics()
	if len(logged) != 1 {
		t.Fatalf("logged %d panics, want 1", len(logged))
	}
	got := logged[0]
	if got.Level != slog.LevelError || got.Decoder != "test decoder" || !strings.Contains(got.Value, "index out of range [3] with length 3") {
		t.Fatalf("logged %+v", got)
	}
	// The stack starts at the function that panicked, not in the runtime
	// or the guard, and names no source file.
	if len(got.Stack) < 2 || !strings.HasPrefix(got.Stack[0], "github.com/zhongwater123/A-NAS/internal/decodepanic_test.readPastTheEnd:") {
		t.Fatalf("stack = %q", got.Stack)
	}
	for _, frame := range got.Stack {
		if strings.Contains(frame, ".go:") || strings.HasPrefix(frame, "/") {
			t.Fatalf("stack frame %q names a source file", frame)
		}
	}
}

func TestLogShortensTheStackAndThePanic(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	var deep func(int)
	deep = func(depth int) {
		if depth == 0 {
			panic(strings.Repeat("界", 200))
		}
		deep(depth - 1)
	}
	guarded(func() { deep(20) })

	logged := panics()
	if len(logged) != 1 || len(logged[0].Stack) != 8 {
		t.Fatalf("logged %+v, want one panic with 8 frames", logged)
	}
	if value := logged[0].Value; len(value) > 256+len("…") || !strings.HasSuffix(value, "界…") {
		t.Fatalf("panic = %q (%d bytes), want it cut to 256 bytes at a character", value, len(value))
	}
}
