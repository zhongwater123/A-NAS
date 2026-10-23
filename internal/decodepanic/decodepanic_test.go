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
	if got.Level != slog.LevelError || got.Decoder != "test decoder" || got.Type != "runtime.boundsError" ||
		!strings.Contains(got.Value, "index out of range [3] with length 3") {
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

func TestLogKeepsTheInputADecoderQuotesOutOfTheLog(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	guarded(func() { panic("bad frame in 体检报告 by alice") })
	guarded(func() { panic(fmtError("tag 体检报告")) })

	logged := panics()
	if len(logged) != 2 || logged[0].Type != "string" || logged[1].Type != "decodepanic_test.fmtError" {
		t.Fatalf("logged %+v, want a string and a fmtError panic", logged)
	}
	for _, got := range logged {
		for _, field := range got.Fields {
			if strings.Contains(field, "体检报告") {
				t.Fatalf("logged field %q quotes the input", field)
			}
		}
	}
}

type fmtError string

func (e fmtError) Error() string { return string(e) }

func TestLogShortensTheStack(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	var deep func(int)
	deep = func(depth int) {
		if depth == 0 {
			panic("deep")
		}
		deep(depth - 1)
	}
	guarded(func() { deep(20) })

	if logged := panics(); len(logged) != 1 || len(logged[0].Stack) != 8 {
		t.Fatalf("logged %+v, want one panic with 8 frames", logged)
	}
}

func TestLogCountsRepeatsOfAPanicInsteadOfLoggingThem(t *testing.T) {
	panics := imagetest.LoggedPanics(t)
	// The same path with another length is a repeat too; another path is
	// not.
	for _, at := range []int{1, 1, 1, 5} {
		guarded(func() { readPastTheEnd(nil, at) })
	}
	guarded(func() { panic("elsewhere") })

	logged := panics()
	if len(logged) != 2 || logged[0].Type != "runtime.boundsError" || logged[1].Type != "string" {
		t.Fatalf("logged %+v, want the first bounds error and the other panic", logged)
	}
}
