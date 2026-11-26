package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// An excerpt's frames are counted as the fill counts its slots' frames
// (Python's round, half to even) and as validateTimeline checks them: one
// count rounding a half frame up made 90.15 s 2705 frames against the
// fill's 2704, and the job failed at its fill. About one excerpt length in
// twenty has a half frame at 30 fps.
func TestAnExcerptsFramesRoundAHalfFrameAsTheFillDoes(t *testing.T) {
	for seconds, want := range map[float64]int{90.15: 2704, 90.05: 2702, 90.25: 2708, 90: 2700} {
		if got := FilmFrame(seconds); got != want {
			t.Errorf("filmFrame(%v) = %d, want %d as Python's round(%v * 30)", seconds, got, want, seconds)
		}
	}
}

// A timeline that does not show a beat where the draft has it is not the
// draft's film: the member is told which shots to change (#609 review).
func TestAnEditsTimelineIsCutWithTheDraftsBeats(t *testing.T) {
	pins := []videocutcompute.FillPin{
		{Part: 1, Picture: 1, Slot: [2]float64{0, 4}, Beat: "c1/1"},
		{Part: 1, Picture: 1, Slot: [2]float64{4, 8}, Beat: "c1/2"},
		{Part: 2, Picture: 1, Slot: [2]float64{8, 14}, Beat: "c2/2"},
	}
	slots := [][2]int{{0, 120}, {120, 240}, {240, 420}}
	if err := CheckPinnedSlots(pins, slots, [][]string{{"c1/1"}, {"c1/2", "c3/1"}, {"c3/2", "c2/2"}}); err != nil {
		t.Fatalf("a timeline with every pin = %v", err)
	}
	err := CheckPinnedSlots(pins, slots, [][]string{{"c1/1"}, {"c1/3"}, {"c3/2"}})
	var stopped *StepFailed
	if !errors.As(err, &stopped) || !strings.Contains(err.Error(), "第 2、3 镜选的节拍放不进它的位置") {
		t.Fatalf("a timeline without two pins = %v", err)
	}
	short := []json.RawMessage{json.RawMessage(`[1, 1, 4.0, 8.0, 10]`)}
	if err := ShortOfPins(pins, short); !errors.As(err, &stopped) || !strings.Contains(err.Error(), "第 2 镜选的节拍不够长") {
		t.Fatalf("a slot left short = %v", err)
	}
}
