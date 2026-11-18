package montage

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
)

// What a job's steps work with besides the photo library's grants
// (docs/architecture/montage-center.md, 整体结构): the compute Worker, the
// FFmpeg Worker, the planner and the songs. Each is an interface, so that
// the steps are tested without either Worker.

// Compute is what the steps ask of the compute Worker
// (ai/anas_montage/worker.py); montagecompute.Client provides it.
type Compute interface {
	// Catalog lists the cards that may go into the film, in the order
	// given.
	Catalog(ctx context.Context, request CatalogRequest) ([]string, error)
	// Fill fills the music's slots from the planned shots, or names the
	// clear span of a keyframe it needs first (FillAnswer.Need).
	Fill(ctx context.Context, request FillRequest, vectors []float32, dimensions int) (FillAnswer, error)
	// Clear finds how far around a keyframe nothing covers the picture,
	// from grey frames FrameWidth × FrameHeight: the keyframe, then the
	// frames every half second after it up to hi, and from lo up to it.
	Clear(ctx context.Context, request ClearRequest, key, after, before []byte, afterCount, beforeCount int) ([2]float64, error)
}

// Grey frames for Clear: 256 × 144, two a second (picture.STEP).
const (
	FrameWidth  = 256
	FrameHeight = 144
	FrameRate   = 2
)

// Card is a card as the compute Worker's domain reads it: its video, its
// bounds, its keyframes and the reader's description (answer).
type Card struct {
	ID     string          `json:"id"`
	Video  string          `json:"video"`
	Start  float64         `json:"start"`
	End    float64         `json:"end"`
	Times  []float64       `json:"times"`
	Frames []string        `json:"frames"`
	Answer json.RawMessage `json:"answer"`
}

// CatalogRequest asks which described cards may go into the film; Measures
// holds each card's quality as the photo library measured it.
type CatalogRequest struct {
	Cards    []Card                     `json:"cards"`
	Measures map[string]json.RawMessage `json:"measures"`
}

// FillRow is one window whose vector the fill reads.
type FillRow struct {
	Video string  `json:"video"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// FillRequest is the fill's input: the catalog's cards, the windows beneath
// them, the excerpt, the plans and the clear spans already found.
type FillRequest struct {
	FPS     int                   `json:"fps"`
	Rows    []FillRow             `json:"rows"`
	Cards   map[string]Card       `json:"cards"`
	Catalog []string              `json:"catalog"`
	Music   json.RawMessage       `json:"music"`
	Planned json.RawMessage       `json:"planned"`
	Picked  json.RawMessage       `json:"picked"`
	Known   map[string][2]float64 `json:"known"`
}

// FillNeed is the clear span the fill needs next.
type FillNeed struct {
	Name   string  `json:"name"`
	Beat   string  `json:"beat"`
	Video  string  `json:"video"`
	Key    float64 `json:"key"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	Turned bool    `json:"turned"`
}

// Clip is one shot of the film: Start to End seconds of Video, shown for
// Share seconds (longer than the source when Slow stretches it) in Slot.
type Clip struct {
	Card  string     `json:"card"`
	Video string     `json:"video"`
	Start float64    `json:"start"`
	End   float64    `json:"end"`
	Slot  [2]float64 `json:"slot"`
	Share float64    `json:"share"`
	Slow  float64    `json:"slow,omitempty"`
}

// FillAnswer is the fill's answer: what it needs, or the clips and the
// slots it could not fill.
type FillAnswer struct {
	Need  *FillNeed         `json:"need,omitempty"`
	Clips []json.RawMessage `json:"clips,omitempty"`
	Short []json.RawMessage `json:"short,omitempty"`
}

// ClearRequest is the keyframe and the span around it to search.
type ClearRequest struct {
	Key float64 `json:"key"`
	Lo  float64 `json:"lo"`
	Hi  float64 `json:"hi"`
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
	JobID    string          `json:"jobId"`
	Cards    []Card          `json:"cards"`
	Music    json.RawMessage `json:"music"`
	Settings JobSettings     `json:"settings"`
	// Picked is the first step's answer, for the second step.
	Picked json.RawMessage `json:"picked,omitempty"`
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
	// ErrPlanUnknown: the planning round ended without a plan.
	ErrPlanUnknown = errors.New("the planning round ended without a plan")
	// ErrPlanInvalid: the plan failed its checks too many times.
	ErrPlanInvalid = errors.New("the plan failed its checks")
)

// Music is what jobs ask of the project's song (M8b).
type Music interface {
	// Chosen is the project's song and the excerpt's start; no song is "".
	Chosen(ctx context.Context, owner, projectID string) (string, float64, error)
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
