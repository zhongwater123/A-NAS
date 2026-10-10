// Package mediaworker runs FFmpeg for the media center (ADR 0017).
//
// In production systemd starts one worker process per connection on
// /run/a-nas-media/media.sock, under a dynamic identity with no network and
// no view of the data volume or the Product Service's state. The Product
// Service sends a typed request with the video's read-only descriptor
// attached; the worker builds the FFmpeg command itself, so a request can
// never name a path or pass arbitrary arguments. A frame is a 4-byte
// big-endian length and JSON; after the response header the body runs to
// the end of the connection.
//
// Development runs the same server on one end of a socket pair in-process
// (Local), so both paths share the protocol and the commands.
package mediaworker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"syscall"
)

const (
	OpProbe    = "probe"
	OpFrame    = "frame"
	OpSubtitle = "subtitle"
	OpStream   = "stream"
)

// Error codes in a response.
const (
	// CodeUnreadable: FFmpeg cannot read this input; it would fail again.
	CodeUnreadable = "unreadable"
	CodeInvalid    = "invalid_request"
	CodeFailed     = "failed"
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
}

type Response struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
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
	case OpSubtitle:
		if r.Stream < -1 || r.Stream > 4096 || !charsets[r.Charset] || (r.Stream == -1 && !subtitleFormats[r.Format]) {
			return errors.New("invalid subtitle request")
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

func writeFrame(conn *net.UnixConn, value any, file *os.File) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > maxFrame {
		return errors.New("media worker message is too large")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(body)))
	var rights []byte
	if file != nil {
		rights = syscall.UnixRights(int(file.Fd()))
	}
	if _, _, err := conn.WriteMsgUnix(header, rights, nil); err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}

// readFrame reads one message and the descriptor attached to it, if any.
func readFrame(conn *net.UnixConn, value any) (*os.File, error) {
	header := make([]byte, 4)
	rights := make([]byte, syscall.CmsgSpace(4*4))
	count, rightsCount, _, _, err := conn.ReadMsgUnix(header, rights)
	if count == 0 && err != nil {
		return nil, err
	}
	file, rightsErr := receivedFile(rights[:rightsCount])
	if count < len(header) {
		if _, err := io.ReadFull(conn, header[count:]); err != nil {
			closeFile(file)
			return nil, err
		}
	}
	if rightsErr != nil {
		closeFile(file)
		return nil, rightsErr
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxFrame {
		closeFile(file)
		return nil, errors.New("media worker message is too large")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		closeFile(file)
		return nil, err
	}
	if err := json.Unmarshal(body, value); err != nil {
		closeFile(file)
		return nil, fmt.Errorf("decode media worker message: %w", err)
	}
	return file, nil
}

func receivedFile(rights []byte) (*os.File, error) {
	if len(rights) == 0 {
		return nil, nil
	}
	messages, err := syscall.ParseSocketControlMessage(rights)
	if err != nil {
		return nil, err
	}
	var descriptors []int
	for _, message := range messages {
		received, err := syscall.ParseUnixRights(&message)
		if err != nil {
			return nil, err
		}
		descriptors = append(descriptors, received...)
	}
	if len(descriptors) == 0 {
		return nil, nil
	}
	for _, extra := range descriptors[1:] {
		_ = syscall.Close(extra)
	}
	return os.NewFile(uintptr(descriptors[0]), "media"), nil
}

func closeFile(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}
