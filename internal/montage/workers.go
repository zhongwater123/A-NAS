package montage

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

// callWorker sends request with files to the Worker on socket and decodes
// its answer into answer.
func callWorker(ctx context.Context, socket string, timeout time.Duration, request any, files []*os.File, answer any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWorkerUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unixConn := conn.(*net.UnixConn)
	err = unixmsg.WriteFiles(unixConn, request, files, maxWorkerFrame)
	runtime.KeepAlive(files)
	if err != nil {
		return fmt.Errorf("%w: sending: %v", ErrWorkerUnavailable, err)
	}
	var raw json.RawMessage
	received, err := unixmsg.ReadFiles(unixConn, &raw, maxWorkerFrame, 0)
	unixmsg.CloseFiles(received)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w: %v", ErrWorkerUnavailable, ctxErr, err)
		}
		return fmt.Errorf("%w: reading: %v", ErrWorkerUnavailable, err)
	}
	var status struct {
		OK    bool         `json:"ok"`
		Error *workerError `json:"error"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("%w: %v", ErrWorkerUnavailable, err)
	}
	if !status.OK {
		if status.Error == nil {
			return fmt.Errorf("%w: failed without a reason", ErrWorkerUnavailable)
		}
		if status.Error.Code == workerInvalidInput {
			return fmt.Errorf("%w: %s", ErrWorkerRefused, status.Error.Message)
		}
		return fmt.Errorf("%w: %s: %s", ErrWorkerUnavailable, status.Error.Code, status.Error.Message)
	}
	return json.Unmarshal(raw, answer)
}

// MusicWorker reaches the music analysis Worker
// (anas-montage-music.socket): allin1 in a runtime of its own, one song at
// a time.
type MusicWorker struct {
	Socket string
	// Timeout bounds one song; zero is 30 minutes, for a 15-minute song on
	// a slow processor.
	Timeout time.Duration
}

// musicAnswer is the Worker's answer to analyze.
type musicAnswer struct {
	Structure
	// Loudness is base64 of little-endian float32.
	Loudness string  `json:"loudness"`
	Seconds  float64 `json:"seconds"`
}

// Analyze hands the two streams to the Worker; it reads them to their end.
func (w MusicWorker) Analyze(ctx context.Context, stereo, mono *os.File) (Analysis, error) {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	request := map[string]any{"op": "analyze", "streams": []map[string]any{
		{"name": "stereo", "channels": 2, "rate": stereoRate},
		{"name": "mono", "channels": 1, "rate": monoRate},
	}}
	var answer musicAnswer
	if err := callWorker(ctx, w.Socket, timeout, request, []*os.File{stereo, mono}, &answer); err != nil {
		return Analysis{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(answer.Loudness)
	if err != nil {
		return Analysis{}, fmt.Errorf("%w: loudness: %v", ErrWorkerRefused, err)
	}
	loudness, err := decodeLoudness(raw)
	if err != nil || len(answer.Downbeats) == 0 && len(answer.Beats) == 0 && len(loudness) == 0 {
		return Analysis{}, fmt.Errorf("%w: the answer has no song in it: %v", ErrWorkerRefused, err)
	}
	return Analysis{Structure: answer.Structure, Loudness: loudness, Seconds: answer.Seconds}, nil
}

var _ MusicAnalyzer = MusicWorker{}
