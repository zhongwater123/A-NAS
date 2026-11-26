// What app asks of VideoCut's outside: the photo library, the
// FFmpeg Worker, the compute and music Workers and the planner. The
// interfaces are defined here, where they are used; the package
// internal/videocut/external implements them, and tests stand in for them.

package app

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Markers are the planning markers: one per member whose job wants a round
// of planning, which the planner's waker reads
// (docs/architecture/videocut-planning-session.md).
type Markers interface {
	// Leave leaves username's marker.
	Leave(username string) error
	// Has reports whether username has a marker.
	Has(username string) bool
	// Keep leaves the markers of the members in wanted and takes away the
	// others.
	Keep(wanted map[string]bool) error
}

// WindowSearch finds, among a grant's videos, the windows most like each of
// texts: the photo library's searchWindows (#599), which its grants answer
// (photosapi.Grants).
type WindowSearch interface {
	SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error)
}

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

// Grants is what VideoCut asks of the photo library's grants;
// photosapi.Grants provides it.
type Grants interface {
	Register(ctx context.Context, session, projectID string, assetIDs []string) (photos.Grant, error)
	Lookup(ctx context.Context, grantID string, assetIDs []string) ([]photos.GrantedAsset, error)
	Remove(ctx context.Context, grantID string, assetIDs []string) error
	List(ctx context.Context) ([]photos.Grant, error)
	Revoke(ctx context.Context, grantID string) error
	// What jobs ask: a video's original, its analysis, and saving a film.
	Open(ctx context.Context, grantID, assetID string) (*os.File, string, error)
	Analyze(ctx context.Context, grantID string, assetIDs []string, retry bool) error
	Analysis(ctx context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error)
	SaveFilm(ctx context.Context, grantID, versionKey, name string, film *os.File) (string, error)
}

// Media is what VideoCut asks of the FFmpeg worker for its songs;
// ffmpegworker.Client provides it.
type Media interface {
	Probe(ctx context.Context, file *os.File) (ffmpegworker.Probe, error)
	Audio(ctx context.Context, file *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error)
}

// MusicAnalyzer finds a song's structure in its samples: stereo, 32-bit
// float at 44.1 kHz for allin1, and mono at 22050 Hz for the loudness. Both
// arrive as streams; the analyzer reads them in that order while they are
// written. whole yields once, when the writing ended: nil when both streams
// carry the whole song. The analyzer analyses nothing before that, and
// returns any other error whole yields as it is.
type MusicAnalyzer interface {
	Analyze(ctx context.Context, stereo, mono *os.File, whole <-chan error) (domain.Analysis, error)
}

// MusicComputer is the compute Worker's music operation.
type MusicComputer interface {
	Music(ctx context.Context, request videocutcompute.MusicRequest) (videocutcompute.Selection, error)
}
