package photos

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"strings"
	"time"
)

// Local AI turns each original's upright thumbnail into a vector through the
// AI Worker (docs/architecture/photo-ai.md). The vectors are derived data:
// keyed by content object, rebuilt when the model changes and deleted with
// the object. Unlike thumbnails, this work yields to people using the device
// and waits whenever the Worker is away.

// Embedder is the photo service's view of the AI Worker.
type Embedder interface {
	Info(ctx context.Context) (EmbedderInfo, error)
	// EmbedImage returns the normalized vector of an upright image.
	EmbedImage(ctx context.Context, image *os.File) ([]float32, error)
	// EmbedQuery returns the normalized vector of a search query, comparable
	// with image vectors of the same model.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

type EmbedderInfo struct {
	// Model names the model, its weights and its preprocessing; a new value
	// makes earlier vectors stale.
	Model      string
	Dimensions int
	// Detector names the object detector in the same way, or is empty when
	// the Worker has none it can use.
	Detector string
	// Faces names the face models in the same way, or is empty when the
	// Worker has none it can use.
	Faces string
	// Animals names the animal model in the same way, or is empty when the
	// Worker has none it can use.
	Animals string
}

// Detector is the AI Worker's object detector
// (docs/architecture/photo-objects.md). An Embedder that is also a Detector
// gets detection jobs while its Info names a detector.
type Detector interface {
	// DetectObjects returns the detector's model and the objects in an
	// upright image. Cancelling ctx stops the detection in the Worker.
	DetectObjects(ctx context.Context, image *os.File) (string, []Box, error)
	// Release frees the detector's memory, or with all, the Worker's.
	Release(ctx context.Context, all bool) error
}

// FaceDetector is the AI Worker's face models
// (docs/architecture/photo-people.md). An Embedder that is also a
// FaceDetector gets face jobs while its Info names face models.
type FaceDetector interface {
	// DetectFaces returns the face models' name and the faces in an upright
	// image. Cancelling ctx stops the work in the Worker.
	DetectFaces(ctx context.Context, image *os.File) (string, []Face, error)
}

// Face is one face the Worker found: its box and five landmarks (eyes, nose
// tip, mouth corners) as fractions of the image, how sure the detector is,
// and the face's normalized identity vector.
type Face struct {
	Box       [4]float32
	Landmarks [10]float32
	Score     float32
	Vector    []float32
}

// Box is one object a detector found: its class, how sure the detector is,
// and its corners as fractions of the image.
type Box struct {
	Class          int
	Score          float32
	X0, Y0, X1, Y1 float32
}

// Gate tells background AI work whether it may start another item now, and
// why not otherwise: "foreground" while someone uses the photo library,
// "cpu_pressure", "io_pressure" or "memory_pressure" while the device's
// other work waits on that resource, or "memory_low".
type Gate interface {
	Open(ctx context.Context) (open bool, reason string)
}

var (
	// ErrAIUnavailable means the AI Worker cannot be reached: it is not
	// installed, not running or restarting. Work waits for it.
	ErrAIUnavailable = errors.New("the photo AI worker is not available")
	// ErrAIRejected means the AI Worker cannot process this input; the same
	// input would be rejected again.
	ErrAIRejected = errors.New("the photo AI worker rejected the input")
)

const (
	embeddingDerivation = "embedding/v1"
	// aiRetryDelay spaces out work the Worker could not take, without
	// spending the job's attempts.
	aiRetryDelay = time.Minute
	// jobErrorInvalidResult marks a vector of the wrong size or with values
	// that are not finite numbers.
	jobErrorInvalidResult = "invalid_result"
)

// AI processing states reported by AIStatus.
const (
	AIUnavailable = "unavailable"
	AIPaused      = "paused"
	AIWorking     = "working"
	AIIdle        = "idle"
)

type AIStatus struct {
	State string `json:"state"`
	// Reason says why work is paused, such as foreground activity.
	Reason string `json:"reason,omitempty"`
	Model  string `json:"model,omitempty"`
	// ObjectModel is the detector's model while the Worker has one.
	ObjectModel string `json:"objectModel,omitempty"`
	// FaceModel names the face models while the Worker has them.
	FaceModel string `json:"faceModel,omitempty"`
	// AnimalModel names the animal model while the Worker has it.
	AnimalModel string `json:"animalModel,omitempty"`
	// The counts cover the originals of the caller's visible photos. An
	// original is ready once it has its vector and, while the Worker has a
	// detector or face models, its detection and, while a library with people
	// on holds it, its face scan, and no pet scan waits while the Worker has
	// an animal model.
	Ready   int `json:"ready"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
	// Clusters says whether AI 聚合 can list its groups: ClustersReady, or
	// ClustersPreparing while label texts wait for the Worker. It is empty
	// when the Worker's model has no calibrated clusters.
	Clusters string `json:"clusters,omitempty"`
}

const (
	ClustersReady     = "ready"
	ClustersPreparing = "preparing"
)

// RunAI processes embedding and detection jobs through embedder until ctx
// ends, whenever gate allows. An empty or closed queue is rechecked every idle
// interval or as soon as a thumbnail adds work; work stopped by processor or
// disk pressure looks again within seconds.
func (s *Service) RunAI(ctx context.Context, embedder Embedder, gate Gate, idle time.Duration, logError func(error)) {
	s.ai.run(ctx, embedder, gate, idle, logError)
}

// ProcessAIJob fetches one missing label text vector or runs one ready
// embedding or detection job, and reports whether it did. Vectors come before
// detections, since search needs them. It does nothing while gate is closed or
// the Worker is away, and frees the Worker's memory when its work stops; the
// error returned is only for Catalog or storage faults.
func (s *Service) ProcessAIJob(ctx context.Context, embedder Embedder, gate Gate) (processed bool, err error) {
	return s.ai.round(ctx, embedder, gate)
}

// finishDetection stores a detection or records why it failed.
func (s *Service) finishDetection(ctx context.Context, job claimedJob, model string, boxes []Box, err error) error {
	switch {
	case errors.Is(err, ErrAIRejected):
		return s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return s.failJob(ctx, job, jobErrorIO)
	case !validBoxes(boxes):
		return s.failJob(ctx, job, jobErrorInvalidResult)
	}
	return s.completeDetection(ctx, job, model, boxes)
}

// finishFaces stores a face scan or records why it failed.
func (s *Service) finishFaces(ctx context.Context, job claimedJob, model string, faces []Face, err error, input *os.File) error {
	switch {
	case errors.Is(err, ErrAIRejected):
		return s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return s.failJob(ctx, job, jobErrorIO)
	case !validFaces(faces):
		return s.failJob(ctx, job, jobErrorInvalidResult)
	}
	return s.completeFaces(ctx, job, model, faces, input)
}

// releaseJob gives a claimed job back without spending its attempt, for work
// that could not start through no fault of its input.
func (s *Service) releaseJob(ctx context.Context, job claimedJob) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET state = 'pending', attempts = attempts - 1, lease_until = NULL, not_before = ? WHERE object_id = ? AND derivation = ?",
		formatTime(s.now().Add(aiRetryDelay)), job.objectID, job.derivation)
	return err
}

// returnJob puts a claimed job back in its place in the queue without
// spending its attempt, for work the gate or a shutdown stopped: it is the
// first to run once work resumes.
func (s *Service) returnJob(ctx context.Context, job claimedJob) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET state = 'pending', attempts = attempts - 1, lease_until = NULL WHERE object_id = ? AND derivation = ?",
		job.objectID, job.derivation)
	return err
}

func (s *Service) completeEmbedding(ctx context.Context, job claimedJob, model string, vector []float32) error {
	stored := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
			job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
			// Purged while the Worker was busy.
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO embeddings(object_id, derivation, model, vector, created_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET model = excluded.model, vector = excluded.vector, created_at = excluded.created_at`,
			job.objectID, job.derivation, model, encodeVector(vector), formatTime(s.now())); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		stored = err == nil
		return err
	})
	if err == nil && stored {
		s.index.put(job.objectID, model, vector)
	}
	return err
}

