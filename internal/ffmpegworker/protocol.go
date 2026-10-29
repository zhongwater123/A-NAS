// Package ffmpegworker is the one place A-NAS runs media decoders (ADR 0017,
// ADR 0018). It knows nothing about the apps that use it: the media center,
// music library and ordinary file previews adapt its typed operations.
//
// In production systemd starts one worker process per connection on
// /run/a-nas-ffmpeg/ffmpeg.sock, under a dynamic identity with no network and
// no view of the data volume or the Product Service's state. The Product
// Service sends a typed request with the file's read-only descriptor
// attached; the worker builds the decoder command itself, so a request can
// never name a path or pass arbitrary arguments. A frame is a 4-byte
// big-endian length and JSON; after the response header the body runs to
// the end of the connection.
//
// Development runs the same server on one end of a socket pair in-process
// (Local), so both paths share the protocol and the commands.
package ffmpegworker

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

const (
	OpProbe    = "probe"
	OpFrame    = "frame"
	OpImage    = "image"
	OpSubtitle = "subtitle"
	// OpStream converts a video to fragmented MP4.
	OpStream = "stream"
	// OpAudio decodes the first audio stream to FLAC.
	OpAudio = "audio"
)

var (
	// ErrUnavailable reports that no worker took the request: none could be
	// reached, none was free, or it has no decoder for the request. The same
	// request may succeed later.
	ErrUnavailable = errors.New("FFmpeg worker is unavailable")
	// ErrBusy reports that every slot a request may use stayed taken while
	// someone waited for it. It is also ErrUnavailable.
	ErrBusy = fmt.Errorf("%w: every slot is taken", ErrUnavailable)
	// ErrInterrupted reports that a worker took the request and then stopped
	// before it finished, as when its decoder ran out of memory: the input
	// may be the cause. It is also ErrUnavailable, for callers that only
	// tell someone to try again.
	ErrInterrupted = fmt.Errorf("%w: it stopped before it finished", ErrUnavailable)
	// ErrFailed reports that the worker could not finish, as when its decoder
	// ran out of time.
	ErrFailed = errors.New("FFmpeg worker failed")
	// ErrUnreadable reports that FFmpeg cannot read the file.
	ErrUnreadable = errors.New("FFmpeg worker cannot read the file")
	// ErrInvalid reports a request the worker refused.
	ErrInvalid = errors.New("FFmpeg worker request is invalid")
)

// Error codes in a response.
const (
	// CodeUnreadable: FFmpeg cannot read this input; it would fail again.
	CodeUnreadable  = "unreadable"
	CodeInvalid     = "invalid_request"
	CodeFailed      = "failed"
	CodeUnavailable = "unavailable"
)

const maxFrame = 1 << 16

type Request struct {
	Op string `json:"op"`
	// Frame.
	At    float64 `json:"at,omitempty"`
	Width int     `json:"width,omitempty"`
	// Subtitle: an external file in Format and Charset, or Stream.
	Format  string `json:"format,omitempty"`
	Charset string `json:"charset,omitempty"`
	Stream  int    `json:"stream"`
	// Stream.
	Start      float64 `json:"start,omitempty"`
	CopyVideo  bool    `json:"copyVideo,omitempty"`
	HEVC       bool    `json:"hevc,omitempty"`
	Height     int     `json:"height,omitempty"`
	MaxBitrate int     `json:"maxBitrate,omitempty"`
	AudioTrack int     `json:"audioTrack"`
	CopyAudio  bool    `json:"copyAudio,omitempty"`
	// Audio: the output sample rate (0 keeps the source's) and bits per
	// sample, 16 or 24. Start is shared with Stream.
	SampleRate int `json:"sampleRate,omitempty"`
	Bits       int `json:"bits,omitempty"`
	// Ack asks the worker to say it has the request before it starts, so
	// that a caller can tell a connection systemd dropped from a worker that
	// stopped while working.
	Ack bool `json:"ack,omitempty"`
}

type Response struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Accepted marks the acknowledgement that precedes the response when
	// the request asked for one.
	Accepted bool `json:"accepted,omitempty"`
}

var subtitleFormats = map[string]bool{"srt": true, "ass": true, "webvtt": true}
var charsets = map[string]bool{"": true, "GB18030": true, "UTF-16": true, "BIG5": true}

func finiteIn(value, low, high float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= low && value <= high
}

// Validate bounds every field the commands use.
func (r Request) Validate() error {
	switch r.Op {
	case OpProbe:
	case OpFrame:
		if !finiteIn(r.At, 0, 1e6) || r.Width < 16 || r.Width > 3840 {
			return errors.New("invalid frame request")
		}
	case OpImage:
		if r.Width != 320 && r.Width != 1600 {
			return errors.New("invalid image request")
		}
	case OpSubtitle:
		if r.Stream < -1 || r.Stream > 4096 || !charsets[r.Charset] || (r.Stream == -1 && !subtitleFormats[r.Format]) {
			return errors.New("invalid subtitle request")
		}
	case OpAudio:
		if !finiteIn(r.Start, 0, 1e6) || r.SampleRate != 0 && (r.SampleRate < 8000 || r.SampleRate > 384000) || r.Bits != 16 && r.Bits != 24 {
			return errors.New("invalid audio request")
		}
	case OpStream:
		if !finiteIn(r.Start, 0, 1e6) || r.AudioTrack < -1 || r.AudioTrack > 64 || r.MaxBitrate < 0 || r.MaxBitrate > 100_000 ||
			r.Height != 0 && (r.Height < 144 || r.Height > 2160) || r.HEVC && !r.CopyVideo {
			return errors.New("invalid stream request")
		}
	default:
		return fmt.Errorf("unknown operation %q", r.Op)
	}
	return nil
}

// writeFrame and readFrame speak internal/unixmsg with the worker's limit.
func writeFrame(conn *net.UnixConn, value any, file *os.File) error {
	return unixmsg.Write(conn, value, file, maxFrame)
}

// readFrame reads one message and the descriptor attached to it, if any.
func readFrame(conn *net.UnixConn, value any) (*os.File, error) {
	return unixmsg.Read(conn, value, maxFrame)
}

func closeFile(file *os.File) { unixmsg.CloseFile(file) }
