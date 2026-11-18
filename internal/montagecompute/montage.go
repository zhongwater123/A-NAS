//go:build linux

package montagecompute

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

// What the montage service asks of the Worker (montage.Compute): the
// catalog, the fill and the clear spans it needs. Their cards, rows and
// plans outgrow a frame for a project of hundreds of videos, so they travel
// as the "body" array.

// withBody sends request's fields as the body array beside arrays.
func (c Client) withBody(ctx context.Context, op string, request any, arrays []Array, answer any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return c.Call(ctx, op, nil, append(arrays, Array{Name: "body", Shape: []int{len(body)}, Uint8: nonNil(body)}), answer)
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Catalog lists the cards that may go into the film.
func (c Client) Catalog(ctx context.Context, request montage.CatalogRequest) ([]string, error) {
	var answer struct {
		Cards []string `json:"cards"`
	}
	err := c.withBody(ctx, "catalog", request, nil, &answer)
	return answer.Cards, err
}

// Fill fills the music's slots, or names the clear span it needs.
func (c Client) Fill(ctx context.Context, request montage.FillRequest, vectors []float32, dimensions int) (montage.FillAnswer, error) {
	if dimensions <= 0 || len(vectors) != len(request.Rows)*dimensions {
		return montage.FillAnswer{}, fmt.Errorf("%w: %d values for %d rows of %d", ErrRefused, len(vectors), len(request.Rows), dimensions)
	}
	var answer montage.FillAnswer
	err := c.withBody(ctx, "fill", request, []Array{{Name: "vectors", Shape: []int{len(request.Rows), dimensions}, Float32: nonNilFloats(vectors)}}, &answer)
	return answer, err
}

func nonNilFloats(v []float32) []float32 {
	if v == nil {
		return []float32{}
	}
	return v
}

// Clear finds the clear span around a keyframe from grey frames.
func (c Client) Clear(ctx context.Context, request montage.ClearRequest, key, after, before []byte, afterCount, beforeCount int) ([2]float64, error) {
	var answer struct {
		Span [2]float64 `json:"span"`
	}
	frame := []int{montage.FrameHeight, montage.FrameWidth}
	err := c.Call(ctx, "clear", request, []Array{
		{Name: "key", Shape: frame, Uint8: nonNil(key)},
		{Name: "after", Shape: []int{afterCount, montage.FrameHeight, montage.FrameWidth}, Uint8: nonNil(after)},
		{Name: "before", Shape: []int{beforeCount, montage.FrameHeight, montage.FrameWidth}, Uint8: nonNil(before)},
	}, &answer)
	return answer.Span, err
}

var _ montage.Compute = Client{}

// PlanBrief is a planning step's data and exact schema.
func (c Client) PlanBrief(ctx context.Context, request montage.PlanBriefRequest) (montage.PlanBrief, error) {
	var answer montage.PlanBrief
	err := c.withBody(ctx, "plan_brief", request, nil, &answer)
	return answer, err
}

// ShotsAnswer turns the second step's answer back into the outline's
// pictures.
func (c Client) ShotsAnswer(ctx context.Context, answer, picked json.RawMessage, pictures []int) (json.RawMessage, error) {
	var turned struct {
		Answer json.RawMessage `json:"answer"`
	}
	err := c.withBody(ctx, "shots_answer", map[string]any{"answer": answer, "picked": picked, "pictures": pictures}, nil, &turned)
	return turned.Answer, err
}
