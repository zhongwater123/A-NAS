package domain

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// The film: 1920 × 1080 at 30 frames a second, with the song.
const (
	FilmRate   = 30
	FilmWidth  = 1920
	FilmHeight = 1080
)

const (
	// narrowBy: a video narrower than 16:9 by more than this is shown on a
	// blurred background (app.renderClip), and counts as 竖.
	narrowBy = 0.01
)

// Narrow reports whether a video is narrower than the film.
func Narrow(width, height int) bool {
	return width > 0 && height > 0 && float64(width)/float64(height) < 16.0/9.0-narrowBy
}

// ClipFrames is how many frames of the film a shot takes.
func ClipFrames(clip Clip) int { return int(math.Round(clip.Share * FilmRate)) }

// The Python fill rounds beat positions to the nearest even frame.
func FilmFrame(seconds float64) int { return int(math.RoundToEven(seconds * FilmRate)) }

// Within a picture the Worker may reorder whole slots to follow shooting
// order. Each picture must still use exactly its planned slot lengths.
func CheckPlannedSlots(plan json.RawMessage, filled [][2]int) error {
	var layout struct{ Slots [][][][2]float64 }
	if err := json.Unmarshal(plan, &layout); err != nil {
		return Failed("invalid planned slots: %v", err)
	}
	at := 0
	for _, part := range layout.Slots {
		for _, picture := range part {
			if len(picture) == 0 || len(picture) > len(filled)-at {
				return Failed("the picture has unfilled slots")
			}
			lengths := map[int]int{}
			for _, slot := range picture {
				lengths[FilmFrame(slot[1])-FilmFrame(slot[0])]++
			}
			for _, slot := range filled[at : at+len(picture)] {
				length := slot[1] - slot[0]
				if lengths[length] == 0 {
					return Failed("unexpected %d-frame slot in picture", length)
				}
				lengths[length]--
			}
			at += len(picture)
		}
	}
	if at != len(filled) {
		return Failed("the timeline has %d unplanned slots", len(filled)-at)
	}
	return nil
}

// CheckPinnedSlots checks an edit's timeline fills its pins' slots, each
// in its place, and that each is cut with the beat the member chose for it:
// beats are the beats of each slot's shots. The fill passes over a pin whose
// card has no footage left around its keyframe, as when the shots beside it
// took it; a film that silently shows another beat there is not the draft,
// so the member is told which shots to change.
func CheckPinnedSlots(pins []videocutcompute.FillPin, filled [][2]int, beats [][]string) error {
	if len(pins) != len(filled) {
		return Failed("the timeline has %d slots, the draft %d", len(filled), len(pins))
	}
	var passed []string
	for i, pin := range pins {
		if want := [2]int{FilmFrame(pin.Slot[0]), FilmFrame(pin.Slot[1])}; filled[i] != want {
			return Failed("slot %d is %v, the draft's %v", i+1, filled[i], want)
		}
		if !slices.Contains(beats[i], pin.Beat) {
			passed = append(passed, strconv.Itoa(i+1))
		}
	}
	if len(passed) > 0 {
		return Failed("第 %s 镜选的节拍放不进它的位置：同一段录像里相邻的镜头已经用到了那段画面。换一个节拍后再存", strings.Join(passed, "、"))
	}
	return nil
}

// ShortOfPins says which of an edit's shots the fill could not fill: short
// are the fill's unfilled slots, each [part, picture, start, end, frames].
func ShortOfPins(pins []videocutcompute.FillPin, short []json.RawMessage) error {
	var shots []string
	for _, raw := range short {
		var slot []float64
		if err := json.Unmarshal(raw, &slot); err != nil || len(slot) < 4 {
			continue
		}
		for i, pin := range pins {
			if FilmFrame(pin.Slot[0]) == FilmFrame(slot[2]) {
				shots = append(shots, strconv.Itoa(i+1))
			}
		}
	}
	if len(shots) == 0 {
		return Failed("%d slots were left empty", len(short))
	}
	return Failed("第 %s 镜选的节拍不够长，也没有别的画面能补上。换一个节拍后再存", strings.Join(shots, "、"))
}
