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
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
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
	// OpDetectObjects finds the objects in an image. Hanging up cancels it,
	// and the Worker does not answer.
	OpDetectObjects = "detect_objects"
	// OpDetectFaces finds the faces in an image and embeds each; hanging up
	// cancels it too.
	OpDetectFaces = "detect_faces"
	// OpEmbedAnimals embeds the animals at Boxes in an image, to tell one
	// pet from another; hanging up cancels it too.
	OpEmbedAnimals = "embed_animals"
	// OpRelease frees memory: What is "detector" to drop the background
	// models (the detector, the face models and the animal model), or "all"
	// for the Worker to exit after answering.
	OpRelease = "release"
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
	What string `json:"what,omitempty"`
	// Boxes are the animals to embed, [x0, y0, x1, y1] as fractions.
	Boxes [][4]float64 `json:"boxes,omitempty"`
}

type Response struct {
	OK         bool   `json:"ok"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
	// Detector is the object detector's model, in answer to info.
	Detector string `json:"detector,omitempty"`
	// Vector holds little-endian float32 values (base64 in JSON).
	Vector []byte `json:"vector,omitempty"`
	// Boxes are [class, score, x0, y0, x1, y1], corners as fractions.
	Boxes [][]float64 `json:"boxes,omitempty"`
	// FaceModel names the face models, in answer to info.
	FaceModel string `json:"faceModel,omitempty"`
	// Faces answer detect_faces.
	Faces []WireFace `json:"faces,omitempty"`
	// AnimalModel names the animal model, in answer to info.
	AnimalModel string `json:"animalModel,omitempty"`
	// Vectors answer embed_animals, one per box, each as Vector is.
	Vectors [][]byte   `json:"vectors,omitempty"`
	Error   *WireError `json:"error,omitempty"`
}

// WireFace is one face as detect_faces answers it: box and landmarks as
// fractions of the image, and the vector as Vector is.
type WireFace struct {
	Box       []float64 `json:"box"`
	Landmarks []float64 `json:"landmarks"`
	Score     float64   `json:"score"`
	Vector    []byte    `json:"vector"`
}

type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Client implements photos.Embedder, photos.Detector, photos.FaceDetector and
// photos.AnimalEmbedder.
type Client struct {
	SocketPath string
	// Timeout bounds one call, including loading the model after the Worker
	// has exited while idle; it defaults to two minutes.
	Timeout time.Duration
}

var (
	_ photos.Embedder       = Client{}
	_ photos.Detector       = Client{}
	_ photos.FaceDetector   = Client{}
	_ photos.AnimalEmbedder = Client{}
)

func (c Client) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	response, err := c.call(ctx, Request{Op: OpInfo}, nil)
	if err != nil {
		return photos.EmbedderInfo{}, err
	}
	if response.Model == "" || response.Dimensions <= 0 {
		return photos.EmbedderInfo{}, errors.New("AI worker described no model")
	}
	return photos.EmbedderInfo{Model: response.Model, Dimensions: response.Dimensions, Detector: response.Detector,
		Faces: response.FaceModel, Animals: response.AnimalModel}, nil
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

func (c Client) DetectObjects(ctx context.Context, image *os.File) (string, []photos.Box, error) {
	response, err := c.call(ctx, Request{Op: OpDetectObjects}, image)
	if err != nil {
		return "", nil, err
	}
	if response.Model == "" {
		return "", nil, errors.New("AI worker named no detector")
	}
	boxes := make([]photos.Box, 0, len(response.Boxes))
	for _, box := range response.Boxes {
		if len(box) != 6 || box[0] != math.Trunc(box[0]) {
			return "", nil, errors.New("AI worker returned a malformed box")
		}
		boxes = append(boxes, photos.Box{Class: int(box[0]), Score: float32(box[1]),
			X0: float32(box[2]), Y0: float32(box[3]), X1: float32(box[4]), Y1: float32(box[5])})
	}
	return response.Model, boxes, nil
}

func (c Client) DetectFaces(ctx context.Context, image *os.File) (string, []photos.Face, error) {
	response, err := c.call(ctx, Request{Op: OpDetectFaces}, image)
	if err != nil {
		return "", nil, err
	}
	if response.Model == "" {
		return "", nil, errors.New("AI worker named no face models")
	}
	faces := make([]photos.Face, 0, len(response.Faces))
	for _, wire := range response.Faces {
		if len(wire.Box) != 4 || len(wire.Landmarks) != 10 {
			return "", nil, errors.New("AI worker returned a malformed face")
		}
		vector, err := vectorOf(Response{Vector: wire.Vector})
		if err != nil {
			return "", nil, err
		}
		face := photos.Face{Score: float32(wire.Score), Vector: vector}
		for i, value := range wire.Box {
			face.Box[i] = float32(value)
		}
		for i, value := range wire.Landmarks {
			face.Landmarks[i] = float32(value)
		}
		faces = append(faces, face)
	}
	return response.Model, faces, nil
}

func (c Client) EmbedAnimals(ctx context.Context, image *os.File, boxes [][4]float32) (string, [][]float32, error) {
	request := Request{Op: OpEmbedAnimals, Boxes: make([][4]float64, len(boxes))}
	for i, box := range boxes {
		for j, value := range box {
			request.Boxes[i][j] = float64(value)
		}
	}
	response, err := c.call(ctx, request, image)
	if err != nil {
		return "", nil, err
	}
	if response.Model == "" {
		return "", nil, errors.New("AI worker named no animal model")
	}
	if len(response.Vectors) != len(boxes) {
		return "", nil, errors.New("AI worker returned a vector per box it was not sent")
	}
	vectors := make([][]float32, len(response.Vectors))
	for i, encoded := range response.Vectors {
		if vectors[i], err = vectorOf(Response{Vector: encoded}); err != nil {
			return "", nil, err
		}
	}
	return response.Model, vectors, nil
}

func (c Client) Release(ctx context.Context, all bool) error {
	what := "detector"
	if all {
		what = "all"
	}
	_, err := c.call(ctx, Request{Op: OpRelease, What: what}, nil)
	return err
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
// that keeps killing it cannot be retried forever. Cancelling ctx hangs up,
// which stops a detection; the error then wraps context.Canceled.
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

	unixConn := conn.(*net.UnixConn)
	err = unixmsg.Write(unixConn, request, file, MaxFrame)
	runtime.KeepAlive(file)
	if err != nil {
		return Response{}, fmt.Errorf("send to AI worker: %w", err)
	}
	var response Response
	received, err := unixmsg.Read(unixConn, &response, MaxFrame)
	unixmsg.CloseFile(received)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, fmt.Errorf("%w: %v", ctxErr, err)
		}
		return Response{}, fmt.Errorf("read from AI worker: %w", err)
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
