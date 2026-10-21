package mediaworker

import (
	"strconv"
)

// Tools names the media decoder binaries.
type Tools struct {
	FFmpeg          string
	FFprobe         string
	HEIFThumbnailer string
}

func seconds(value float64) string { return strconv.FormatFloat(value, 'f', 3, 64) }

// command builds the program and arguments for a validated request. The
// input is always the descriptor on standard input, read through FFmpeg's
// fd: protocol or libheif reopening worker-owned memory; output is standard
// output.
func (t Tools) command(request Request) (string, []string) {
	quiet := []string{"-hide_banner", "-v", "error"}
	switch request.Op {
	case OpImage:
		return t.HEIFThumbnailer, []string{"-p", "-s", strconv.Itoa(request.Width), "/proc/self/fd/0", "/proc/self/fd/1"}
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
		// A fragment at least every second, not only at each keyframe:
		// sources with long GOPs otherwise hold back the first picture,
		// and the start after a seek, for a whole GOP.
		return t.FFmpeg, append(args, "-max_muxing_queue_size", "1024", "-frag_duration", "1000000",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	case OpAudio:
		// FLAC keeps decoded audio exactly, so a lossy source is not
		// encoded lossily a second time; the fastest level suits streaming.
		args := append([]string{"-nostdin"}, quiet...)
		if request.Start > 0 {
			args = append(args, "-ss", seconds(request.Start))
		}
		args = append(args, "-i", "fd:", "-map", "0:a:0", "-vn", "-sn", "-dn", "-map_metadata", "-1")
		if request.SampleRate > 0 {
			args = append(args, "-ar", strconv.Itoa(request.SampleRate))
		}
		args = append(args, "-c:a", "flac", "-compression_level", "1")
		if request.Bits == 24 {
			args = append(args, "-sample_fmt", "s32", "-bits_per_raw_sample", "24")
		} else {
			args = append(args, "-sample_fmt", "s16")
		}
		return t.FFmpeg, append(args, "-f", "flac", "pipe:1")
	case OpSpeech:
		// Recognition services and the voiceprint model take 16 kHz mono;
		// Opus at 24 kbps keeps an hour of speech near 11 MB to upload.
		args := append([]string{"-nostdin"}, quiet...)
		if request.Start > 0 {
			args = append(args, "-ss", seconds(request.Start))
		}
		args = append(args, "-i", "fd:")
		if request.Duration > 0 {
			args = append(args, "-t", seconds(request.Duration))
		}
		args = append(args, "-map", "0:a:0", "-vn", "-sn", "-dn", "-map_metadata", "-1", "-ac", "1", "-ar", "16000")
		if request.Format == SpeechOpus {
			return t.FFmpeg, append(args, "-c:a", "libopus", "-b:a", "24k", "-application", "voip", "-f", "ogg", "pipe:1")
		}
		return t.FFmpeg, append(args, "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1")
	}
	return "", nil
}
