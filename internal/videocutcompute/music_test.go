//go:build linux

package videocutcompute_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// song is a downbeat every two seconds for 200 seconds, the choruses louder.
func song() (videocutcompute.Structure, []float32) {
	structure := videocutcompute.Structure{BPM: 120}
	for k := 0; k <= 100; k++ {
		structure.Downbeats = append(structure.Downbeats, float64(2*k))
		structure.Beats = append(structure.Beats, float64(2*k))
	}
	for _, s := range [][3]any{{"intro", 0, 16}, {"verse", 16, 48}, {"chorus", 48, 80}, {"verse", 80, 112}, {"chorus", 112, 144}, {"outro", 144, 200}} {
		structure.Segments = append(structure.Segments, videocutcompute.Section{Label: s[0].(string), Start: float64(s[1].(int)), End: float64(s[2].(int))})
	}
	loudness := make([]float32, 200*22050/512)
	for i := range loudness {
		at := float64(i) * 512 / 22050
		loudness[i] = 0.1
		if at >= 48 && at < 80 || at >= 112 && at < 144 {
			loudness[i] = 0.2
		}
	}
	return structure, loudness
}

// The music operation through the real Worker (ai/anas_videocut/ops/music.py),
// as worker.py's OPS names it.
func TestTheMusicOperationAnswersFromPython(t *testing.T) {
	client := startWorker(t, "")
	structure, loudness := song()
	ctx := context.Background()
	selection, err := client.Music(ctx, videocutcompute.MusicRequest{Structure: structure, Loudness: loudness, Target: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !selection.Chorus || selection.Unusable != "" || selection.Seconds < 30 || selection.Seconds > 150 || len(selection.Parts) == 0 {
		t.Fatalf("selection = %+v", selection)
	}
	start := 21.0 // Equidistant downbeats snap to the earlier one, as in montage.nearest.
	dragged, err := client.Music(ctx, videocutcompute.MusicRequest{Structure: structure, Loudness: loudness, Target: 90, Start: &start})
	if err != nil || dragged.Excerpt != [2]float64{20, 110} || dragged.Seconds != 90 {
		t.Fatalf("dragged = %+v, %v", dragged, err)
	}
	broken := structure
	broken.Downbeats = []float64{3, 1}
	if _, err := client.Music(ctx, videocutcompute.MusicRequest{Structure: broken, Loudness: loudness, Target: 90}); !errors.Is(err, videocutcompute.ErrWorkerRefused) {
		t.Fatalf("a broken structure = %v; want refused", err)
	}
}

// A Worker that takes the request and breaks on it has failed on the song,
// which the montage center counts; it is not a Worker to wait for without
// end.
func TestAWorkerThatBreaksOnTheMusicOperationHasFailed(t *testing.T) {
	client := startWorker(t, "def broken(request, arrays):\n    raise RuntimeError('broken')\nworker.OPS['music'] = broken")
	structure, loudness := song()
	_, err := client.Music(context.Background(), videocutcompute.MusicRequest{Structure: structure, Loudness: loudness, Target: 90})
	if !errors.Is(err, videocutcompute.ErrWorkerFailed) {
		t.Fatalf("Music() from a Worker that breaks = %v; want failed", err)
	}
}

// While the device is updated, a compute Worker from before the operation
// existed may still be running. It is one to wait for: its refusal says
// nothing of the song.
func TestAWorkerWithoutTheMusicOperationIsWaitedFor(t *testing.T) {
	client := startWorker(t, "del worker.OPS['music']")
	structure, loudness := song()
	_, err := client.Music(context.Background(), videocutcompute.MusicRequest{Structure: structure, Loudness: loudness, Target: 90})
	if !errors.Is(err, videocutcompute.ErrWorkerUnavailable) || errors.Is(err, videocutcompute.ErrWorkerRefused) {
		t.Fatalf("Music() without the operation = %v; want unavailable", err)
	}
}
