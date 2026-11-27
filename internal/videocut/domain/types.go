package domain

import (
	"encoding/json"
	"io/fs"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// ToolAnswer is what a submission, or a brief that cannot be given,
// answers the model. Message is for the model, in the template's words.
type ToolAnswer struct {
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

// EditRefusal is a step the rules do not allow; Reason says why, for the
// member.
type EditRefusal struct{ Reason string }

func (r *EditRefusal) Error() string { return "the edit is refused: " + r.Reason }

// Draft is a member's edit of a version, as it is answered.
type Draft struct {
	ID            string `json:"id"`
	VersionID     string `json:"versionId"`
	ProjectID     string `json:"projectId"`
	VersionNumber int    `json:"versionNumber"`
	// Revision is the draft's revision, which a step is made on; CanUndo
	// whether the step before it can be gone back to.
	Revision int  `json:"revision"`
	CanUndo  bool `json:"canUndo"`
	// SongStart is where the version's excerpt starts in its song, and
	// Seconds how long the film is. SongTranscode: the song is decoded on
	// the way and cannot seek, so the rough preview asks for it from
	// where it plays.
	SongStart     float64     `json:"songStart"`
	SongTranscode bool        `json:"songTranscode"`
	Seconds       float64     `json:"seconds"`
	Plan          DraftPlan   `json:"plan"`
	Parts         []DraftPart `json:"parts"`
	Shots         []DraftShot `json:"shots"`
	Message       string      `json:"message,omitempty"`
	Counts        DraftCounts `json:"counts"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// DraftPlan is the plan the version was cut by: the first step's design,
// opening and ending.
type DraftPlan struct {
	Design  string `json:"design"`
	Opening string `json:"opening"`
	Ending  string `json:"ending"`
}

// DraftPart is a part of the music and of the plan.
type DraftPart struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Level   string `json:"level"`
	// Label is the music's section (allin1's: intro, verse, chorus, build…);
	// a build leads into a chorus (铺垫).
	Label string  `json:"label"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// DraftCounts are the draft's shots, the videos they come from, and how
// many of those are 竖.
type DraftCounts struct {
	Shots    int `json:"shots"`
	Videos   int `json:"videos"`
	Portrait int `json:"portrait"`
}

// DraftShot is a shot of the draft. Start and End are its place in the
// film; From and To the part of its video a rough preview plays.
type DraftShot struct {
	Number   int     `json:"number"`
	Beat     string  `json:"beat"`
	AssetID  string  `json:"assetId"`
	Part     int     `json:"part"`
	Picture  int     `json:"picture"`
	Slots    []int   `json:"slots"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Query    string  `json:"query"`
	ShotSize string  `json:"shotSize"`
	What     string  `json:"what"`
	Locked   bool    `json:"locked"`
	Changed  bool    `json:"changed"`
	Extended bool    `json:"extended"`
	Portrait bool    `json:"portrait"`
}

// VersionOutline is a version as its film is: the plan it was cut by and
// its shots, read without making a draft.
type VersionOutline struct {
	VersionID     string      `json:"versionId"`
	ProjectID     string      `json:"projectId"`
	VersionNumber int         `json:"versionNumber"`
	Seconds       float64     `json:"seconds"`
	Plan          DraftPlan   `json:"plan"`
	Parts         []DraftPart `json:"parts"`
	Shots         []DraftShot `json:"shots"`
	Counts        DraftCounts `json:"counts"`
	// Unmet is what the plan found the videos do not have.
	Unmet []string `json:"unmet"`
}

// DraftOp is one step of an edit. Shot and To are shot numbers from 1;
// Seed, for a shuffle, makes it again as it was.
type DraftOp struct {
	Type string `json:"type"`
	Shot int    `json:"shot,omitempty"`
	To   int    `json:"to,omitempty"`
	Beat string `json:"beat,omitempty"`
	Seed *int64 `json:"seed,omitempty"`
}

// CandidateQuery asks for the beats that could lead shot Shot: Q searches
// all the videos, and Size, Scene and Landscape filter what is searched or
// listed.
type CandidateQuery struct {
	Shot      int
	Q         string
	Size      string
	Scene     string
	Landscape bool
}

// Candidate is a beat that could lead the shot. From and To are the part
// of its video a try plays; Fits whether it is long enough; UsedBy the
// shot that has it, 0 none.
type Candidate struct {
	Beat     string     `json:"beat"`
	AssetID  string     `json:"assetId"`
	From     float64    `json:"from"`
	To       float64    `json:"to"`
	ShotSize string     `json:"shotSize"`
	What     string     `json:"what"`
	Scene    string     `json:"scene"`
	Portrait bool       `json:"portrait"`
	Fits     bool       `json:"fits"`
	UsedBy   int        `json:"usedBy,omitempty"`
	Score    *float64   `json:"score,omitempty"`
	TakenAt  *time.Time `json:"takenAt,omitempty"`
}

// SceneCount is a scene the cards were described in, and how many.
type SceneCount struct {
	Scene string `json:"scene"`
	Cards int    `json:"cards"`
}

// Candidates are the beats for a shot: the rest of its card, the plan's
// spare beats for its picture, and the search's results, or every beat
// in shooting order when nothing was searched for.
type Candidates struct {
	Shot     int          `json:"shot"`
	Query    string       `json:"query"`
	SameCard []Candidate  `json:"sameCard"`
	Planned  []Candidate  `json:"planned"`
	Results  []Candidate  `json:"results"`
	Scenes   []SceneCount `json:"scenes"`
}

// HeroFile is a hero file opened: its content type and what serving it needs.
type HeroFile struct {
	File        *os.File
	Info        fs.FileInfo
	ContentType string
}

// Excerpt is the part of the song the film is cut to.
type Excerpt struct {
	// Key names the song's copy, its analysis and the excerpt's start.
	Key string `json:"key"`
	// Music is the excerpt as the compute Worker's domain reads it.
	Music json.RawMessage `json:"music"`
	// Start is where the excerpt starts in the song, in seconds; Frames
	// is how long the film is, at FilmRate.
	Start  float64 `json:"start"`
	Frames int     `json:"frames"`
}

// JobSettings is what a job started with.
type JobSettings struct {
	SongID        string  `json:"songId,omitempty"`
	SongStart     float64 `json:"songStart,omitempty"`
	Ask           string  `json:"ask"`
	Chronological bool    `json:"chronological"`
	ProjectName   string  `json:"projectName"`
	// Edit is what an edit job renders (#596): the job and 方案 the
	// version was planned in, and the draft's slots with their beats.
	Edit *EditSettings `json:"edit,omitempty"`
	// Recut is what a recut job plans from (#596): the version's plan and
	// the member's sentence.
	Recut *RecutSettings `json:"recut,omitempty"`
}

// RecutSettings is a recut's sentence and the plan it starts from: the
// outline and shots the version was cut by, without the shots changed by
// hand.
type RecutSettings struct {
	Change string          `json:"change"`
	Base   json.RawMessage `json:"base"`
	// Cards are where the cards Base names were in their videos when the
	// version was cut, by the numbers Base names them by: the brief names
	// them by their numbers now (alignBase).
	Cards map[string]CardPlace `json:"cards,omitempty"`
}

// EditSettings is the draft an edit job renders, as it stood when saved.
type EditSettings struct {
	Job  string                    `json:"job"`
	Plan int                       `json:"plan"`
	Pins []videocutcompute.FillPin `json:"pins"`
}

// Step is where one step of a job stands.
type Step struct {
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Started  *time.Time `json:"startedAt,omitempty"`
	Finished *time.Time `json:"finishedAt,omitempty"`
}

// Job is a job as the member sees it.
type Job struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"projectId"`
	Kind       string   `json:"kind"`
	State      string   `json:"state"`
	Step       string   `json:"step,omitempty"`
	WaitReason string   `json:"waitReason,omitempty"`
	Error      string   `json:"error,omitempty"`
	Steps      []Step   `json:"steps"`
	Missing    []string `json:"missingAssetIds,omitempty"`
	// Version is the first 方案's film; Versions are every 方案's, in order.
	Version    *Version   `json:"version,omitempty"`
	Versions   []Version  `json:"versions,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Version is a film a job saved.
type Version struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	// JobID and Plan: the job that made it, and which of its 方案 it is.
	JobID       string    `json:"jobId"`
	Plan        int       `json:"plan"`
	FilmAssetID string    `json:"filmAssetId"`
	CreatedAt   time.Time `json:"createdAt"`
	// Gone: the member deleted the film from the photo library.
	Gone bool `json:"gone,omitempty"`
	// Editable: its timeline is known, so that it can be edited (#596). A
	// version saved before timelines were kept has its job's fill, unless
	// that is gone too; then it can only be played. It is a look at the
	// version's row alone: whether the plan it was cut by can still be read
	// is found when its draft or outline is opened (ErrNotEditable).
	Editable bool `json:"editable"`
	// Kind is plan, edit or recut; an edit or recut names the version it
	// came from (改自第 N 版), and an edit the draft's revision.
	Kind            string `json:"kind"`
	SourceVersionID string `json:"sourceVersionId,omitempty"`
	SourceNumber    int    `json:"sourceNumber,omitempty"`
	DraftRevision   *int   `json:"draftRevision,omitempty"`
	// Change is a recut's sentence: 按“<这句话>”重剪自第 N 版.
	Change string `json:"change,omitempty"`
}

// Notice is what the desktop tells a member about their jobs.
type Notice struct {
	Kind      string `json:"kind"`
	JobID     string `json:"jobId"`
	ProjectID string `json:"projectId"`
	Project   string `json:"project"`
	Reason    string `json:"reason,omitempty"`
	// Detail says why a job still waits for VNAS, in the project page's
	// words (DetailVNASOff and the others), or what made one fail.
	Detail string    `json:"detail,omitempty"`
	FilmID string    `json:"filmAssetId,omitempty"`
	At     time.Time `json:"at"`
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

// Claim is what a claimed round tells the planner: what it fills the
// template with, and the project's name, which titles the conversation in
// the member's VNAS history.
type Claim struct {
	RoundID       string `json:"roundId"`
	JobID         string `json:"jobId"`
	AttemptToken  string `json:"attemptToken"`
	Template      string `json:"template"`
	TargetSeconds int    `json:"targetSeconds"`
	Project       string `json:"project"`
}

// AssetStatus is where a project's video stands in the photo library now.
type AssetStatus string

// Project is a member's project in a list.
type Project struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	AssetCount int       `json:"assetCount"`
	CreatedAt  time.Time `json:"createdAt"`
	// PreviewAssetIDs are up to six of its videos, spread over the order
	// they were added: the list shows their thumbnails.
	PreviewAssetIDs []string `json:"previewAssetIds,omitempty"`
	// ChosenVersionID is the version the member confirmed as the
	// project's film (#596), and ChosenFilmAssetID that film.
	ChosenVersionID   string `json:"chosenVersionId,omitempty"`
	ChosenFilmAssetID string `json:"chosenFilmAssetId,omitempty"`
}

// ProjectAsset is a video of a project.
type ProjectAsset struct {
	AssetID  string      `json:"assetId"`
	Status   AssetStatus `json:"status"`
	Duration float64     `json:"duration,omitempty"`
	TakenAt  *time.Time  `json:"takenAt,omitempty"`
	AddedAt  time.Time   `json:"addedAt"`
}

// ProjectDetail is one project with its videos.
type ProjectDetail struct {
	Project
	Ask           string         `json:"ask"`
	Chronological bool           `json:"chronological"`
	Assets        []ProjectAsset `json:"assets"`
	// Versions are the films saved, newest first.
	Versions []Version `json:"versions"`
	// Job is the project's latest job, for the page to pick up where it
	// was after a reload.
	Job *Job `json:"job,omitempty"`
}

// ProjectPage is a page of projects, newest first.
type ProjectPage struct {
	Items []Project `json:"items"`
	Next  string    `json:"next,omitempty"`
}

// ProjectChange changes a project's name and settings; nil leaves one.
// SongStart moves the song's excerpt to the downbeat nearest it.
type ProjectChange struct {
	Name          *string  `json:"name,omitempty"`
	Ask           *string  `json:"ask,omitempty"`
	Chronological *bool    `json:"chronological,omitempty"`
	SongStart     *float64 `json:"songStart,omitempty"`
}

// CardPlace is where a card is in its video: the footage it cuts and the
// instants of its beats. The analysis numbers cards by the videos' order in
// the job, and cuts and describes them again when the photo library
// analyses a video again; a card in the same place is the same footage,
// whatever its number.
type CardPlace struct {
	Video string    `json:"video"`
	Start float64   `json:"start"`
	End   float64   `json:"end"`
	Times []float64 `json:"times"`
}

// Key is the place as a map key: its JSON.
func (p CardPlace) Key() string {
	encoded, _ := json.Marshal(p)
	return string(encoded)
}

// SongSource is what the Product Service tells of the track it streams.
type SongSource struct {
	TrackID string `json:"trackId"`
	Title   string `json:"title"`
	// ContentType is how browsers receive the file; empty when they cannot
	// play it and the media worker decodes it to FLAC (music.Track.Transcode).
	ContentType string `json:"contentType"`
}

// Analysis is what the music analysis Worker answers for a song: its
// structure and its loudness, the eighth version's RMS curve (frames of
// 2048 samples every 512 at 22050 Hz).
type Analysis struct {
	Structure videocutcompute.Structure
	Loudness  []float32
	// Seconds is how long the Worker took.
	Seconds float64
}

// SongDetail is a project's song as the music page shows it.
type SongDetail struct {
	ID string `json:"id"`
	// TrackID is the track of the music library the song was copied from.
	TrackID  string  `json:"trackId,omitempty"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
	// Transcode: browsers cannot play the copy; /song/audio decodes it to
	// FLAC from ?start= and cannot seek by Range.
	Transcode bool                      `json:"transcode"`
	State     string                    `json:"state"`
	Reason    string                    `json:"reason,omitempty"`
	Waiting   string                    `json:"waiting,omitempty"`
	Tempo     float64                   `json:"tempo,omitempty"`
	Beats     []float64                 `json:"beats,omitempty"`
	Downbeats []float64                 `json:"downbeats,omitempty"`
	Sections  []videocutcompute.Section `json:"sections,omitempty"`
	// DefaultStart is where the eighth version's rule starts the excerpt;
	// Selection follows the project's own start when the member dragged it.
	DefaultStart *float64                   `json:"defaultStart,omitempty"`
	Selection    *videocutcompute.Selection `json:"selection,omitempty"`
}

// SongAudio is a project's song opened to be heard.
type SongAudio struct {
	File *os.File
	Song SongDetail
	// ContentType is how the file is sent; empty when it is decoded to FLAC.
	ContentType string
	ModifiedAt  time.Time
}
