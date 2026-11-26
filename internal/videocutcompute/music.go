//go:build linux

package videocutcompute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
)

// unknownOperation is how a Worker refuses an operation it does not have
// (ai/anas_montage/worker.py, answer).
const unknownOperation = "unknown operation"

// Music asks for a song's excerpt and slots (ai/anas_montage/music_ops.py,
// op_music): the eighth version's from allin1's structure and the song's
// loudness, or from the start the member dragged it to.
func (c Client) Music(ctx context.Context, request app.MusicRequest) (app.Selection, error) {
	if len(request.Loudness) == 0 {
		return app.Selection{}, fmt.Errorf("%w: no loudness", app.ErrWorkerRefused)
	}
	fields := map[string]any{"structure": request.Structure, "target": request.Target, "start": request.Start, "song": request.Song}
	var answer map[string]json.RawMessage
	err := c.Call(ctx, "music", fields, []Array{{Name: "loudness", Shape: []int{len(request.Loudness)}, Float32: request.Loudness}}, &answer)
	var selection app.Selection
	if err == nil {
		err = decodeSelection(answer, &selection)
	}
	if err := waitedError(err); err != nil {
		return app.Selection{}, err
	}
	if selection.Unusable == "" && (len(selection.Parts) == 0 || selection.Seconds <= 0) {
		return app.Selection{}, fmt.Errorf("%w: an empty excerpt", app.ErrWorkerRefused)
	}
	return selection, nil
}

// waitedError turns a call's error into the montage center's, for a call a
// member waits on (the song's excerpt, a step of an edit) rather than a job:
// the request's handler tells them apart, where a job's runner retries.
func waitedError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrRefused) && strings.Contains(err.Error(), unknownOperation):
		// A Worker from before the operation existed, still running while
		// the device is updated: one to wait for. It says nothing of the
		// input.
		return fmt.Errorf("%w: %v", app.ErrWorkerUnavailable, err)
	case errors.Is(err, ErrRefused):
		return fmt.Errorf("%w: %v", app.ErrWorkerRefused, err)
	case errors.Is(err, ErrFailed):
		// The Worker took the request and broke on it, or stopped: for a
		// song the montage center counts these, and gives it up after a few.
		return fmt.Errorf("%w: %v", app.ErrWorkerFailed, err)
	}
	return fmt.Errorf("%w: %v", app.ErrWorkerUnavailable, err)
}

// decodeSelection reads the answer, and keeps the excerpt as the domain
// reads it (from_structure's dict) for the jobs that cut to it.
func decodeSelection(answer map[string]json.RawMessage, selection *app.Selection) error {
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

var _ app.MusicComputer = Client{}
