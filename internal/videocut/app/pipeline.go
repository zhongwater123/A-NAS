package app

import "github.com/zhongwater123/A-NAS/internal/videocut/domain"

// What a job's steps work with besides the photo library's grants
// (docs/architecture/videocut-center.md, 整体结构): the compute Worker, the
// FFmpeg Worker, the planner and the songs. Each is an interface, so that
// the steps are tested without either Worker.

const (
	// endingFade fades the last shot to black, in frames (one second).
	endingFade = domain.FilmRate
)
