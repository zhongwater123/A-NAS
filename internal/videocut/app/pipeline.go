package app

// What a job's steps work with besides the photo library's grants
// (docs/architecture/montage-center.md, 整体结构): the compute Worker, the
// FFmpeg Worker, the planner and the songs. Each is an interface, so that
// the steps are tested without either Worker.

// The film: 1920 × 1080 at 30 frames a second, with the song.
const (
	FilmRate   = 30
	FilmWidth  = 1920
	FilmHeight = 1080
	// endingFade fades the last shot to black, in frames (one second).
	endingFade = FilmRate
)
