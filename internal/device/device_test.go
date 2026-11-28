package device

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	day := time.Date(2026, 11, 29, 10, 0, 0, 0, loc)
	night := time.Date(2026, 11, 29, 23, 0, 0, 0, loc)
	hash, other := "frame-a", "frame-b"

	tests := []struct {
		name   string
		in     Input
		state  *State
		want   Action
		reason string
	}{
		{
			name:   "power on refreshes fully",
			in:     Input{TokenID: "t", Shown: false, Hash: hash, Now: day, Location: loc},
			state:  &State{TokenID: "t"},
			want:   ActionFull,
			reason: ReasonPowerOn,
		},
		{
			name:   "first contact refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Hash: hash, Now: day, Location: loc},
			state:  nil,
			want:   ActionFull,
			reason: ReasonFirstContact,
		},
		{
			name:   "dirty refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Dirty: true, Hash: hash, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash},
			want:   ActionFull,
			reason: ReasonDirtyCatchUp,
		},
		{
			name:   "forced refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Force: true, Hash: hash, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash},
			want:   ActionFull,
			reason: ReasonForced,
		},
		{
			name:   "night does not refresh and holds a change",
			in:     Input{TokenID: "t", Shown: true, Hash: other, Now: night, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash},
			want:   ActionNone,
			reason: ReasonNight,
		},
		{
			name:   "unchanged content does not refresh",
			in:     Input{TokenID: "t", Shown: true, Hash: hash, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash, FullDay: "2026-11-29"},
			want:   ActionNone,
			reason: ReasonUnchanged,
		},
		{
			name:   "first change of the day refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Hash: other, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash, FullDay: "2026-11-28"},
			want:   ActionFull,
			reason: ReasonFirstOfDay,
		},
		{
			name:   "partials exhausted refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Hash: other, Partial: true, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash, FullDay: "2026-11-29", Partials: MaxPartials},
			want:   ActionFull,
			reason: ReasonPartialsExhausted,
		},
		{
			name:   "unsupported partial refreshes fully",
			in:     Input{TokenID: "t", Shown: true, Hash: other, Partial: false, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash, FullDay: "2026-11-29", Partials: 1},
			want:   ActionFull,
			reason: ReasonPartialUnsupported,
		},
		{
			name:   "changed content refreshes partially",
			in:     Input{TokenID: "t", Shown: true, Hash: other, Partial: true, Now: day, Location: loc},
			state:  &State{TokenID: "t", ContentHash: hash, FullDay: "2026-11-29", Partials: 1},
			want:   ActionPartial,
			reason: ReasonChanged,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Decide(test.in, test.state)
			if result.Action != test.want {
				t.Fatalf("Decide action = %q, want %q", result.Action, test.want)
			}
			if result.Reason != test.reason {
				t.Fatalf("Decide reason = %q, want %q", result.Reason, test.reason)
			}
		})
	}
}

func TestDecideNightHoldsChange(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	night := time.Date(2026, 11, 29, 23, 0, 0, 0, loc)
	result := Decide(Input{TokenID: "t", Shown: true, Hash: "new", Now: night, Location: loc},
		&State{TokenID: "t", ContentHash: "old"})
	if !result.State.NightHeld {
		t.Fatalf("Decide should hold the night change, got NightHeld = false")
	}
}

func TestDecidePartialIncrements(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	day := time.Date(2026, 11, 29, 10, 0, 0, 0, loc)
	result := Decide(Input{TokenID: "t", Shown: true, Hash: "new", Partial: true, Now: day, Location: loc},
		&State{TokenID: "t", ContentHash: "old", FullDay: "2026-11-29", Partials: 1})
	if result.State.Partials != 2 {
		t.Fatalf("Decide partials = %d, want 2", result.State.Partials)
	}
	if result.State.ContentHash != "new" {
		t.Fatalf("Decide content hash = %q, want %q", result.State.ContentHash, "new")
	}
}
