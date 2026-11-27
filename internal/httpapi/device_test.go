package httpapi

import (
	"testing"
	"time"
)

func TestDeviceLimiter(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	limiter := &deviceLimiter{now: func() time.Time { return now }}
	for range deviceFailures - 1 {
		limiter.failed("lan 172.18.45.60")
	}
	if wait := limiter.held("lan 172.18.45.60"); wait != 0 {
		t.Fatalf("held after %d failures for %v", deviceFailures-1, wait)
	}
	limiter.failed("lan 172.18.45.60")
	if wait := limiter.held("lan 172.18.45.60"); wait != deviceWindow {
		t.Fatalf("held for %v, want %v", wait, deviceWindow)
	}
	if wait := limiter.held("lan 172.18.45.61"); wait != 0 {
		t.Fatalf("another client is held for %v", wait)
	}
	now = now.Add(deviceWindow - time.Second)
	if wait := limiter.held("lan 172.18.45.60"); wait != time.Second {
		t.Fatalf("held for %v near the window's end", wait)
	}
	now = now.Add(time.Second)
	if wait := limiter.held("lan 172.18.45.60"); wait != 0 {
		t.Fatalf("still held for %v once the window ended", wait)
	}
	// A token that works clears the count.
	for range deviceFailures - 1 {
		limiter.failed("lan 172.18.45.60")
	}
	limiter.passed("lan 172.18.45.60")
	limiter.failed("lan 172.18.45.60")
	if wait := limiter.held("lan 172.18.45.60"); wait != 0 {
		t.Fatalf("held for %v after a token worked", wait)
	}
}
