//go:build linux

package montagecompute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

// unknownOperation is how a Worker refuses an operation it does not have
// (ai/anas_montage/worker.py, answer).
const unknownOperation = "unknown operation"

// Music asks for a song's excerpt and slots (ai/anas_montage/music_ops.py,
// op_music): the eighth version's from allin1's structure and the song's
// loudness, or from the start the member dragged it to.
func (c Client) Music(ctx context.Context, request montage.MusicRequest) (montage.Selection, error) {
	if len(request.Loudness) == 0 {
		return montage.Selection{}, fmt.Errorf("%w: no loudness", montage.ErrWorkerRefused)
	}
	fields := map[string]any{"structure": request.Structure, "target": request.Target, "start": request.Start, "song": request.Song}
	var answer map[string]json.RawMessage
	err := c.Call(ctx, "music", fields, []Array{{Name: "loudness", Shape: []int{len(request.Loudness)}, Float32: request.Loudness}}, &answer)
	var selection montage.Selection
	if err == nil {
		err = decodeSelection(answer, &selection)
	}
	switch {
	case errors.Is(err, ErrRefused) && strings.Contains(err.Error(), unknownOperation):
		// A Worker from before the operation existed, still running while
		// the device is updated: one to wait for. It says nothing of the
		// song.
		return montage.Selection{}, fmt.Errorf("%w: %v", montage.ErrWorkerUnavailable, err)
	case errors.Is(err, ErrRefused):
		return montage.Selection{}, fmt.Errorf("%w: %v", montage.ErrWorkerRefused, err)
	case errors.Is(err, ErrFailed):
		// The Worker took the song and broke on it, or stopped: the montage
		// center counts these, and gives the song up after a few.
		return montage.Selection{}, fmt.Errorf("%w: %v", montage.ErrWorkerFailed, err)
	case err != nil:
		return montage.Selection{}, fmt.Errorf("%w: %v", montage.ErrWorkerUnavailable, err)
	case selection.Unusable == "" && (len(selection.Parts) == 0 || selection.Seconds <= 0):
		return montage.Selection{}, fmt.Errorf("%w: an empty excerpt", montage.ErrWorkerRefused)
	}
	return selection, nil
}

// decodeSelection reads the answer, and keeps the excerpt as the domain
// reads it (from_structure's dict) for the jobs that cut to it.
func decodeSelection(answer map[string]json.RawMessage, selection *montage.Selection) error {
	encoded, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, selection); err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, err)
	}
	for _, key := range []string{"ok", "chorus", "unusable"} {
		delete(answer, key)
	}
	selection.Music, err = json.Marshal(answer)
	return err
}

var _ montage.MusicComputer = Client{}
