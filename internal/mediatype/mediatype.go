// Package mediatype names the files A-NAS treats as videos, so that the
// media center catalogs and the file manager previews the same ones (#222).
package mediatype

import (
	"path"
	"strings"
)

// videoExtensions are the containers FFmpeg reads that A-NAS treats as
// videos. The web uploader keeps the same list (web/src/media/uploads.ts).
// F4V is Flash's MP4 and plays like one.
var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".mov": true, ".avi": true, ".wmv": true,
	".flv": true, ".f4v": true, ".ts": true, ".m2ts": true, ".mts": true, ".mpg": true, ".mpeg": true, ".rmvb": true,
	".rm": true, ".3gp": true, ".vob": true, ".ogv": true, ".asf": true, ".divx": true,
}

// IsVideo reports whether name has a video's extension, in any case.
func IsVideo(name string) bool { return videoExtensions[strings.ToLower(path.Ext(name))] }
