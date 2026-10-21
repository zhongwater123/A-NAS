// Package voiceworker is the Product Service's client for the voice Worker
// (ai/anas_ai/voice.py, docs/architecture/voice.md). The Worker turns speech
// into speaker vectors on the device, without network and without the data
// volume (ADR 0027). Every call dials its socket, which systemd activates on
// demand, sends one request and reads one response; the framing is the
// photo AI Worker's: a 4-byte big-endian length followed by JSON. The
// samples, 16 kHz mono 16-bit little-endian, travel as one memfd descriptor
// in the request's control message, and requests name stretches of them by
// sample offset.
package voiceworker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// SampleRate is the rate of the samples the Worker takes.
const SampleRate = 16000

// Limits the Worker enforces.
const (
	MaxFrame    = 1 << 20
	MaxSamples  = SampleRate * 60 * 30
	MinSegment  = SampleRate / 2
	MaxSegment  = SampleRate * 60
	MaxSegments = 4000
	MaxGroups   = 64
)

const (
	OpInfo          = "info"
	OpEmbedSpeakers = "embed_speakers"
	OpClusterSpeech = "cluster_speech"
	OpRelease       = "release"
)

const (
	CodeInvalidInput = "invalid_input"
	CodeUnavailable  = "unavailable"
)

var (
	// ErrUnavailable: no Worker could be reached, or it runs without its model.
	ErrUnavailable = errors.New("voice worker is unavailable")
	// ErrRejected: the Worker cannot use this input; it would fail again.
	ErrRejected = errors.New("voice worker rejected the speech")
)

// Segment is a stretch of the samples: Count samples from Offset.
type Segment struct {
	Offset int
	Count  int
}

func (s Segment) MarshalJSON() ([]byte, error) { return json.Marshal([2]int{s.Offset, s.Count}) }

type Info struct {
	Model      string
	Dimensions int
	SampleRate int
}

type request struct {
	Op        string      `json:"op"`
	What      string      `json:"what,omitempty"`
	Groups    [][]Segment `json:"groups,omitempty"`
	Segments  []Segment   `json:"segments,omitempty"`
	Threshold float64     `json:"threshold,omitempty"`
}

