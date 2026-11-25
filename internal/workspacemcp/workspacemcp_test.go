package workspacemcp

import (
	"testing"
	"time"
)

// Claude Code cuts a server's instructions at 4,096 characters.
func TestTheInstructionsFit(t *testing.T) {
	if n := len([]rune(instructions)); n > 4096 {
		t.Fatalf("the instructions have %d characters", n)
	}
}

func TestSinceSaysHowStaleSomethingIs(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for at, want := range map[time.Time]string{
		now.Add(-10 * time.Second): "just now",
		now.Add(-7 * time.Minute):  "7 minutes ago",
		now.Add(-5 * time.Hour):    "5 hours ago",
		now.Add(-72 * time.Hour):   "2026-11-22",
	} {
		if got := since(now, at); got != want {
			t.Errorf("since(%v) = %q, want %q", now.Sub(at), got, want)
		}
	}
}
