// Package aiworker is the photo service's client for the AI Worker
// (docs/architecture/photo-ai.md). Every call dials the Worker's Unix
// socket, which systemd activates on demand, sends one request and reads one
// response. A frame is a 4-byte big-endian length followed by JSON. An image
// travels as a read-only descriptor in the request's control message, never
// as a path, so the Worker needs no access to the data volume.
package aiworker

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

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// MaxFrame bounds a request or response body.
const MaxFrame = 1 << 20

// Operations the Worker serves.
const (
	OpInfo       = "info"
	OpEmbedImage = "embed_image"
	// OpEmbedQuery encodes Text as a search query; the Worker adds the
	// model's own query prompt.
	OpEmbedQuery = "embed_query"
)

// Error codes the Worker answers with.
const (
	// CodeInvalidInput: the Worker cannot read this input; the same input
	// would fail again.
	CodeInvalidInput = "invalid_input"
	// CodeUnavailable: the Worker runs but cannot serve, for example because
	// its model file is missing or does not match the manifest.
	CodeUnavailable = "unavailable"
)

type Request struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
}

type Response struct {
	OK         bool   `json:"ok"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
	// Vector holds little-endian float32 values (base64 in JSON).
	Vector []byte     `json:"vector,omitempty"`
	Error  *WireError `json:"error,omitempty"`
}

type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Client implements photos.Embedder.
type Client struct {
	SocketPath string
	// Timeout bounds one call, including loading the model after the Worker
	// has exited while idle; it defaults to two minutes.
	Timeout time.Duration
}

var _ photos.Embedder = Client{}

func (c Client) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	response, err := c.call(ctx, Request{Op: OpInfo}, nil)
	if err != nil {
		return photos.EmbedderInfo{}, err
	}
	if response.Model == "" || response.Dimensions <= 0 {
		return photos.EmbedderInfo{}, errors.New("AI worker described no model")
	}
	return photos.EmbedderInfo{Model: response.Model, Dimensions: response.Dimensions}, nil
}

func (c Client) EmbedImage(ctx context.Context, image *os.File) ([]float32, error) {
	response, err := c.call(ctx, Request{Op: OpEmbedImage}, image)
	if err != nil {
		return nil, err
	}
	return vectorOf(response)
}

func (c Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	response, err := c.call(ctx, Request{Op: OpEmbedQuery, Text: text}, nil)
	if err != nil {
		return nil, err
	}
	return vectorOf(response)
}

func vectorOf(response Response) ([]float32, error) {
	if len(response.Vector)%4 != 0 {
		return nil, errors.New("AI worker returned a malformed vector")
	}
	vector := make([]float32, len(response.Vector)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(response.Vector[4*i:]))
	}
	return vector, nil
}

// call sends one request. Only failing to reach the Worker is
// photos.ErrAIUnavailable, which leaves the work waiting for free; a Worker
// that fails after receiving an input spends the job's attempt, so an input
// that keeps killing it cannot be retried forever.
func (c Client) call(ctx context.Context, request Request, file *os.File) (Response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", photos.ErrAIUnavailable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	payload, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	frame = append(frame, payload...)
	var rights []byte
	if file != nil {
		rights = unix.UnixRights(int(file.Fd()))
	}
	_, _, err = conn.(*net.UnixConn).WriteMsgUnix(frame, rights, nil)
	runtime.KeepAlive(file)
	if err != nil {
		return Response{}, fmt.Errorf("send to AI worker: %w", err)
	}

	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return Response{}, fmt.Errorf("read from AI worker: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > MaxFrame {
		return Response{}, fmt.Errorf("AI worker response of %d bytes exceeds the limit", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return Response{}, fmt.Errorf("read from AI worker: %w", err)
	}
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return Response{}, fmt.Errorf("decode AI worker response: %w", err)
	}
	if !response.OK {
		if response.Error == nil {
			return Response{}, errors.New("AI worker failed without a reason")
		}
		switch response.Error.Code {
		case CodeInvalidInput:
			return Response{}, fmt.Errorf("%w: %s", photos.ErrAIRejected, response.Error.Message)
		case CodeUnavailable:
			return Response{}, fmt.Errorf("%w: %s", photos.ErrAIUnavailable, response.Error.Message)
		default:
			return Response{}, fmt.Errorf("AI worker failed: %s: %s", response.Error.Code, response.Error.Message)
		}
	}
	return response, nil
}
