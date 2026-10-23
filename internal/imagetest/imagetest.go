// Package imagetest gives tests of in-process image decoding a picture whose
// decoder panics, as a decoder with a bug may on a malformed picture, a
// picture whose decoder waits for the test, and a record of the panics decode
// guards log.
package imagetest

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// The pictures do not start with a NUL byte: ID3 readers take one after a
// picture's description for part of the description.
var (
	panicking = []byte("\x01anas-panicking-picture")
	held      = []byte("\x01anas-held-picture")
)

// PanicMessage is the panic of the PanickingPicture decoder. It stands for
// input a decoder quotes, so guards must not log it.
const PanicMessage = "imagetest: decoder bug"

var (
	heldDecoding atomic.Int32
	heldRelease  = make(chan struct{})
)

var register = sync.OnceFunc(func() {
	// The formats call themselves webp, which every caller accepts, and
	// their headers are pictures, so callers get past their checks to Decode.
	image.RegisterFormat("webp", string(panicking[:8]),
		func(io.Reader) (image.Image, error) { panic(PanicMessage) },
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 64, Height: 48}, nil
		})
	image.RegisterFormat("webp", string(held[:8]),
		func(io.Reader) (image.Image, error) {
			heldDecoding.Add(1)
			defer heldDecoding.Add(-1)
			<-heldRelease
			return image.NewGray(image.Rect(0, 0, 1, 1)), nil
		},
		func(r io.Reader) (image.Config, error) {
			config := image.Config{ColorModel: color.GrayModel}
			data, err := io.ReadAll(r)
			if err != nil {
				return config, err
			}
			_, err = fmt.Sscanf(strings.TrimPrefix(string(data), string(held)), ":%dx%d", &config.Width, &config.Height)
			return config, err
		})
})

// PanickingPicture returns a picture whose header reads as a 64x48 WebP and
// whose decoder panics.
func PanickingPicture() []byte {
	register()
	return slices.Clone(panicking)
}

// HeldPicture returns a picture whose header reads as a width x height WebP
// and whose decoder waits for ReleaseHeld, then returns a 1x1 picture.
func HeldPicture(width, height int) []byte {
	register()
	return fmt.Appendf(slices.Clone(held), ":%dx%d", width, height)
}

// HeldDecoding is the number of HeldPicture decodes in progress.
func HeldDecoding() int { return int(heldDecoding.Load()) }

// ReleaseHeld lets one HeldPicture decode in progress finish, waiting for one
// to start if none has.
func ReleaseHeld() { heldRelease <- struct{}{} }

// Panic is a recovered panic that a decode guard logged.
type Panic struct {
	Decoder string
	// Type is the panic value's type; Value is the message of a runtime
	// error, the only value logged.
	Type    string
	Value   string
	Stack   []string
	Repeats int64
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
// level with a stack that starts in that decoder. No field may carry the
// panic's message, name a source file, or contain any of names.
func RequirePanicLogged(t testing.TB, logged []Panic, decoder string, names ...string) {
	t.Helper()
	if len(logged) == 0 {
		t.Fatalf("no recovered panic was logged, want the %q decoder's", decoder)
	}
	for _, got := range logged {
		if got.Decoder != decoder || got.Level != slog.LevelError || got.Type != "string" || got.Value != "" {
			t.Fatalf("logged %+v, want the %q decoder's string panic at error level", got, decoder)
		}
		if len(got.Stack) == 0 || !strings.HasPrefix(got.Stack[0], "github.com/zhongwater123/A-NAS/internal/imagetest.") {
			t.Fatalf("stack = %q, want it to start in the panicking decoder", got.Stack)
		}
		for _, field := range got.Fields {
			if strings.Contains(field, PanicMessage) || strings.Contains(field, ".go:") ||
				slices.ContainsFunc(names, func(name string) bool { return strings.Contains(field, name) }) {
				t.Fatalf("logged field %q carries the input or names a file", field)
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
		case "panic_type":
			logged.Type = attr.Value.String()
		case "panic":
			logged.Value = attr.Value.String()
		case "stack":
			logged.Stack, _ = attr.Value.Any().([]string)
		case "repeats":
			logged.Repeats = attr.Value.Int64()
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
