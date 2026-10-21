package files

import (
	"context"
	"sync"
	"time"
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
	// generation counts changes begun and ended and scans that changed
	// rows when written. A scan that found the rows as they were leaves it,
	// so listing folders does not stop a longer scan from being written.
	generation uint64
	changes    int
	writing    bool
	// idle is closed and replaced whenever changes reaches 0 or a write ends.
	idle chan struct{}
	// wholeSlot lets one pass over the whole space run at a time (see
	// reconcileWhole). wholeStarted numbers those passes, wholeWritten is
	// the last one that wrote its scan and wholeAt when that one began.
	wholeSlot    chan struct{}
	wholeStarted uint64
	wholeWritten uint64
	wholeAt      time.Time
}

func (s *Service) order(spaceID string) *spaceOrder {
	s.ordersMu.Lock()
	defer s.ordersMu.Unlock()
	order, ok := s.orders[spaceID]
	if !ok {
		order = &spaceOrder{idle: make(chan struct{}), wholeSlot: make(chan struct{}, 1)}
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
// endWrite ends the write.
func (o *spaceOrder) beginWrite(generation uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.changes > 0 || o.writing || o.generation != generation {
		return false
	}
	o.writing = true
	return true
}

// lock waits until no change is in flight and starts a write, for callers
// that must scan and write even while the space is busy.
func (o *spaceOrder) lock(ctx context.Context) error {
	for {
		o.mu.Lock()
		if o.changes == 0 && !o.writing {
			o.writing = true
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
	defer order.endWrite(true)
	return scan()
}

// endWrite ends a write, which changed rows or not.
func (o *spaceOrder) endWrite(changed bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writing = false
	if changed {
		o.generation++
	}
	o.wake()
}

func (o *spaceOrder) wake() {
	close(o.idle)
	o.idle = make(chan struct{})
}
