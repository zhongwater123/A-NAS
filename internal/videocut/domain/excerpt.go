package domain

import (
	"encoding/json"
	"slices"

	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Named is the excerpt music as the domain reads it, with title for its
// song. An analysis is shared by every member who chose the same content, so
// it names no song; the plan's music note shows each member their own.
func Named(music json.RawMessage, title string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(music, &fields); err != nil {
		return nil, err
	}
	song, err := json.Marshal(title)
	if err != nil {
		return nil, err
	}
	fields["song"] = song
	return json.Marshal(fields)
}

// ExcerptSeconds is the target length of the film in whole seconds, from
// the song step's stored excerpt.
func ExcerptSeconds(stored string) int {
	var excerpt Excerpt
	if stored == "" || json.Unmarshal([]byte(stored), &excerpt) != nil || excerpt.Frames <= 0 {
		return 0
	}
	return (excerpt.Frames + FilmRate/2) / FilmRate
}

// DefaultDownbeat is the downbeat that stands for the default excerpt's
// start. That start is a section's bound kept to two decimals, and need not
// be a downbeat itself; a start the member drags always is one.
func DefaultDownbeat(st *videocutcompute.Structure, selection *videocutcompute.Selection) float64 {
	return Nearest(st.Downbeats, selection.Excerpt[0])
}

// Nearest is the time in times closest to at; times is sorted.
func Nearest(times []float64, at float64) float64 {
	i, _ := slices.BinarySearch(times, at)
	switch {
	case i == 0:
		return times[0]
	case i == len(times):
		return times[len(times)-1]
	case at-times[i-1] <= times[i]-at:
		return times[i-1]
	}
	return times[i]
}
