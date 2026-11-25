// Package keylock serializes concurrent work on the same resource.
package keylock

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// Map holds a cancellable lock per key, only while it is held or awaited.
// Its zero value is ready for use.
type Map[K comparable] struct {
	mu   sync.Mutex
	held map[K]*entry
}

type entry struct {
	gate *semaphore.Weighted
	refs int
}

// Lock waits for the key's lock, or for ctx to end, and returns its release.
func (l *Map[K]) Lock(ctx context.Context, key K) (func(), error) {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[K]*entry{}
	}
	e := l.held[key]
	if e == nil {
		e = &entry{gate: semaphore.NewWeighted(1)}
		l.held[key] = e
	}
	e.refs++
	l.mu.Unlock()

	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		e.refs--
		if e.refs == 0 {
			delete(l.held, key)
		}
	}
	if err := e.gate.Acquire(ctx, 1); err != nil {
		drop()
		return nil, err
	}
	return func() { e.gate.Release(1); drop() }, nil
}
