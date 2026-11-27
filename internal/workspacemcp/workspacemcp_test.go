package workspacemcp

import (
	"strings"
	"testing"
	"time"
)

// A member who picked an element says "移除这个" to their agent, often after
// talking about something else: the instructions send the agent to their
// pick first, in the words members use, and to ask when there is none
// (issue #664).
func TestTheInstructionsSendTheAgentToThePick(t *testing.T) {
	for _, words := range []string{"移除这个", "这个", "this", "even in the middle of a conversation", "never guess it from earlier messages"} {
		if !strings.Contains(instructions, words) {
			t.Errorf("the instructions do not say %q", words)
		}
	}
	if !strings.Contains(noPick, "ask which element") {
		t.Errorf("without a pick the agent is not told to ask: %q", noPick)
	}
}

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
