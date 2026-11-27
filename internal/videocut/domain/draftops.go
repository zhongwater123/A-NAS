package domain

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// BeatID names beat n, from 1, of a card: "card/n", as the fill and the
// plan write a beat (CONTEXT.md, 节拍).
func BeatID(card string, n int) string { return card + "/" + strconv.Itoa(n) }

// SplitBeat is the card and the number from 1 that BeatID wrote as beat;
// ok is false for a text it did not write.
func SplitBeat(beat string) (card string, n int, ok bool) {
	card, number, found := strings.CutLast(beat, "/")
	n, err := strconv.Atoi(number)
	if !found || err != nil || n < 1 || strconv.Itoa(n) != number {
		return "", 0, false
	}
	return card, n, true
}

// CardOf is the card of a beat.
func CardOf(beat string) string {
	if card, _, ok := SplitBeat(beat); ok {
		return card
	}
	return beat
}

// DraftSlot is a slot of the version: where it is in the film, the plan's
// part and picture it belongs to, and the beat that led it.
type DraftSlot struct {
	Part    int     `json:"part"`
	Picture int     `json:"picture"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Beat    string  `json:"beat"`
}

// DraftStateShot is a shot of the draft: a beat over slots First to Last. A
// shot over more than one slot took a deleted shot's slots; Home is the
// slot that was its own, whose beat the version had there.
type DraftStateShot struct {
	Beat   string `json:"beat"`
	First  int    `json:"first"`
	Last   int    `json:"last"`
	Home   int    `json:"home"`
	Locked bool   `json:"locked,omitempty"`
}

// DraftState is a revision of a draft: the version's slots, and the shots
// that cover them in film order. Only the shots are kept (Encode): the
// slots are the version's, read from its timeline (slots).
type DraftState struct {
	Slots []DraftSlot
	Shots []DraftStateShot
}

// KeptShots is a revision as it is kept.
type KeptShots struct {
	Shots []DraftStateShot `json:"shots"`
}

// Encode is the state as a revision keeps it.
func (state DraftState) Encode() (string, error) {
	encoded, err := json.Marshal(KeptShots{Shots: state.Shots})
	return string(encoded), err
}

// Valid checks a stored state still fits its version.
func (state DraftState) Valid() bool {
	next := 0
	for _, shot := range state.Shots {
		if shot.First != next || shot.Last < shot.First || shot.Last >= len(state.Slots) {
			return false
		}
		next = shot.Last + 1
	}
	return next == len(state.Slots) && next > 0
}

func (state DraftState) Clone() DraftState {
	return DraftState{Slots: state.Slots, Shots: slices.Clone(state.Shots)}
}

func (state DraftState) Length(shot DraftStateShot) float64 {
	return state.Slots[shot.Last].End - state.Slots[shot.First].Start
}

// UsedBy is the number, from 1, of the shot whose beat is beat; 0 none.
func (state DraftState) UsedBy(beat string) int {
	for i, shot := range state.Shots {
		if shot.Beat == beat {
			return i + 1
		}
	}
	return 0
}

// Original is the beat the version had where the shot is: its own slot's,
// not that of a deleted shot whose slots it took.
func (state DraftState) Original(shot DraftStateShot) string {
	home := shot.First
	if shot.Home > shot.First && shot.Home <= shot.Last {
		home = shot.Home
	}
	return state.Slots[home].Beat
}

// Changed reports whether the shot's beat is not the one the version had
// there.
func (state DraftState) Changed(shot DraftStateShot) bool {
	return state.Original(shot) != shot.Beat
}

// ShotAt is shot number n of state, from 1.
func ShotAt(state DraftState, n int) (int, error) {
	if n < 1 || n > len(state.Shots) {
		return 0, ErrInvalid
	}
	return n - 1, nil
}

// Taken reports whether beat leads a shot of a shuffle other than the i-th:
// one before it as shuffled, or one after it as it is, which may stay so
// (locked, with nothing to search for, or finding nothing to pick).
func Taken(next DraftState, i int, beat string) bool {
	leads := func(shot DraftStateShot) bool { return shot.Beat == beat }
	return slices.ContainsFunc(next.Shots[:i], leads) || slices.ContainsFunc(next.Shots[i+1:], leads)
}
