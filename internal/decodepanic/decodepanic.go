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
)

// Message is the message of the records Log writes.
const Message = "decoder panicked; the input is treated as undecodable"

const (
	// maxFrames keeps the stack to where the panic started.
	maxFrames = 8
	// maxValue bounds the panic's message, since decoders may quote input.
	maxValue = 256
)

// Log records value, a panic recovered while decoding what, at error level on
// the default logger, with up to eight stack frames from where the panic
// started. Call it from the deferred function that called recover.
//
// Administrators read the system log and must not learn members' file names
// (docs/specs/logs.md), so what names the decoder, such as "media artwork",
// never the file, and frames are functions and line numbers without source
// paths. Decoders get bytes or open files rather than names, so the panic's
// message carries no name either.
func Log(what string, value any) {
	slog.Error(Message, "decoder", what, "panic", truncate(fmt.Sprint(value)), "stack", stack())
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

func truncate(value string) string {
	if len(value) <= maxValue {
		return value
	}
	return strings.ToValidUTF8(value[:maxValue], "") + "…"
}
