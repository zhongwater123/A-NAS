//go:build linux

// Package montagecompute is the client of the montage center's compute
// Worker (ai/anas_montage/worker.py, docs/architecture/montage-center.md,
// 剪辑计算 Worker): the photo service cuts cards and measures their quality
// with it, the montage service lists the catalog, asks for plans and fills
// the music's slots. One request and one answer per connection, framed as
// the AI Worker's; arrays travel as sealed memfds beside the request.
package montagecompute

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

const (
	// maxFrame is the Worker's frame limit (anas_ai.protocol.MAX_FRAME).
	maxFrame = 1 << 20
	// maxArrays is the most arrays a request passes (worker.MAX_ARRAYS).
	maxArrays = 8
	// callTimeout bounds one call: each is seconds to tens of seconds.
	callTimeout = 5 * time.Minute
)

var (
	// ErrUnavailable: the Worker could not be reached or stopped; the same
	// request may work later.
	ErrUnavailable = errors.New("the montage compute worker is not available")
	// ErrRefused: the Worker refused the request; it would refuse it again.
	ErrRefused = errors.New("the montage compute worker refused the request")
)

// Array is one array a request passes: Float32 or Uint8, with its shape.
type Array struct {
	Name    string
	Shape   []int
	Float32 []float32
	Uint8   []byte
}

func (a Array) bytes() ([]byte, string, error) {
	count := 1
	for _, n := range a.Shape {
		count *= n
	}
	switch {
	case a.Float32 != nil || (a.Uint8 == nil && count == 0):
		if len(a.Float32) != count {
			return nil, "", fmt.Errorf("array %s holds %d values for shape %v", a.Name, len(a.Float32), a.Shape)
		}
		out := make([]byte, 4*len(a.Float32))
		for i, v := range a.Float32 {
			binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(v))
		}
		return out, "float32", nil
	default:
		if len(a.Uint8) != count {
			return nil, "", fmt.Errorf("array %s holds %d bytes for shape %v", a.Name, len(a.Uint8), a.Shape)
		}
		return a.Uint8, "uint8", nil
	}
}

// sealed holds data in a memfd the Worker can only read.
func sealed(name string, data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("anas-montage-"+name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// Client reaches the Worker's socket.
type Client struct {
	SocketPath string
}

type arraySpec struct {
	Name  string `json:"name"`
	DType string `json:"dtype"`
	Shape []int  `json:"shape"`
}

type failure struct {
	OK    bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Call sends op with the fields of request (a struct or map that becomes the
// request's JSON) and arrays, and decodes the answer into answer.
func (c Client) Call(ctx context.Context, op string, request any, arrays []Array, answer any) error {
	if len(arrays) > maxArrays {
		return fmt.Errorf("%w: %d arrays", ErrRefused, len(arrays))
	}
	fields := map[string]any{}
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return fmt.Errorf("a compute request must encode as an object: %w", err)
		}
	}
	specs := make([]arraySpec, 0, len(arrays))
	files := make([]*os.File, 0, len(arrays))
	defer func() { unixmsg.CloseFiles(files) }()
	for _, array := range arrays {
		data, dtype, err := array.bytes()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRefused, err)
		}
		file, err := sealed(array.Name, data)
		if err != nil {
			return err
		}
		files = append(files, file)
		specs = append(specs, arraySpec{Name: array.Name, DType: dtype, Shape: array.Shape})
	}
	fields["op"], fields["arrays"] = op, specs
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unixConn := conn.(*net.UnixConn)
	if err := unixmsg.WriteFiles(unixConn, fields, files, maxFrame); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	body, extra, err := unixmsg.ReadBody(unixConn, maxFrame)
	unixmsg.CloseFile(extra)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var head failure
	if err := json.Unmarshal(body, &head); err != nil {
		return fmt.Errorf("%w: undecodable answer: %v", ErrUnavailable, err)
	}
	if !head.OK {
		if head.Error != nil && head.Error.Code == "invalid_input" {
			return fmt.Errorf("%w: %s", ErrRefused, head.Error.Message)
		}
		message := "no error"
		if head.Error != nil {
			message = head.Error.Code + ": " + head.Error.Message
		}
		return fmt.Errorf("%w: %s", ErrUnavailable, message)
	}
	if answer == nil {
		return nil
	}
	return json.Unmarshal(body, answer)
}

