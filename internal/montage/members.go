package montage

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// memberLocks holds one lock per member. A member's registrations, the
// deletion of a project and reconciling that member's grants take it, so
// that a repeated request waits for the first and finds its result, and
// reconciling never sees a registration half done. An entry exists only
// while it is held or waited for. It is the per-client admission of
// accounts.signInFailures; singleflight would hand a waiting duplicate the
// first request's answer, which cannot tell 201 from 200.
type memberLocks struct {
	mu   sync.Mutex
	held map[string]*memberLock
}

type memberLock struct {
	gate *semaphore.Weighted
	refs int
}

// lock waits for owner's lock, or for ctx to end, and returns its release.
func (l *memberLocks) lock(ctx context.Context, owner string) (func(), error) {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*memberLock{}
	}
	entry := l.held[owner]
	if entry == nil {
		entry = &memberLock{gate: semaphore.NewWeighted(1)}
		l.held[owner] = entry
	}
	entry.refs++
	l.mu.Unlock()

	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.held, owner)
		}
	}
	if err := entry.gate.Acquire(ctx, 1); err != nil {
		drop()
		return nil, err
	}
	return func() { entry.gate.Release(1); drop() }, nil
}
