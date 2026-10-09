package photos

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"
	"os"
	"strings"
	"sync"
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
}

// Gate tells background AI work whether it may start another item now, and
// why not otherwise.
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
	// The counts cover the originals of the caller's visible photos.
	Ready   int `json:"ready"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// aiProgress is what the AI runner last observed.
type aiProgress struct {
	mu     sync.Mutex
	state  string
	reason string
	model  string
}

func (p *aiProgress) set(state, reason, model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.reason = state, reason
	if model != "" {
		p.model = model
	}
}

func (p *aiProgress) get() (state, reason, model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return AIUnavailable, "", p.model
	}
	return p.state, p.reason, p.model
}

// RunAI processes embedding jobs through embedder until ctx ends, whenever
// gate allows. An empty or closed queue is rechecked every idle interval or
// as soon as a thumbnail adds work.
func (s *Service) RunAI(ctx context.Context, embedder Embedder, gate Gate, idle time.Duration, logError func(error)) {
	for {
		processed, err := s.ProcessAIJob(ctx, embedder, gate)
		if err != nil && ctx.Err() == nil && logError != nil {
			logError(err)
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.aiWake:
		case <-time.After(idle):
		}
	}
}

// ProcessAIJob runs one ready embedding job and reports whether it finished
// one. It does nothing while gate is closed or the Worker is away; the error
// returned is only for Catalog or storage faults.
func (s *Service) ProcessAIJob(ctx context.Context, embedder Embedder, gate Gate) (bool, error) {
	if open, reason := gate.Open(ctx); !open {
		s.ai.set(AIPaused, reason, "")
		return false, nil
	}
	info, err := embedder.Info(ctx)
	if errors.Is(err, ErrAIUnavailable) {
		s.ai.set(AIUnavailable, "", "")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.requeueStale(ctx, info.Model); err != nil {
		return false, err
	}
	job, err := s.claimJob(ctx, embeddingDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		s.ai.set(AIIdle, "", info.Model)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.ai.set(AIWorking, "", info.Model)

	thumbnail, err := s.root.Open(derivedPath(thumbnailDerivation, job.objectID))
	if errors.Is(err, fs.ErrNotExist) {
		// Reconciliation is rendering the thumbnail again.
		return false, s.releaseJob(ctx, job)
	}
	if err != nil {
		return false, errors.Join(err, s.releaseJob(ctx, job))
	}
	vector, err := embedder.EmbedImage(ctx, thumbnail)
	_ = thumbnail.Close()
	switch {
	case errors.Is(err, ErrAIUnavailable):
		s.ai.set(AIUnavailable, "", "")
		return false, s.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return true, s.failJob(ctx, job, jobErrorIO)
	case !validVector(vector, info.Dimensions):
		return true, s.failJob(ctx, job, jobErrorInvalidResult)
	}
	return true, s.completeEmbedding(ctx, job, info.Model, vector)
}

// requeueStale queues every vector of another model again the first time a
// model is seen. The old vectors stay until their replacements exist.
func (s *Service) requeueStale(ctx context.Context, model string) error {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	if s.aiModel == model {
		return nil
	}
	now := formatTime(s.now())
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, derivation, 'pending', ?, ? FROM embeddings WHERE derivation = ? AND model <> ?
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, embeddingDerivation, model); err != nil {
		return err
	}
	s.aiModel = model
	return nil
}

// releaseJob gives a claimed job back without spending its attempt, for work
// that could not start through no fault of its input.
func (s *Service) releaseJob(ctx context.Context, job claimedJob) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET state = 'pending', attempts = attempts - 1, lease_until = NULL, not_before = ? WHERE object_id = ? AND derivation = ?",
		formatTime(s.now().Add(aiRetryDelay)), job.objectID, job.derivation)
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
WHERE NOT EXISTS (SELECT 1 FROM embeddings WHERE object_id = ? AND derivation = ?)
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, embeddingDerivation, now, now, objectID, embeddingDerivation)
	return err
}

func (s *Service) wakeAI() {
	select {
	case s.aiWake <- struct{}{}:
	default:
	}
}

// AIStatus reports what local AI is doing and how far it has come with the
// photos p can see. Counts never include libraries p cannot view.
func (s *Service) AIStatus(ctx context.Context, p Principal) (AIStatus, error) {
	libraries, err := s.Libraries(ctx, p)
	if err != nil {
		return AIStatus{}, err
	}
	state, reason, model := s.ai.get()
	status := AIStatus{State: state, Reason: reason, Model: model}
	if len(libraries) == 0 {
		return status, nil
	}
	var args []any
	for _, lib := range libraries {
		args = append(args, lib.ID)
	}
	args = append(args, embeddingDerivation, embeddingDerivation, thumbnailDerivation)
	var total int
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COUNT(e.object_id),
       COUNT(CASE WHEN e.object_id IS NULL AND (j.state = 'failed' OR t.state = 'failed') THEN 1 END)
FROM (SELECT DISTINCT object_id FROM assets
      WHERE trashed_at IS NULL AND library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(libraries)), ",")+`)) a
LEFT JOIN embeddings e ON e.object_id = a.object_id AND e.derivation = ?
LEFT JOIN jobs j ON j.object_id = a.object_id AND j.derivation = ?
LEFT JOIN jobs t ON t.object_id = a.object_id AND t.derivation = ?`, args...).Scan(&total, &status.Ready, &status.Failed)
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