// Window is one window of a video for cutting cards.
type Window struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Card is a card the Worker cut: its bounds in seconds, how many windows it
// spans, the instants of its keyframes and the windows they came from.
type Card struct {
	ID      string    `json:"id"`
	Start   float64   `json:"start"`
	End     float64   `json:"end"`
	Windows int       `json:"windows"`
	Times   []float64 `json:"times"`
	Keys    []int     `json:"keys"`
}

// Cards cuts one video's cards from its windows' vectors (dimensions each)
// and flat marks.
func (c Client) Cards(ctx context.Context, key string, windows []Window, vectors []float32, dimensions int, flat []bool) ([]Card, error) {
	marks := make([]byte, len(flat))
	for i, f := range flat {
		if f {
			marks[i] = 1
		}
	}
	var answer struct {
		Cards []Card `json:"cards"`
	}
	err := c.Call(ctx, "cards", map[string]any{"key": key, "windows": windows}, []Array{
		{Name: "vectors", Shape: []int{len(windows), dimensions}, Float32: vectors},
		{Name: "flat", Shape: []int{len(flat)}, Uint8: marks},
	}, &answer)
	return answer.Cards, err
}

// Shake frames are 320 × 180 grey, ten a second.
const (
	ShakeWidth  = 320
	ShakeHeight = 180
)

// ShakeMoves measures the moves between neighbouring frames of one chunk of
// a card's shake frames.
func (c Client) ShakeMoves(ctx context.Context, frames []byte, count int) ([][2]float64, error) {
	var answer struct {
		Moves [][2]float64 `json:"moves"`
	}
	err := c.Call(ctx, "quality_part", map[string]any{"kind": "shake"},
		[]Array{{Name: "frames", Shape: []int{count, ShakeHeight, ShakeWidth}, Uint8: frames}}, &answer)
	return answer.Moves, err
}

// KeyframeMeasure is what one keyframe tells of a card's quality.
type KeyframeMeasure struct {
	Blocked float64 `json:"blocked"`
	Light   float64 `json:"light"`
}

// Keyframe measures one encoded keyframe: how much of it something covers,
// and how light it is.
func (c Client) Keyframe(ctx context.Context, jpeg []byte) (KeyframeMeasure, error) {
	var answer KeyframeMeasure
	err := c.Call(ctx, "quality_part", map[string]any{"kind": "keyframe"},
		[]Array{{Name: "jpeg", Shape: []int{len(jpeg)}, Uint8: jpeg}}, &answer)
	return answer, err
}

// SharpPoints counts the sharp points per million pixels of a grey frame.
func (c Client) SharpPoints(ctx context.Context, frame []byte, width, height int) (float64, error) {
	var answer struct {
		Points float64 `json:"points"`
	}
	err := c.Call(ctx, "quality_part", map[string]any{"kind": "sharp"},
		[]Array{{Name: "frame", Shape: []int{height, width}, Uint8: frame}}, &answer)
	return answer.Points, err
}

// QualityInput is a card's measures, keyframe by keyframe; nil blur is a
// black keyframe, nil points a light one.
type QualityInput struct {
	Moves   [][2]float64 `json:"moves"`
	Blurs   []*float64   `json:"blurs"`
	Blocked []float64    `json:"blocked"`
	Points  []*float64   `json:"points"`
}

// Quality is a card's quality and why it cannot be used, if it cannot.
type Quality struct {
	Blur    *float64   `json:"blur"`
	Shake   float64    `json:"shake"`
	Blocked float64    `json:"blocked"`
	Points  []*float64 `json:"points"`
	Reasons []string   `json:"reasons"`
}

// Quality judges a card from its measures.
func (c Client) Quality(ctx context.Context, input QualityInput) (Quality, error) {
	var answer Quality
	err := c.Call(ctx, "quality", input, nil, &answer)
	return answer, err
}
