// Package eink is the e-ink screen outlet: when a screen redraws and when it
// asks again (docs/specs/eink-screens.md, “什么时候刷”). What a screen shows
// comes from the board; this package only compares picture keys and times.
package eink

import "time"

// Actions a screen is told to take.
const (
	ActionNone = "none"
	ActionFull = "full"
)

// Why a screen redraws or not; the screen logs it and settings show it.
const (
	ReasonBoot    = "开机"
	ReasonDirty   = "擦掉底条"
	ReasonPressed = "有人按了电源键"
	ReasonNight   = "夜里不刷"
	ReasonSame    = "内容没变"
	ReasonChanged = "内容变了"
)

const (
	// NightStart is the hour from which screens hold changes until morning.
	NightStart = 22
	// DayPoll is how often a battery screen asks during the day.
	DayPoll = 15 * time.Minute
)

// Morning is when screens start redrawing: 7:00 for the to-do screen.
var Morning = 7 * time.Hour

// Ask is one question from a screen.
type Ask struct {
	// Now is the NAS's local time; screens set their clocks from it.
	Now time.Time
	// Known is false when the NAS has no record of this screen yet.
	Known bool
	// Shown is the key of the picture on the screen; empty after power-on.
	Shown string
	// Current is the key of the picture the board gives now.
	Current string
	// Dirty is set when the screen pasted its own offline bar.
	Dirty bool
	// Pressed is set when someone pressed the power key.
	Pressed bool
	// Poll is how often this screen asks during the day; zero means DayPoll.
	Poll time.Duration
}

// Decision is the answer to an Ask.
type Decision struct {
	Action string
	Reason string
	// NextAt is when the screen asks again, to the second.
	NextAt time.Time
}

// Decide answers a screen, in the order the spec gives: power-on, a pasted
// bar, a pressed key, night, unchanged, changed. Only the first three redraw
// at night; anything else waits for the morning.
func Decide(ask Ask) Decision {
	poll := ask.Poll
	if poll <= 0 {
		poll = DayPoll
	}
	next := ask.Now.Add(poll).Truncate(time.Second)
	if quiet(ask.Now) || quiet(next) {
		next = morningAfter(ask.Now)
	}
	decision := Decision{Action: ActionFull, NextAt: next}
	switch {
	case !ask.Known || ask.Shown == "":
		decision.Reason = ReasonBoot
	case ask.Dirty:
		decision.Reason = ReasonDirty
	case ask.Pressed:
		decision.Reason = ReasonPressed
	case quiet(ask.Now):
		decision.Action, decision.Reason = ActionNone, ReasonNight
	case ask.Shown == ask.Current:
		decision.Action, decision.Reason = ActionNone, ReasonSame
	default:
		decision.Reason = ReasonChanged
	}
	return decision
}

// quiet tells whether t falls in the night, from NightStart until Morning.
func quiet(t time.Time) bool {
	since := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
	return t.Hour() >= NightStart || since < Morning
}

// morningAfter is the next Morning: today's if it is still before it,
// otherwise tomorrow's.
func morningAfter(t time.Time) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	at := day.Add(Morning)
	if !at.After(t) {
		at = day.AddDate(0, 0, 1).Add(Morning)
	}
	return at
}
