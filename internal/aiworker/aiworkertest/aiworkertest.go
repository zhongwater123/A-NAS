// Package aiworkertest runs fake AI Workers that speak the aiworker
// protocol, for tests of the photo service and its client.
package aiworkertest

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/aiworker"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// Handler answers one request; the descriptor is the image sent with it, if
// any. A nil response closes the connection without answering, as a Worker
// that dies would.
type Handler func(aiworker.Request, *os.File) *aiworker.Response

// Serve listens on a fresh Unix socket until the test ends and returns its
// path.
func Serve(t testing.TB, handle Handler) string {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "ai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ai.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveOne(conn.(*net.UnixConn), handle)
		}
	}()
	return socket
}

func serveOne(conn *net.UnixConn, handle Handler) {
	defer conn.Close()
	var request aiworker.Request
	file, err := unixmsg.Read(conn, &request, aiworker.MaxFrame)
	if err != nil {
		return
	}
	defer unixmsg.CloseFile(file)
	response := handle(request, file)
	if response == nil {
		return
	}
	_ = unixmsg.Write(conn, response, nil, aiworker.MaxFrame)
}

// Deterministic is a Worker whose vectors come from the image bytes or the
// query text, so equal inputs get equal vectors.
func Deterministic(model string, dimensions int) Handler {
	return func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			return &aiworker.Response{OK: true, Model: model, Dimensions: dimensions}
		case aiworker.OpEmbedImage:
			if file == nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			content, err := io.ReadAll(file)
			if err != nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: err.Error()}}
			}
			return &aiworker.Response{OK: true, Model: model, Dimensions: dimensions, Vector: Encode(hashVector(content, dimensions))}
		case aiworker.OpEmbedQuery:
			if request.Text == "" {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no text"}}
			}
			return &aiworker.Response{OK: true, Model: model, Dimensions: dimensions, Vector: Encode(hashVector([]byte(request.Text), dimensions))}
		}
		return &aiworker.Response{Error: &aiworker.WireError{Code: "unknown_op", Message: request.Op}}
	}
}

// WithDetector adds an object detector named model to a Worker: info names
// it, detect_objects answers boxes that come from the image bytes, and release
// succeeds.
func WithDetector(handle Handler, model string) Handler {
	return func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			response := handle(request, file)
			if response != nil && response.OK {
				response.Detector = model
			}
			return response
		case aiworker.OpDetectObjects:
			if file == nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			content, err := io.ReadAll(file)
			if err != nil || len(content) == 0 {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			return &aiworker.Response{OK: true, Model: model, Boxes: HashBoxes(content)}
		case aiworker.OpRelease:
			return &aiworker.Response{OK: true}
		}
		return handle(request, file)
	}
}

// WithFaces adds face models named model to a Worker: info names them, and
// detect_faces answers HashFaces of the image bytes.
func WithFaces(handle Handler, model string) Handler {
	return func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			response := handle(request, file)
			if response != nil && response.OK {
				response.FaceModel = model
			}
			return response
		case aiworker.OpDetectFaces:
			if file == nil {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			content, err := io.ReadAll(file)
			if err != nil || len(content) == 0 {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			return &aiworker.Response{OK: true, Model: model, Faces: HashFaces(content)}
		case aiworker.OpRelease:
			return &aiworker.Response{OK: true}
		}
		return handle(request, file)
	}
}

// WithAnimals adds an animal model named model to a Worker: info names it, and
// embed_animals answers HashAnimal of the image bytes and each box.
func WithAnimals(handle Handler, model string) Handler {
	return func(request aiworker.Request, file *os.File) *aiworker.Response {
		switch request.Op {
		case aiworker.OpInfo:
			response := handle(request, file)
			if response != nil && response.OK {
				response.AnimalModel = model
			}
			return response
		case aiworker.OpEmbedAnimals:
			if file == nil || len(request.Boxes) == 0 {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image or boxes"}}
			}
			content, err := io.ReadAll(file)
			if err != nil || len(content) == 0 {
				return &aiworker.Response{Error: &aiworker.WireError{Code: aiworker.CodeInvalidInput, Message: "no image"}}
			}
			vectors := make([][]byte, len(request.Boxes))
			for i, box := range request.Boxes {
				vectors[i] = Encode(HashAnimal(content, box))
			}
			return &aiworker.Response{OK: true, Model: model, Vectors: vectors}
		case aiworker.OpRelease:
			return &aiworker.Response{OK: true}
		}
		return handle(request, file)
	}
}