type response struct {
	OK         bool     `json:"ok"`
	Model      string   `json:"model,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
	SampleRate int      `json:"sampleRate,omitempty"`
	Vectors    [][]byte `json:"vectors,omitempty"`
	Labels     []int    `json:"labels,omitempty"`
	Centroids  [][]byte `json:"centroids,omitempty"`
	Error      *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Client reaches the Worker at SocketPath.
type Client struct {
	SocketPath string
	// Timeout bounds one call, including loading the model after the Worker
	// exited while idle; it defaults to ten minutes, enough for half an hour
	// of speech on a slow CPU.
	Timeout time.Duration
}

func (c Client) Info(ctx context.Context) (Info, error) {
	answer, err := c.call(ctx, request{Op: OpInfo}, nil)
	if err != nil {
		return Info{}, err
	}
	if answer.Model == "" || answer.Dimensions <= 0 || answer.SampleRate != SampleRate {
		return Info{}, errors.New("voice worker described no usable model")
	}
	return Info{Model: answer.Model, Dimensions: answer.Dimensions, SampleRate: answer.SampleRate}, nil
}

// EmbedSpeakers returns one unit vector per group of segments: the
// length-weighted mean of its segments' vectors.
func (c Client) EmbedSpeakers(ctx context.Context, samples []int16, groups [][]Segment) (string, [][]float32, error) {
	if len(groups) == 0 || len(groups) > MaxGroups {
		return "", nil, fmt.Errorf("%w: %d groups", ErrRejected, len(groups))
	}
	pcm, err := samplesFile(samples)
	if err != nil {
		return "", nil, err
	}
	defer pcm.Close()
	answer, err := c.call(ctx, request{Op: OpEmbedSpeakers, Groups: groups}, pcm)
	if err != nil {
		return "", nil, err
	}
	if len(answer.Vectors) != len(groups) || answer.Model == "" {
		return "", nil, errors.New("voice worker answered with a vector per group it was not sent")
	}
	vectors, err := decodeAll(answer.Vectors)
	return answer.Model, vectors, err
}

// ClusterSpeech groups the segments by voice: labels hold one cluster per
// segment, centroids one unit vector per cluster. threshold is the cosine
// distance below which two voices are one.
func (c Client) ClusterSpeech(ctx context.Context, samples []int16, segments []Segment, threshold float64) (string, []int, [][]float32, error) {
	pcm, err := samplesFile(samples)
	if err != nil {
		return "", nil, nil, err
	}
	defer pcm.Close()
	answer, err := c.call(ctx, request{Op: OpClusterSpeech, Segments: segments, Threshold: threshold}, pcm)
	if err != nil {
		return "", nil, nil, err
	}
	if len(answer.Labels) != len(segments) || answer.Model == "" {
		return "", nil, nil, errors.New("voice worker answered with a label per segment it was not sent")
	}
	centroids, err := decodeAll(answer.Centroids)
	if err != nil {
		return "", nil, nil, err
	}
	for _, label := range answer.Labels {
		if label < 0 || label >= len(centroids) {
			return "", nil, nil, errors.New("voice worker answered with a label it has no centroid for")
		}
	}
	return answer.Model, answer.Labels, centroids, nil
}

// Release asks the Worker to exit after answering, handing its memory back.
func (c Client) Release(ctx context.Context) error {
	_, err := c.call(ctx, request{Op: OpRelease, What: "all"}, nil)
	return err
}

// samplesFile copies samples into an anonymous memory file, so the Worker
// reads them without either side touching a disk.
func samplesFile(samples []int16) (*os.File, error) {
	if len(samples) == 0 || len(samples) > MaxSamples {
		return nil, fmt.Errorf("%w: %d samples", ErrRejected, len(samples))
	}
	fd, err := unix.MemfdCreate("anas-speech", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "anas-speech")
	if _, err := file.Write(littleEndian(samples)); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// littleEndian is samples as little-endian bytes.
func littleEndian(samples []int16) []byte {
	data := make([]byte, 2*len(samples))
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(sample))
	}
	return data
}

func decodeAll(encoded [][]byte) ([][]float32, error) {
	vectors := make([][]float32, len(encoded))
	for i, value := range encoded {
		if len(value) == 0 || len(value)%4 != 0 {
			return nil, errors.New("voice worker returned a malformed vector")
		}
		vector := make([]float32, len(value)/4)
		for j := range vector {
			vector[j] = math.Float32frombits(binary.LittleEndian.Uint32(value[4*j:]))
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func (c Client) call(ctx context.Context, payload request, file *os.File) (response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	if len(body) > MaxFrame {
		return response{}, fmt.Errorf("%w: request of %d bytes", ErrRejected, len(body))
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	frame = append(frame, body...)
	var rights []byte
	if file != nil {
		rights = unix.UnixRights(int(file.Fd()))
	}
	_, _, err = conn.(*net.UnixConn).WriteMsgUnix(frame, rights, nil)
	runtime.KeepAlive(file)
	if err != nil {
		return response{}, fmt.Errorf("%w: send: %v", ErrUnavailable, err)
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return response{}, fmt.Errorf("%w: %v", ctxErr, err)
		}
		return response{}, fmt.Errorf("%w: read: %v", ErrUnavailable, err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > MaxFrame {
		return response{}, fmt.Errorf("voice worker response of %d bytes exceeds the limit", length)
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return response{}, fmt.Errorf("%w: read: %v", ErrUnavailable, err)
	}
	var answer response
	if err := json.Unmarshal(raw, &answer); err != nil {
		return response{}, fmt.Errorf("decode voice worker response: %w", err)
	}
	if !answer.OK {
		if answer.Error == nil {
			return response{}, errors.New("voice worker failed without a reason")
		}
		switch answer.Error.Code {
		case CodeInvalidInput:
			return response{}, fmt.Errorf("%w: %s", ErrRejected, answer.Error.Message)
		case CodeUnavailable:
			return response{}, fmt.Errorf("%w: %s", ErrUnavailable, answer.Error.Message)
		}
		return response{}, fmt.Errorf("voice worker failed: %s: %s", answer.Error.Code, answer.Error.Message)
	}
	return answer, nil
}
