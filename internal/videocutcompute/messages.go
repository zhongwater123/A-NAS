// The messages between the montage service and the compute Worker
// (ai/anas_videocut/worker.py): the requests and answers of its operations,
// the bounds it holds the input to, and how a call can fail.

package videocutcompute

import (
	"encoding/json"
	"errors"
)

// PlanBriefRequest asks for a planning step's brief: the catalog's cards in
// shooting order, the excerpt, the member's settings and, for the second
// step, the outline taken.
type PlanBriefRequest struct {
	Step          string          `json:"step"`
	Cards         []CatalogCard   `json:"cards"`
	Music         json.RawMessage `json:"music"`
	Ask           string          `json:"ask"`
	Chronological bool            `json:"chronological"`
	Picked        json.RawMessage `json:"picked,omitempty"`
	// Change and Base, for a recut (#596): the member's sentence, and the
	// outline and shots of the version it starts from.
	Change string          `json:"change,omitempty"`
	Base   json.RawMessage `json:"base,omitempty"`
}

// PlanBrief is a step's brief: Data and Schema go to the model; Slots and
// Pictures stay here, for the fill and to turn the answer back.
type PlanBrief struct {
	Data     json.RawMessage `json:"data"`
	Schema   json.RawMessage `json:"schema"`
	Template string          `json:"template"`
	Slots    json.RawMessage `json:"slots,omitempty"`
	Pictures []int           `json:"pictures,omitempty"`
}

// Grey frames for Clear: 256 × 144, two a second (picture.STEP).
const (
	FrameWidth  = 256
	FrameHeight = 144
	FrameRate   = 2
)

// CatalogCard is a described card as the compute Worker's catalog, plan
// and fill read it (app.Card before #670). CutCard is the other kind: a
// card the Worker just cut, before anything describes it. CatalogCard
// carries its video, its bounds, its keyframes and the reader's
// description (answer).
type CatalogCard struct {
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
	Cards    []CatalogCard              `json:"cards"`
	Measures map[string]json.RawMessage `json:"measures"`
}

// FillRow is one window whose vector the fill reads.
type FillRow struct {
	Video string  `json:"video"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// FillRequest is the fill's input: the catalog's cards, the windows beneath
// them, the excerpt, the plans and the clear spans already found. Pins,
// for a film edited after it was made (#596), list its slots in film order
// with the beat that leads each; without them the fill is the eighth
// version's.
type FillRequest struct {
	FPS     int                    `json:"fps"`
	Rows    []FillRow              `json:"rows"`
	Cards   map[string]CatalogCard `json:"cards"`
	Catalog []string               `json:"catalog"`
	Music   json.RawMessage        `json:"music"`
	Planned json.RawMessage        `json:"planned"`
	Picked  json.RawMessage        `json:"picked"`
	Known   map[string][2]float64  `json:"known"`
	Pins    []FillPin              `json:"pins,omitempty"`
}

// FillPin is one slot of an edited film: the plan's part and picture it
// belongs to, its span in the film and the beat ("card/n") that leads it.
type FillPin struct {
	Part    int        `json:"part"`
	Picture int        `json:"picture"`
	Slot    [2]float64 `json:"slot"`
	Beat    string     `json:"beat"`
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

// Section is a part of a song as allin1 labels it.
type Section struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
}

// Structure is allin1's result for a song, as the eighth version keeps it
// (structure-<song>.json), with the model that found it.
type Structure struct {
	Model     string    `json:"model"`
	BPM       float64   `json:"bpm"`
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	Segments  []Section `json:"segments"`
}

// Part is a section of an excerpt: its level in the film, and the slots the
// shots fill, in seconds from the excerpt's start.
type Part struct {
	Start  float64      `json:"start"`
	End    float64      `json:"end"`
	Level  string       `json:"level"`
	Label  string       `json:"label"`
	Allin1 string       `json:"allin1,omitempty"`
	Slots  [][2]float64 `json:"slots"`
}

// Selection is the compute Worker's music operation: the eighth version's
// excerpt and slots (domain.music.from_structure), with whether a chorus was
// found and why the song cannot be used.
type Selection struct {
	Tempo   float64    `json:"tempo"`
	Bar     float64    `json:"bar"`
	Excerpt [2]float64 `json:"excerpt"`
	Seconds float64    `json:"seconds"`
	Parts   []Part     `json:"parts"`
	// Chorus is false when allin1 found no chorus and the loudest section
	// stood in for it: the member is asked to listen.
	Chorus   bool   `json:"chorus"`
	Unusable string `json:"unusable,omitempty"`
	// Music is the excerpt as the compute Worker's domain reads it
	// (domain.music.from_structure's dict), which jobs cut the film to.
	Music json.RawMessage `json:"-"`
}

// MusicRequest asks for the excerpt of a song: from the eighth version's
// rule, or from Start, a downbeat the member dragged it to.
type MusicRequest struct {
	// Song names the song in the plan's music note.
	Song      string
	Structure Structure
	Loudness  []float32
	Target    float64
	Start     *float64
}

// ErrWorkerUnavailable reports that a Worker could not be reached or could
// not serve; the montage center tries again later.
var ErrWorkerUnavailable = errors.New("the worker is not available")

// ErrWorkerFailed reports that a Worker took the request and could not
// finish it: it reported an error of its own, stopped answering or ran out
// of time. The same request may work later, but every attempt costs what the
// work costs, so the montage center counts them.
var ErrWorkerFailed = errors.New("the worker failed")

// ErrWorkerRefused reports that a Worker refused the input; the same input
// would fail again.
var ErrWorkerRefused = errors.New("the worker refused the input")

// MaxComputeArrayBytes is how much the compute Worker takes in input arrays
// in all (worker.MAX_ARRAY_BYTES).
const MaxComputeArrayBytes = 64 << 20
