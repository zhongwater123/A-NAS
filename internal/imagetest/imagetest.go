// Package imagetest gives tests of in-process image decoding a picture whose
// decoder panics, as a decoder with a bug may on a malformed picture, and a
// record of the panics decode guards log.
package imagetest

import (
	"context"
	"image"
	"image/color"
	"io"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// panicking does not start with a NUL byte: ID3 readers take one after a
// picture's description for part of the description.
var panicking = []byte("\x01anas-panicking-picture")

// PanicMessage is the panic of the PanickingPicture decoder.
const PanicMessage = "imagetest: decoder bug"

var register = sync.OnceFunc(func() {
	// The format calls itself webp, which every caller accepts, and its
	// header is a small picture, so callers get past their checks to Decode.
	image.RegisterFormat("webp", string(panicking[:8]),
		func(io.Reader) (image.Image, error) { panic(PanicMessage) },
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 64, Height: 48}, nil
		})
})

// PanickingPicture returns a picture whose header reads as a 64x48 WebP and
// whose decoder panics.
func PanickingPicture() []byte {
	register()
	return slices.Clone(panicking)
}

// Panic is a recovered panic that a decode guard logged.
type Panic struct {
	Decoder string
	Value   string
	Stack   []string
	Level   slog.Level
	// Fields are all of the record's attributes as key=value.
	Fields []string
}

// LoggedPanics records the panics decode guards log until the test ends and
// returns a function that lists them. It replaces the default logger, so
// tests that use it must not run in parallel.
func LoggedPanics(t testing.TB) func() []Panic {
	t.Helper()
	recorder := &recorder{}
	previous, output, flags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(recorder))
	t.Cleanup(func() {
		// SetDefault also routed the log package to the recorder.
		slog.SetDefault(previous)
		log.SetOutput(output)
		log.SetFlags(flags)
	})
	return recorder.list
}

// RequirePanicLogged fails the test unless logged holds panics of the
// PanickingPicture decoder and nothing else, each logged by decoder at error
// level with a stack that starts in that decoder and names no source file,
// nor any of names.
func RequirePanicLogged(t testing.TB, logged []Panic, decoder string, names ...string) {
	t.Helper()
	if len(logged) == 0 {
		t.Fatalf("no recovered panic was logged, want the %q decoder's", decoder)
	}
	for _, got := range logged {
		if got.Decoder != decoder || got.Level != slog.LevelError || got.Value != PanicMessage {
			t.Fatalf("logged %+v, want the %q decoder's panic %q at error level", got, decoder, PanicMessage)
		}
		if len(got.Stack) == 0 || !strings.HasPrefix(got.Stack[0], "github.com/zhongwater123/A-NAS/internal/imagetest.") {
			t.Fatalf("stack = %q, want it to start in the panicking decoder", got.Stack)
		}
		for _, field := range got.Fields {
			if strings.Contains(field, ".go:") || slices.ContainsFunc(names, func(name string) bool { return strings.Contains(field, name) }) {
				t.Fatalf("logged field %q names a file", field)
			}
		}
	}
}

type recorder struct {
	mu     sync.Mutex
	panics []Panic
}

func (r *recorder) list() []Panic {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.panics)
}

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, record slog.Record) error {
	if record.Message != decodepanic.Message {
		return nil
	}
	logged := Panic{Level: record.Level}
	record.Attrs(func(attr slog.Attr) bool {
		logged.Fields = append(logged.Fields, attr.String())
		switch attr.Key {
		case "decoder":
			logged.Decoder = attr.Value.String()
		case "panic":
			logged.Value = attr.Value.String()
		case "stack":
			logged.Stack, _ = attr.Value.Any().([]string)
		}
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.panics = append(r.panics, logged)
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *recorder) WithGroup(string) slog.Handler { return r }
