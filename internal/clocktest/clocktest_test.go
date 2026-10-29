package clocktest_test

import (
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
)

var start = time.Date(2026, time.October, 23, 9, 0, 0, 0, time.UTC)

func TestAClockStandsUntilTheTestMovesIt(t *testing.T) {
	clock := clocktest.New(start)
	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}
	clock.Advance(time.Minute)
	if got, want := clock.Now(), start.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("Now() after Advance = %v, want %v", got, want)
	}
}

func TestATickingClockMovesOnEveryReadUntilSet(t *testing.T) {
	clock := clocktest.Ticking(start, time.Second)
	if first, second := clock.Now(), clock.Now(); !first.Equal(start.Add(time.Second)) || !second.Equal(start.Add(2*time.Second)) {
		t.Fatalf("two reads = %v, %v; want one and two seconds after the start", first, second)
	}
	later := start.Add(time.Hour)
	clock.Set(later)
	if first, second := clock.Now(), clock.Now(); !first.Equal(later) || !second.Equal(later) {
		t.Fatalf("reads after Set = %v, %v; want %v both times", first, second, later)
	}
}

// The race detector fails this test if the clock is not safe for
// concurrent use.
func TestAClockIsReadWhileTheTestMovesIt(t *testing.T) {
	clock := clocktest.Ticking(start, time.Second)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 100 {
				clock.Now()
			}
		})
	}
	for range 100 {
		clock.Advance(time.Minute)
	}
	readers.Wait()
	if got, want := clock.Now(), start.Add(100*time.Minute+401*time.Second); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}
