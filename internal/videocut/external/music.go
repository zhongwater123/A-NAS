// Package external reaches the rest of the NAS for the montage center: the
// Workers, the photo library and the planner. It implements the interfaces
// in internal/videocut/app/ports.go and never imports app; videocut.go
// hands its values to app (docs/architecture/montage-center.md, 代码结构).
package external

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// The music analysis Worker speaks the AI Worker's framing
// (anas_ai.protocol): one request and one answer per connection, a four-byte
// big-endian length and JSON, descriptors beside the request. Its song
// arrives as two streams, not arrays, so it is not one of the compute
// Worker's calls (internal/montagecompute), which reads memfds.

// maxWorkerFrame bounds a Worker's answer, as anas_ai.protocol.MAX_FRAME.
const maxWorkerFrame = 1 << 20

// Worker error codes (anas_ai.protocol).
const (
	workerInvalidInput = "invalid_input"
	workerUnavailable  = "unavailable"
)

type workerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// streamsComplete is the frame that tells a Worker the streams it read to
// their end were written whole (anas_montage_music.worker, "confirm").
var streamsComplete = map[string]string{"streams": "complete"}

// callWorker sends request with files to the Worker on socket and decodes
// its answer into answer. A Worker that cannot be reached, or says it cannot
// serve, is ErrWorkerUnavailable; one that took the request and then
// reported an error of its own, stopped answering or ran out of time is
// ErrWorkerFailed.
//
// With whole, the files are streams still being written: once whole yields
// nil the Worker is told they are complete, and only then works on them. An
// error from whole ends the call with that error and hangs up, so the Worker
// drops what it read: the end of a pipe alone does not tell a song from one
// cut short.
func callWorker(ctx context.Context, socket string, timeout time.Duration, request any, files []*os.File, whole <-chan error, answer any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("%w: %v", videocutcompute.ErrWorkerUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unixConn := conn.(*net.UnixConn)
	err = unixmsg.WriteFiles(unixConn, request, files, maxWorkerFrame)
	runtime.KeepAlive(files)
	if err != nil {
		return fmt.Errorf("%w: sending: %v", videocutcompute.ErrWorkerUnavailable, err)
	}
	type reply struct {
		raw json.RawMessage
		err error
	}
	// The Worker may answer before the streams end, when it refuses them.
	answered := make(chan reply, 1)
	go func() {
		var raw json.RawMessage
		received, err := unixmsg.ReadFiles(unixConn, &raw, maxWorkerFrame, 0)
		unixmsg.CloseFiles(received)
		answered <- reply{raw, err}
	}()
	var got reply
	select {
	case got = <-answered:
	case err := <-whole:
		if err != nil {
			return err
		}
		if err := unixmsg.Write(unixConn, streamsComplete, nil, maxWorkerFrame); err != nil {
			return fmt.Errorf("%w: confirming the streams: %v", videocutcompute.ErrWorkerFailed, err)
		}
		got = <-answered
	}
	if got.err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w: %v", videocutcompute.ErrWorkerFailed, ctxErr, got.err)
		}
		return fmt.Errorf("%w: reading: %v", videocutcompute.ErrWorkerFailed, got.err)
	}
	var status struct {
		OK    bool         `json:"ok"`
		Error *workerError `json:"error"`
	}
	if err := json.Unmarshal(got.raw, &status); err != nil {
		return fmt.Errorf("%w: %v", videocutcompute.ErrWorkerFailed, err)
	}
	if !status.OK {
		switch {
		case status.Error == nil:
			return fmt.Errorf("%w: failed without a reason", videocutcompute.ErrWorkerFailed)
		case status.Error.Code == workerInvalidInput:
			return fmt.Errorf("%w: %s", videocutcompute.ErrWorkerRefused, status.Error.Message)
		case status.Error.Code == workerUnavailable:
			return fmt.Errorf("%w: %s", videocutcompute.ErrWorkerUnavailable, status.Error.Message)
		}
		return fmt.Errorf("%w: %s: %s", videocutcompute.ErrWorkerFailed, status.Error.Code, status.Error.Message)
	}
	if err := json.Unmarshal(got.raw, answer); err != nil {
		return fmt.Errorf("%w: %v", videocutcompute.ErrWorkerFailed, err)
	}
	return nil
}

// MusicWorker reaches the music analysis Worker
// (anas-videocut-music.socket): allin1 in a runtime of its own, one song at
// a time.
type MusicWorker struct {
	Socket string
	// Timeout bounds one song; zero is 30 minutes, several times what the
	// longest song the montage center takes needs.
	Timeout time.Duration
}

// musicAnswer is the Worker's answer to analyze.
type musicAnswer struct {
	videocutcompute.Structure
	// Loudness is base64 of little-endian float32.
	Loudness string  `json:"loudness"`
	Seconds  float64 `json:"seconds"`
}

// Analyze hands the two streams to the Worker; it reads them to their end,
// and analyses them once whole says they were written whole.
func (w MusicWorker) Analyze(ctx context.Context, stereo, mono *os.File, whole <-chan error) (domain.Analysis, error) {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	request := map[string]any{"op": "analyze", "confirm": true, "streams": []map[string]any{
		{"name": "stereo", "channels": 2, "rate": domain.StereoRate},
		{"name": "mono", "channels": 1, "rate": domain.MonoRate},
	}}
	var answer musicAnswer
	if err := callWorker(ctx, w.Socket, timeout, request, []*os.File{stereo, mono}, whole, &answer); err != nil {
		return domain.Analysis{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(answer.Loudness)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("%w: loudness: %v", videocutcompute.ErrWorkerFailed, err)
	}
	loudness, err := domain.DecodeLoudness(raw)
	if err != nil || len(answer.Downbeats) == 0 && len(answer.Beats) == 0 && len(loudness) == 0 {
		return domain.Analysis{}, fmt.Errorf("%w: the answer has no song in it: %v", videocutcompute.ErrWorkerFailed, err)
	}
	return domain.Analysis{Structure: answer.Structure, Loudness: loudness, Seconds: answer.Seconds}, nil
}
