// Package domain holds the montage center's data types, the rules of its
// draft operations and the errors every layer shares. It does no input or
// output and imports no other layer (docs/architecture/montage-center.md,
// 代码结构).
package domain

import "errors"

// Draft errors.
var (
	// ErrDraftConflict: the draft moved on since the revision the step was
	// made on, as when another window changed it.
	ErrDraftConflict = errors.New("the draft changed since")
	// ErrNotEditable: the version's timeline is not known; it can only be
	// played.
	ErrNotEditable = errors.New("the version cannot be edited")
	// ErrAnalysisChanged: the photo library analysed the version's videos
	// again with another model since the version was cut. The version can
	// only be played, or recut (docs/architecture/montage-center.md, 出片后的二次编辑:
	// 修改稿).
	ErrAnalysisChanged = errors.New("the videos were analysed again with another model")
	// ErrSearchUnavailable: the photo library cannot search windows now.
	ErrSearchUnavailable = errors.New("searching the videos is not available")
	// ErrSearchModel: the text model is not the one the windows were
	// analysed with; they are analysed again first.
	ErrSearchModel = errors.New("the windows were analysed with another model")
)

// Song errors.
var (
	// ErrSongAnalyzing: the song's analysis is not done yet.
	ErrSongAnalyzing = errors.New("the song is still being analysed")
	// ErrSongUnusable: the song cannot be used, or none was chosen.
	ErrSongUnusable = errors.New("the song cannot be used")
)

var (
	// ErrJobActive: the project already has a job in progress.
	ErrJobActive = errors.New("the project already has a job in progress")
	// ErrJobState: the job is not in a state that allows this.
	ErrJobState = errors.New("the job cannot do this now")
	// ErrJobsUnavailable: this service was started without what jobs need.
	ErrJobsUnavailable = errors.New("the montage center cannot make films here")
)

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

// Planning errors the planner and the tools are told.
var (
	// ErrNoPlanWanted: no job of the member waits for a round.
	ErrNoPlanWanted = errors.New("no job waits for planning")
	// ErrRoundOver: the round was abandoned, ended or timed out.
	ErrRoundOver = errors.New("this round is over")
	// ErrJobCancelled: the job was cancelled; the round should stop.
	ErrJobCancelled = errors.New("the job was cancelled")
)

var (
	// ErrNotFound covers another member's project as well as a missing one.
	ErrNotFound = errors.New("montage project not found")
	// ErrInvalid is a request the montage center refuses as it stands.
	ErrInvalid = errors.New("invalid montage request")
	// ErrTooManyAssets names more than MaxAssets videos at once.
	ErrTooManyAssets = errors.New("too many videos in one request")
	// ErrAssetsRefused: the photo library would not grant a video. Missing,
	// hidden, trashed and non-video assets answer alike.
	ErrAssetsRefused = errors.New("a video cannot be added")
	// ErrConflict: a request key the member used for another request, of
	// another kind, on another project or with other videos.
	ErrConflict = errors.New("the request key was used for another request")
	// ErrGrantRevoked: the photo library revoked the project's grant, as
	// when montage.db was restored to before the project's deletion; the
	// project can take no more videos.
	ErrGrantRevoked = errors.New("the project's grant was revoked")
	// ErrPhotosUnavailable: the photo library did not answer, or cannot now.
	ErrPhotosUnavailable = errors.New("the photo library is not available")
	// ErrSessionEnded: the member's session is no longer valid.
	ErrSessionEnded = errors.New("the member's session is not valid")
)

var (
	// ErrSongTooLarge: the file is larger than MaxSongBytes (413).
	ErrSongTooLarge = errors.New("the song is larger than 100 MiB")
	// ErrSongLength: the song is shorter than MinSongSeconds or longer than
	// MaxSongSeconds (422).
	ErrSongLength = errors.New("the song is shorter than 30 seconds or longer than 8 minutes")
	// ErrNotAudio: FFmpeg finds no audio in the file (422).
	ErrNotAudio = errors.New("the file is not audio")
	// ErrNoSong: the project has no song yet (404).
	ErrNoSong = errors.New("the project has no song")
	// ErrSongNotReady: the song is still being analysed, or cannot be used
	// (409).
	ErrSongNotReady = errors.New("the song is not analysed")
	// ErrSongStart: no usable excerpt starts at the downbeat asked for (422).
	ErrSongStart = errors.New("no usable excerpt starts there")
	// ErrMediaUnavailable: the media worker did not answer (503).
	ErrMediaUnavailable = errors.New("the media worker is not available")
)
