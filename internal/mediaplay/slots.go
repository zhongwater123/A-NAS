package mediaplay

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBusy reports that every conversion slot stayed taken while a stream
// waited for one.
var ErrBusy = errors.New("the device is converting as many videos as it can")

const (
	// Conversions is how many videos a service converts for playback at
	// once. Playing an original directly takes no slot.
	Conversions = 2
	// ConversionWait is how long a conversion waits for a slot: a seek
	// replaces the viewer's stream with one starting elsewhere, and its
	// request can arrive before the stream it replaces has let go.
	ConversionWait = 4 * time.Second
)

// Slots bounds the conversions one process runs at once. The FFmpeg Worker
// admits every request of every process to its own slots (ffmpegworker.Slots);
// these leave room there for previews, posters and the other service's
// playback.
type Slots struct {
	slots chan struct{}
	wait  time.Duration
}

// NewSlots makes count slots that a conversion waits for up to wait.
func NewSlots(count int, wait time.Duration) *Slots {
	return &Slots{slots: make(chan struct{}, count), wait: wait}
}

// Take claims a slot, waiting for one while the slots' wait lasts, and
// returns how to give it back. Release may be called more than once.
func (s *Slots) Take(ctx context.Context) (release func(), err error) {
	release = sync.OnceFunc(func() { <-s.slots })
	select {
	case s.slots <- struct{}{}:
		return release, nil
	default:
	}
	timer := time.NewTimer(s.wait)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		return release, nil
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