// enqueueEmbedding schedules the vector of an object whose thumbnail has
// just been stored.
func (s *Service) enqueueEmbedding(ctx context.Context, tx *sql.Tx, objectID string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT ?, ?, 'pending', ?, ?
WHERE NOT EXISTS (SELECT 1 FROM embeddings WHERE object_id = ? AND derivation = ?) AND `+notVideo("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, embeddingDerivation, now, now, objectID, embeddingDerivation, objectID)
	return err
}

// AIStatus reports what local AI is doing and how far it has come with the
// photos p can see. Counts never include libraries p cannot view, nor
// videos, which local AI leaves alone.
func (s *Service) AIStatus(ctx context.Context, p Principal) (AIStatus, error) {
	libraries, err := s.Libraries(ctx, p)
	if err != nil {
		return AIStatus{}, err
	}
	state, reason, model := s.ai.progress.get()
	detector, faces, animals := s.ai.workerModels()
	status := AIStatus{State: state, Reason: reason, Model: model, ObjectModel: detector, FaceModel: faces,
		AnimalModel: animals}
	if status.Clusters, err = s.clusterState(ctx, model); err != nil {
		return AIStatus{}, err
	}
	if len(libraries) == 0 {
		return status, nil
	}
	detecting, facing, petting := detector != "", faces != "", animals != ""
	args := []any{detecting, facing, petting, detecting, facing, petting}
	for _, lib := range libraries {
		args = append(args, lib.ID)
	}
	args = append(args, embeddingDerivation, detectionDerivation, facesDerivation, embeddingDerivation, thumbnailDerivation,
		detectionDerivation, facesDerivation, petsDerivation)
	var total int
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(CASE WHEN ready THEN 1 END), COUNT(CASE WHEN NOT ready AND failed THEN 1 END)
FROM (
  SELECT e.object_id IS NOT NULL AND (NOT ? OR d.object_id IS NOT NULL) AND (NOT ? OR f.object_id IS NOT NULL OR NOT a.people)
           AND (NOT ? OR p.object_id IS NULL) AS ready,
         IFNULL(j.state = 'failed' OR t.state = 'failed' OR (? AND k.state = 'failed') OR (? AND g.state = 'failed')
           OR (? AND p.state = 'failed'), 0) AS failed
  FROM (SELECT v.object_id, `+inPeopleLibrary("v.object_id")+` AS people FROM (SELECT DISTINCT object_id FROM assets
        WHERE trashed_at IS NULL AND library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(libraries)), ",")+`)
          AND `+notVideo("assets.object_id")+`) v) a
  LEFT JOIN embeddings e ON e.object_id = a.object_id AND e.derivation = ?
  LEFT JOIN detections d ON d.object_id = a.object_id AND d.derivation = ?
  LEFT JOIN scans f ON f.object_id = a.object_id AND f.derivation = ?
  LEFT JOIN jobs j ON j.object_id = a.object_id AND j.derivation = ?
  LEFT JOIN jobs t ON t.object_id = a.object_id AND t.derivation = ?
  LEFT JOIN jobs k ON k.object_id = a.object_id AND k.derivation = ?
  LEFT JOIN jobs g ON g.object_id = a.object_id AND g.derivation = ?
  LEFT JOIN jobs p ON p.object_id = a.object_id AND p.derivation = ?
)`, args...).Scan(&total, &status.Ready, &status.Failed)
	if err != nil {
		return AIStatus{}, err
	}
	status.Pending = total - status.Ready - status.Failed
	return status, nil
}

func validVector(vector []float32, dimensions int) bool {
	if dimensions <= 0 || len(vector) != dimensions {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

// encodeVector stores a vector as little-endian float32 values.
func encodeVector(vector []float32) []byte {
	encoded := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[4*i:], math.Float32bits(value))
	}
	return encoded
}

func decodeVector(encoded []byte) []float32 {
	vector := make([]float32, len(encoded)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*i:]))
	}
	return vector
}