// AnimalDimensions is the length of HashAnimal's vectors.
const AnimalDimensions = 8

// HashAnimal is the vector of the animal at box: one of two pets chosen by
// the image bytes and the box, nearly equal in every photo.
func HashAnimal(content []byte, box [4]float64) []float32 {
	seed := append([]byte{}, content...)
	for _, value := range box {
		seed = binary.LittleEndian.AppendUint64(seed, math.Float64bits(value))
	}
	sum := sha256.Sum256(seed)
	return nearly(hashVector([]byte{'a', sum[0] % 2}, AnimalDimensions), hashVector(seed, AnimalDimensions))
}

// nearly is base, centred, with a little of noise, normalized.
func nearly(base, noise []float32) []float32 {
	vector := make([]float32, len(base))
	var norm float64
	for j := range vector {
		vector[j] = base[j] - 0.5 + 0.02*(noise[j]-0.5)
		norm += float64(vector[j]) * float64(vector[j])
	}
	for j := range vector {
		vector[j] /= float32(math.Sqrt(norm))
	}
	return vector
}

// FaceDimensions is the length of HashFaces' vectors.
const FaceDimensions = 8

// HashFaces are up to two faces that come from the image bytes, as the
// Python Worker's fake answers: each belongs to one of three people, whose
// vectors are nearly equal in every photo.
func HashFaces(content []byte) []aiworker.WireFace {
	sum := sha256.Sum256(content)
	faces := []aiworker.WireFace{}
	for i := range int(sum[0] % 3) {
		person := hashVector([]byte{'p', sum[1+i] % 3}, FaceDimensions)
		noise := hashVector(append(append([]byte{}, content...), byte(i)), FaceDimensions)
		vector := nearly(person, noise)
		x0 := 0.05 + 0.3*float64(i)
		faces = append(faces, aiworker.WireFace{
			Box:       []float64{x0, 0.2, x0 + 0.2, 0.5},
			Landmarks: []float64{x0 + 0.06, 0.3, x0 + 0.14, 0.3, x0 + 0.1, 0.36, x0 + 0.07, 0.42, x0 + 0.13, 0.42},
			Score:     0.9 - 0.01*float64(i),
			Vector:    Encode(vector),
		})
	}
	return faces
}

// HashBoxes are up to three boxes that come from the image bytes, best first.
func HashBoxes(content []byte) [][]float64 {
	sum := sha256.Sum256(content)
	boxes := [][]float64{}
	for i := range int(sum[0] % 4) {
		part := sum[1+6*i : 7+6*i]
		x0, y0 := float64(part[2])/510, float64(part[3])/510
		boxes = append(boxes, []float64{float64(1 + int(part[0])%365), 0.3 + float64(part[1])/255*0.7, x0, y0, x0 + 0.5, y0 + 0.5})
	}
	for i := 1; i < len(boxes); i++ {
		for j := i; j > 0 && boxes[j][1] > boxes[j-1][1]; j-- {
			boxes[j], boxes[j-1] = boxes[j-1], boxes[j]
		}
	}
	return boxes
}

func hashVector(content []byte, dimensions int) []float32 {
	sum := sha256.Sum256(content)
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = float32(sum[i%len(sum)]) / 255
	}
	return vector
}

// Encode returns a vector as the protocol carries it.
func Encode(vector []float32) []byte {
	encoded := make([]byte, 0, 4*len(vector))
	for _, value := range vector {
		encoded = binary.LittleEndian.AppendUint32(encoded, math.Float32bits(value))
	}
	return encoded
}
