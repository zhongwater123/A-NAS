package files

import (
	"context"
	"sync"
)

// spaceOrder orders the writers of one space's catalog rows (#164). Changes
// made through A-NAS run side by side: each moves files and then commits the
// rows that say so. A pass that writes what a scan saw (reconciliation,
// Samba recycle import) runs alone, and only when no change began or ended
// since its scan started. Otherwise the scan predates rows that are already
// current, and writing it would undo them: put a deleted folder's recycle-bin
// rows back at their old paths, drop new entries as missing, move moved ones
// back.
type spaceOrder struct {
	mu sync.Mutex
	// generation counts changes begun and ended and scans written.
	generation uint64
	changes    int
	writing    bool
	// idle is closed and replaced whenever changes reaches 0 or a write ends.
	idle chan struct{}
}

func (s *Service) order(spaceID string) *spaceOrder {
	s.ordersMu.Lock()
	defer s.ordersMu.Unlock()
	order, ok := s.orders[spaceID]
	if !ok {
		order = &spaceOrder{idle: make(chan struct{})}
		s.orders[spaceID] = order
	}
	return order
}

// change marks a change to a space's files as in flight, from before its
// first file operation until its rows are committed; call done then, or when
// it gives up. Changes may nest: a write cannot start while one is in flight,
// so an inner change never waits.
func (s *Service) change(ctx context.Context, spaceID string) (done func(), err error) {
	order := s.order(spaceID)
	for {
		order.mu.Lock()
		if !order.writing {
			order.changes++
			order.generation++
			order.mu.Unlock()
			return order.endChange, nil
		}
		idle := order.idle
		order.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (o *spaceOrder) endChange() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.changes--
	o.generation++
	if o.changes == 0 {
		o.wake()
	}
}

// observe returns the generation a scan starts at, or false while a change
// or another write is in flight and the scan would be discarded anyway.
func (o *spaceOrder) observe() (uint64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.generation, o.changes == 0 && !o.writing
}

// beginWrite starts writing a scan begun at generation. It reports false,
// and the scan is to be discarded, when anything changed the rows since.
func (o *spaceOrder) beginWrite(generation uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.changes > 0 || o.writing || o.generation != generation {
		return false
	}
	o.writing = true
	o.generation++
	return true
}

// lock waits until no change is in flight and starts a write, for callers
// that must scan and write even while the space is busy.
func (o *spaceOrder) lock(ctx context.Context) error {
	for {
		o.mu.Lock()
		if o.changes == 0 && !o.writing {
			o.writing = true
			o.generation++
			o.mu.Unlock()
			return nil
		}
		idle := o.idle
		o.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// scanAlone runs a scan and the writes from it with no change to the space
// in flight.
func (s *Service) scanAlone(ctx context.Context, spaceID string, scan func() error) error {
	order := s.order(spaceID)
	if err := order.lock(ctx); err != nil {
		return err
	}
	defer order.endWrite()
	return scan()
}

func (o *spaceOrder) endWrite() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writing = false
	o.wake()
}

func (o *spaceOrder) wake() {
	close(o.idle)
	o.idle = make(chan struct{})
}
