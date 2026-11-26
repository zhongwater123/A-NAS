package app

import (
	"context"
	"encoding/json"
	"os"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// What a job's steps work with besides the photo library's grants
// (docs/architecture/montage-center.md, 整体结构): the compute Worker, the
// FFmpeg Worker, the planner and the songs. Each is an interface, so that
// the steps are tested without either Worker.

// Compute is what the steps ask of the compute Worker
// (ai/anas_videocut/worker.py); videocutcompute.Client provides it.
type Compute interface {
	// Catalog lists the cards that may go into the film, in the order
	// given.
	Catalog(ctx context.Context, request videocutcompute.CatalogRequest) ([]string, error)
	// Fill fills the music's slots from the planned shots, or names the
	// clear span of a keyframe it needs first (FillAnswer.Need).
	Fill(ctx context.Context, request videocutcompute.FillRequest, vectors []float32, dimensions int) (videocutcompute.FillAnswer, error)
	// Clear finds how far around a keyframe nothing covers the picture,
	// from grey frames FrameWidth × FrameHeight: the keyframe, then the
	// frames every half second after it up to hi, and from lo up to it.
	Clear(ctx context.Context, request videocutcompute.ClearRequest, key, after, before []byte, afterCount, beforeCount int) ([2]float64, error)
	// PlanBrief is a planning step's data and exact schema for the
	// member's VNAS (ai/anas_videocut/ops/brief.py).
	PlanBrief(ctx context.Context, request videocutcompute.PlanBriefRequest) (videocutcompute.PlanBrief, error)
	// ShotsAnswer turns the second step's answer back into the outline's
	// pictures, as the fill reads them.
	ShotsAnswer(ctx context.Context, answer, picked json.RawMessage, pictures []int) (json.RawMessage, error)
	// Likeness is how alike two beats look: the cosine of the means of the
	// window vectors each covers, a and b of dimensions values a window.
	Likeness(ctx context.Context, a, b []float32, dimensions int) (float64, error)
}

// Editing is what rendering asks of the FFmpeg Worker; ffmpegworker.Client
// provides it.
type Editing interface {
	ffmpegworker.Editing
	Probe(ctx context.Context, file *os.File) (ffmpegworker.Probe, error)
}

// Planner makes the two steps of the plan: which scenes go into each part
// of the music (Pick), and which beats fill each picture with the slots
// they take (Shots, answering {answer, slots}). The real planner is the AI
// assistant's (M9); until then a stand-in answers.
type Planner interface {
	Pick(ctx context.Context, input domain.PlanInput) (json.RawMessage, error)
	Shots(ctx context.Context, input domain.PlanInput) (json.RawMessage, error)
}

// Music is what jobs ask of the project's song (M8b).
type Music interface {
	// Chosen is the project's song and the excerpt's start; no song is "".
	Chosen(ctx context.Context, owner, projectID string) (string, float64, error)
	// ExcerptKey reads the identity without running the compute Worker.
	ExcerptKey(ctx context.Context, owner, songID string, start float64) (string, error)
	// Excerpt is the song's excerpt from start.
	Excerpt(ctx context.Context, owner, songID string, start float64) (domain.Excerpt, error)
	// Audio opens the song's copy.
	Audio(ctx context.Context, owner, songID string) (*os.File, error)
}

// The film: 1920 × 1080 at 30 frames a second, with the song.
const (
	FilmRate   = 30
	FilmWidth  = 1920
	FilmHeight = 1080
	// endingFade fades the last shot to black, in frames (one second).
	endingFade = FilmRate
)
