package ffmpegworker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Every process that uses the worker (the Product, Photo and Share Services)
// draws on one budget of Slots requests at a time. A request takes a slot
// before it connects and gives it back when its answer or stream is closed;
// a request nobody waits for waits for a slot instead of failing
// (docs/architecture/media-center.md#准入与失败).

// Priority says who waits for whom when every slot is taken.
type Priority int

const (
	// Interactive is someone waiting for a short answer: a preview, a poster,
	// an import's probe. It is the default for every request but a stream.
	Interactive Priority = iota
	// Streaming is playback, which keeps its worker while it plays. It is the
	// default for stream and audio requests.
	Streaming
	// Background is work nobody watches: thumbnails, library scans and the
	// inputs of local AI. It waits for a slot for as long as its context
	// lives, so a busy worker never fails it.
	Background

	priorities = Background + 1
)

// Slots is how many requests use the worker at once across every process
// sharing a slot directory. The socket's MaxConnections leaves room above it
// for workers still exiting after their callers let go.
const Slots = 8

// reach is how many slots each priority may hold. Streams leave two slots to
// someone waiting for a preview, and background work takes at most two, so a
// person never queues behind a thumbnail. Each takes the highest free slot
// within its reach, leaving the lower ones to the others.
var reach = [priorities]int{Interactive: Slots, Streaming: Slots - 2, Background: 2}

// patience is how long a request someone waits on queues for a slot before
// the worker counts as busy; background work waits as long as its context.
var patience = [priorities]time.Duration{Interactive: 20 * time.Second, Streaming: 5 * time.Second}

// poll is how often a waiting request looks for a slot another process freed;
// a slot freed in this process wakes its waiters at once.
var poll = [priorities]time.Duration{Interactive: 50 * time.Millisecond, Streaming: 100 * time.Millisecond, Background: time.Second}

type priorityKey struct{}

// WithPriority marks the requests made with ctx.
func WithPriority(ctx context.Context, priority Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, priority)
}

type timeoutKey struct{}

// WithTimeout bounds each request made with ctx from the moment it has its
// slot: the wait for a slot does not count, so a request queued behind
// others is not cut short for it. A stream's bound lasts until it is closed.
func WithTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, timeoutKey{}, timeout)
}

// working is the context of a request that has its slot.
func working(ctx context.Context) (context.Context, context.CancelFunc) {
	if timeout, ok := ctx.Value(timeoutKey{}).(time.Duration); ok && timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

func priorityOf(ctx context.Context, op string) Priority {
	if priority, ok := ctx.Value(priorityKey{}).(Priority); ok && priority >= Interactive && priority <= Background {
		return priority
	}
	if op == OpStream || op == OpAudio {
		return Streaming
	}
	return Interactive
}

// admission hands out one slot directory's slots to this process. flock(2) on
// the slot files keeps other processes off a slot this one holds and lets go
// of it however the holder exits. Without the directory (development, tests)
// the slots are this process's alone.
type admission struct {
	mu      sync.Mutex
	dir     string
	checked bool
	held    [Slots]bool
	files   [Slots]*os.File
	// freed is closed, and replaced, whenever this process frees a slot.
	freed chan struct{}
}

var admissions sync.Map // slot directory → *admission

// admit takes a slot from dir for a request of priority, waiting while none
// is free, and returns how to give it back.
func admit(ctx context.Context, dir string, priority Priority) (func(), error) {
	value, ok := admissions.Load(dir)
	if !ok {
		value, _ = admissions.LoadOrStore(dir, &admission{dir: dir, freed: make(chan struct{})})
	}
	return value.(*admission).acquire(ctx, priority)
}

func (a *admission) acquire(ctx context.Context, priority Priority) (func(), error) {
	var expired <-chan time.Time
	if wait := patience[priority]; wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		expired = timer.C
	}
	ticker := time.NewTicker(poll[priority])
	defer ticker.Stop()
	for {
		release, freed, err := a.take(reach[priority])
		if release != nil || err != nil {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-expired:
			return nil, ErrBusy
		case <-freed:
		case <-ticker.C:
		}
	}
}

// take holds the highest free slot below reach, or returns a channel closed
// once this process frees one.
func (a *admission) take(reach int) (func(), <-chan struct{}, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.checked && a.dir != "" {
		if _, err := os.Stat(a.dir); errors.Is(err, fs.ErrNotExist) {
			a.dir = ""
		}
	}
	a.checked = true
	for slot := reach - 1; slot >= 0; slot-- {
		if a.held[slot] {
			continue
		}
		if a.dir != "" {
			locked, err := a.lock(slot)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: slot %d: %v", ErrUnavailable, slot, err)
			}
			if !locked {
				continue
			}
		}
		a.held[slot] = true
		var once sync.Once
		return func() { once.Do(func() { a.release(slot) }) }, nil, nil
	}
	return nil, a.freed, nil
}

// lock takes slot's file lock unless another process holds it. A slot file
// opens read-only: flock needs no write access, so the services' read-only
// view of /run is enough.
func (a *admission) lock(slot int) (bool, error) {
	if a.files[slot] == nil {
		file, err := os.Open(filepath.Join(a.dir, strconv.Itoa(slot)))
		if err != nil {
			return false, err
		}
		a.files[slot] = file
	}
	err := unix.Flock(int(a.files[slot].Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func (a *admission) release(slot int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dir != "" {
		_ = unix.Flock(int(a.files[slot].Fd()), unix.LOCK_UN)
	}
	a.held[slot] = false
	close(a.freed)
	a.freed = make(chan struct{})
}
