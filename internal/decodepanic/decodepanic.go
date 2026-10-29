// Package decodepanic records the panics that decode guards recover from.
//
// Services parse user and host files in process with third-party decoders and
// treat a decoder panic as an undecodable input (invariant 18 in
// docs/architecture/OVERVIEW.md). Log keeps each such panic in the system log,
// so that a decoder bug, someone probing for one and a bug in A-NAS's own
// decoding code do not look like a corrupt file.
package decodepanic

import (
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is the message of the records Log writes.
const Message = "decoder panicked; the input is treated as undecodable"

const (
	// maxFrames keeps the stack to where the panic started.
	maxFrames = 8
	// quiet is how long a panic already logged is counted instead.
	quiet = 10 * time.Minute
)

// now is time.Now; tests move it.
var now = time.Now

// logged remembers when each panic was last logged, by decoder, type and
// stack. The keys are paths through the code, never input, so they are few.
var logged struct {
	sync.Mutex
	// to is the default logger the records went to: a new one, as in a
	// test, starts afresh.
	to   *slog.Logger
	keys map[string]*occurrence
}

type occurrence struct {
	at time.Time
	// repeats counts the panics since then that were not logged.
	repeats int
}

// Log records value, a panic recovered while decoding what, at error level on
// the default logger, with up to eight stack frames from where the panic
// started. Call it from the deferred function that called recover.
//
// Administrators read the system log and must learn neither members' file
// names nor what their files hold (docs/specs/logs.md). So what names the
// decoder, such as "media artwork", never the file; frames are functions and
// line numbers without source paths; and of the value only its type is
// logged, with the message of a runtime error, which states lengths and types
// but never quotes input. A decoder's own panic message may quote the bytes
// it was reading.
//
// A panic of the same type along the same stack, as a repeated request
// raises, is logged once every ten minutes, with the number of repeats since
// the last record, so that requesting one broken file over and over does not
// flood the log.
func Log(what string, value any) {
	frames := stack()
	kind := fmt.Sprintf("%T", value)
	message := ""
	if err, ok := value.(runtime.Error); ok {
		message = err.Error()
	}
	// Not the message: its lengths come from the input.
	repeats, ok := due(what + "\x00" + kind + "\x00" + strings.Join(frames, "\n"))
	if !ok {
		return
	}
	attrs := []any{"decoder", what, "panic_type", kind}
	if message != "" {
		attrs = append(attrs, "panic", message)
	}
	attrs = append(attrs, "stack", frames)
	if repeats > 0 {
		attrs = append(attrs, "repeats", repeats)
	}
	slog.Error(Message, attrs...)
}

// due reports whether the panic of key is to be logged now, and how many of
// its repeats went unlogged before.
func due(key string) (repeats int, ok bool) {
	logged.Lock()
	defer logged.Unlock()
	if to := slog.Default(); logged.to != to {
		logged.to, logged.keys = to, map[string]*occurrence{}
	}
	at := now()
	last := logged.keys[key]
	if last != nil && at.Sub(last.at) < quiet {
		last.repeats++
		return 0, false
	}
	if last != nil {
		repeats = last.repeats
	}
	logged.keys[key] = &occurrence{at: at}
	return repeats, true
}

// stack returns the frames below the panic as function:line. Log runs in the
// deferred function above runtime.gopanic; the runtime frames right below it
// raised the panic, as for an index out of range or a nil pointer.
func stack() []string {
	var pcs [64]uintptr
	// Skip runtime.Callers, stack and Log.
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	var lines []string
	panicking := false
	for len(lines) < maxFrames {
		frame, more := frames.Next()
		switch {
		case !panicking:
			panicking = frame.Function == "runtime.gopanic"
		case len(lines) > 0 || !strings.HasPrefix(frame.Function, "runtime."):
			lines = append(lines, frame.Function+":"+strconv.Itoa(frame.Line))
		}
		if !more {
			break
		}
	}
	return lines
}
