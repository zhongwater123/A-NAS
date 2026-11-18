//go:build linux

package photoservice

import (
	"context"
	"errors"

	"github.com/zhongwater123/A-NAS/internal/montagecompute"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// MontageCompute is the montage compute Worker as the photo library uses it
// (photos.Options.Compute).
func MontageCompute(client montagecompute.Client) photos.MontageCompute { return computeWorker{client} }

type computeWorker struct{ client montagecompute.Client }

// mapped turns the Worker's unavailability into the library's, so that the
// work waits for it rather than failing.
func mapped(err error) error {
	if errors.Is(err, montagecompute.ErrUnavailable) {
		return errors.Join(photos.ErrComputeUnavailable, err)
	}
	return err
}

func (w computeWorker) Cards(ctx context.Context, windows []photos.CardWindow, vectors []float32, dimensions int, flat []bool) ([]photos.ComputedCard, error) {
	list := make([]montagecompute.Window, len(windows))
	for i, window := range windows {
		list[i] = montagecompute.Window{Start: window.Start, End: window.End}
	}
	cards, err := w.client.Cards(ctx, "card", list, vectors, dimensions, flat)
	if err != nil {
		return nil, mapped(err)
	}
	out := make([]photos.ComputedCard, len(cards))
	for i, card := range cards {
		out[i] = photos.ComputedCard{Start: card.Start, End: card.End, Times: card.Times}
	}
	return out, nil
}

func (w computeWorker) ShakeMoves(ctx context.Context, frames []byte, count int) ([][2]float64, error) {
	moves, err := w.client.ShakeMoves(ctx, frames, count)
	return moves, mapped(err)
}

func (w computeWorker) Keyframe(ctx context.Context, jpeg []byte) (float64, float64, error) {
	measure, err := w.client.Keyframe(ctx, jpeg)
	return measure.Blocked, measure.Light, mapped(err)
}

func (w computeWorker) SharpPoints(ctx context.Context, frame []byte, width, height int) (float64, error) {
	points, err := w.client.SharpPoints(ctx, frame, width, height)
	return points, mapped(err)
}

func (w computeWorker) Quality(ctx context.Context, measures photos.CardMeasures) (photos.CardQuality, error) {
	quality, err := w.client.Quality(ctx, montagecompute.QualityInput{Moves: measures.Moves, Blurs: measures.Blurs,
		Blocked: measures.Blocked, Points: measures.Points})
	if err != nil {
		return photos.CardQuality{}, mapped(err)
	}
	return photos.CardQuality{Blur: quality.Blur, Shake: quality.Shake, Blocked: quality.Blocked, Points: quality.Points, Reasons: quality.Reasons}, nil
}
