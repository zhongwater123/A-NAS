package mediaworker

import (
	"strconv"
)

// Tools names the FFmpeg binaries.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

func seconds(value float64) string { return strconv.FormatFloat(value, 'f', 3, 64) }

// command builds the program and arguments for a validated request. The
// input is always the descriptor on standard input, read through FFmpeg's
// fd: protocol, which can seek in a regular file; the output is standard
// output.
func (t Tools) command(request Request) (string, []string) {
	quiet := []string{"-hide_banner", "-v", "error"}
	switch request.Op {
	case OpProbe:
		return t.FFprobe, append(quiet, "-print_format", "json", "-show_format", "-show_streams", "-i", "fd:")
	case OpFrame:
		args := append([]string{"-nostdin"}, quiet...)
		if request.At > 0 {
			args = append(args, "-ss", seconds(request.At))
		}
		return t.FFmpeg, append(args, "-i", "fd:", "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1",
			"-vf", "scale='min("+strconv.Itoa(request.Width)+",iw)':-2", "-q:v", "3", "-c:v", "mjpeg", "-f", "image2", "pipe:1")
	case OpSubtitle:
		args := append([]string{"-nostdin"}, quiet...)
		if request.Charset != "" {
			args = append(args, "-sub_charenc", request.Charset)
		}
		selected := "0:s:0"
		if request.Stream >= 0 {
			selected = "0:" + strconv.Itoa(request.Stream)
		} else {
			args = append(args, "-f", request.Format)
		}
		return t.FFmpeg, append(args, "-i", "fd:", "-map", selected, "-c:s", "webvtt", "-f", "webvtt", "pipe:1")
	case OpStream:
		args := append([]string{"-nostdin"}, quiet...)
		if request.Start > 0 {
			args = append(args, "-ss", seconds(request.Start))
		}
		args = append(args, "-i", "fd:", "-map", "0:v:0?")
		if request.AudioTrack >= 0 {
			args = append(args, "-map", "0:a:"+strconv.Itoa(request.AudioTrack)+"?")
		}
		args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
		if request.CopyVideo {
			args = append(args, "-c:v", "copy")
			if request.HEVC {
				args = append(args, "-tag:v", "hvc1")
			}
		} else {
			// Short GOPs keep the first fragment, and so the start of
			// playback, close.
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "22", "-pix_fmt", "yuv420p", "-profile:v", "high", "-g", "48")
			if request.Height > 0 {
				args = append(args, "-vf", "scale=-2:'min("+strconv.Itoa(request.Height)+",ih)'")
			}
			if request.MaxBitrate > 0 {
				args = append(args, "-maxrate", strconv.Itoa(request.MaxBitrate)+"k", "-bufsize", strconv.Itoa(request.MaxBitrate*2)+"k")
			}
		}
		if request.AudioTrack >= 0 {
			if request.CopyAudio {
				args = append(args, "-c:a", "copy")
			} else {
				args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
			}
		} else {
			args = append(args, "-an")
		}
		return t.FFmpeg, append(args, "-max_muxing_queue_size", "1024",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	}
	return "", nil
}
