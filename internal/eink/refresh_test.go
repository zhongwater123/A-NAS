package eink

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, time.Local) }
	const shown, other = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	for _, test := range []struct {
		name   string
		ask    Ask
		action string
		reason string
		next   time.Time
	}{
		{"power-on during the day", Ask{Now: at(27, 10, 0), Known: true, Current: shown},
			ActionFull, ReasonBoot, at(27, 10, 15)},
		{"a screen the NAS has never seen", Ask{Now: at(27, 10, 0), Shown: shown, Current: shown},
			ActionFull, ReasonBoot, at(27, 10, 15)},
		{"power-on at night still draws", Ask{Now: at(27, 23, 0), Known: true, Current: shown},
			ActionFull, ReasonBoot, at(28, 7, 0)},
		{"a pasted bar is wiped", Ask{Now: at(27, 10, 0), Known: true, Shown: shown, Current: shown, Dirty: true},
			ActionFull, ReasonDirty, at(27, 10, 15)},
		{"a press redraws at once", Ask{Now: at(27, 10, 6), Known: true, Shown: shown, Current: shown, Pressed: true},
			ActionFull, ReasonPressed, at(27, 10, 21)},
		{"a press redraws at night too", Ask{Now: at(27, 23, 30), Known: true, Shown: shown, Current: other, Pressed: true},
			ActionFull, ReasonPressed, at(28, 7, 0)},
		{"unchanged", Ask{Now: at(27, 10, 0), Known: true, Shown: shown, Current: shown},
			ActionNone, ReasonSame, at(27, 10, 15)},
		{"changed", Ask{Now: at(27, 10, 0), Known: true, Shown: shown, Current: other},
			ActionFull, ReasonChanged, at(27, 10, 15)},
		{"changes wait for the morning", Ask{Now: at(27, 23, 0), Known: true, Shown: shown, Current: other},
			ActionNone, ReasonNight, at(28, 7, 0)},
		{"before seven is still night", Ask{Now: at(28, 6, 59), Known: true, Shown: shown, Current: other},
			ActionNone, ReasonNight, at(28, 7, 0)},
		{"seven sharp redraws what waited", Ask{Now: at(28, 7, 0), Known: true, Shown: shown, Current: other},
			ActionFull, ReasonChanged, at(28, 7, 15)},
		{"the last ask of the day lands on the morning", Ask{Now: at(27, 21, 50), Known: true, Shown: shown, Current: shown},
			ActionNone, ReasonSame, at(28, 7, 0)},
		{"a screen's own poll", Ask{Now: at(27, 10, 0), Known: true, Shown: shown, Current: shown, Poll: time.Minute},
			ActionNone, ReasonSame, at(27, 10, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Decide(test.ask)
			if got.Action != test.action || got.Reason != test.reason || !got.NextAt.Equal(test.next) {
				t.Errorf("Decide = %s %s %s; want %s %s %s", got.Action, got.Reason, got.NextAt.Format(time.DateTime),
					test.action, test.reason, test.next.Format(time.DateTime))
			}
		})
	}
}

func TestDecideNextAtIsWholeSeconds(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 123456789, time.Local)
	if next := Decide(Ask{Now: now, Known: true, Shown: "a", Current: "a"}).NextAt; next.Nanosecond() != 0 {
		t.Errorf("next_at %v carries a fraction of a second; screens read it to the second", next)
	}
}
