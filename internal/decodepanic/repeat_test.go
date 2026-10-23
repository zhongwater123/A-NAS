package decodepanic

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// repeatsHandler keeps the repeats attribute of each record.
type repeatsHandler struct {
	mu      sync.Mutex
	repeats []int64
}

func (*repeatsHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *repeatsHandler) Handle(_ context.Context, record slog.Record) error {
	repeats := int64(0)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "repeats" {
			repeats = attr.Value.Int64()
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repeats = append(h.repeats, repeats)
	return nil
}

func (h *repeatsHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *repeatsHandler) WithGroup(string) slog.Handler { return h }

//go:noinline
func element(data []byte, at int) byte { return data[at] }

func TestARepeatedPanicIsLoggedAgainAfterTheQuietPeriodWithItsCount(t *testing.T) {
	handler := &repeatsHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	clock := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	t.Cleanup(func() {
		slog.SetDefault(previous)
		now = time.Now
	})
	broken := func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				Log("test decoder", recovered)
			}
		}()
		element(nil, 1)
	}

	// One call site, as a request repeated takes the same path.
	for _, step := range []time.Duration{0, quiet - time.Second, 0, time.Second} {
		clock = clock.Add(step)
		broken()
	}
	if want := []int64{0, 2}; len(handler.repeats) != 2 || handler.repeats[0] != want[0] || handler.repeats[1] != want[1] {
		t.Fatalf("records carry repeats %v, want %v", handler.repeats, want)
	}
}
