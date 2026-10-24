// Package clocktest gives tests a clock they can move. The code under test
// may read it from background goroutines while the test moves it, so like
// time.Now it is safe for concurrent use: a test that kept the time in a
// captured variable instead made the race-detector job fail now and then.
package clocktest

import (
	"sync"
	"time"
)

// Clock is a fake clock. Pass its Now method where the code takes a clock.
type Clock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

// New returns a clock that stands at start until the test moves it.
func New(start time.Time) *Clock { return &Clock{now: start} }

// Ticking returns a clock that moves by step each time it is read, so that
// changes made one after another have different times.
func Ticking(start time.Time, step time.Duration) *Clock {
	return &Clock{now: start, step: step}
}

// Now returns the time, after moving a ticking clock by its step.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

// Set moves the clock to now and stops it ticking.
func (c *Clock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now, c.step = now, 0
}

// Advance moves the clock by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
