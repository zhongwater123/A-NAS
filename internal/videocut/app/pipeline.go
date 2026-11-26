package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// What a job's steps work with besides the photo library's grants
// (docs/architecture/montage-center.md, 整体结构): the compute Worker, the
// FFmpeg Worker, the planner and the songs. Each is an interface, so that
// the steps are tested without either Worker.

// Compute is what the steps ask of the compute Worker
// (ai/anas_montage/worker.py); videocutcompute.Client provides it.
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
	// member's VNAS (ai/anas_montage/brief.py).
	PlanBrief(ctx context.Context, request videocutcompute.PlanBriefRequest) (videocutcompute.PlanBrief, error)
	// ShotsAnswer turns the second step's answer back into the outline's
	// pictures, as the fill reads them.
	ShotsAnswer(ctx context.Context, answer, picked json.RawMessage, pictures []int) (json.RawMessage, error)
	// Likeness is how alike two beats look: the cosine of the means of the
	// window vectors each covers, a and b of dimensions values a window.
	Likeness(ctx context.Context, a, b []float32, dimensions int) (float64, error)
}

// Clip is one shot of the film: Start to End seconds of Video, shown for
// Share seconds (longer than the source when Slow stretches it) in Slot.
type Clip struct {
	Card string `json:"card"`
	// Beat is the beat of the card the shot was cut around, "card/n".
	Beat  string     `json:"beat,omitempty"`
	Video string     `json:"video"`
	Start float64    `json:"start"`
	End   float64    `json:"end"`
	Slot  [2]float64 `json:"slot"`
	Share float64    `json:"share"`
	Slow  float64    `json:"slow,omitempty"`
}

// Editing is what rendering asks of the FFmpeg Worker; ffmpegworker.Client
// provides it.
type Editing interface {
	ffmpegworker.Editing
	Probe(ctx context.Context, file *os.File) (ffmpegworker.Probe, error)
}

// PlanInput is what planning reads: the catalog's cards, the excerpt and
// the member's settings.
type PlanInput struct {
	JobID string `json:"jobId"`
	// Plan is which of the job's 方案 is planned, from 1.
	Plan     int                           `json:"plan"`
	Cards    []videocutcompute.CatalogCard `json:"cards"`
	Music    json.RawMessage               `json:"music"`
	Settings JobSettings                   `json:"settings"`
	// Picked is the first step's answer, for the second step.
	Picked json.RawMessage `json:"picked,omitempty"`
	// StepKey is the input key of the step being planned and PickKey the
	// first step's: a planner that answers later, as the member's VNAS does,
	// keeps its answers by them, so that one for other inputs is never used.
	StepKey string `json:"-"`
	PickKey string `json:"-"`
}

// Planner makes the two steps of the plan: which scenes go into each part
// of the music (Pick), and which beats fill each picture with the slots
// they take (Shots, answering {answer, slots}). The real planner is the AI
// assistant's (M9); until then a stand-in answers.
type Planner interface {
	Pick(ctx context.Context, input PlanInput) (json.RawMessage, error)
	Shots(ctx context.Context, input PlanInput) (json.RawMessage, error)
}

// Planning errors.
var (
	// ErrPlanPending: the plan is not back yet; the job waits for it.
	ErrPlanPending = errors.New("the plan is not back yet")
	// ErrPlanUnknown: the planning ended without a plan, and only the
	// member may plan again. The member's VNAS plans again on its own
	// (ErrPlanFailed); a planner that cannot, as FixedPlanner, says this.
	ErrPlanUnknown = errors.New("the planning round ended without a plan")
	// ErrPlanInvalid: the plan is not one the job can take, and planning
	// again is the member's call (FixedPlanner).
	ErrPlanInvalid = errors.New("the plan failed its checks")
	// ErrPlanFailed: the planning was tried as many rounds as a job's is
	// and none finished it; the job fails (docs/architecture/montage-planning-session.md, 重试).
	ErrPlanFailed = errors.New("the planning failed")
)

// Music is what jobs ask of the project's song (M8b).
type Music interface {
	// Chosen is the project's song and the excerpt's start; no song is "".
	Chosen(ctx context.Context, owner, projectID string) (string, float64, error)
	// ExcerptKey reads the identity without running the compute Worker.
	ExcerptKey(ctx context.Context, owner, songID string, start float64) (string, error)
	// Excerpt is the song's excerpt from start.
	Excerpt(ctx context.Context, owner, songID string, start float64) (Excerpt, error)
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
