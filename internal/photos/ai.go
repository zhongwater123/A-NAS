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
	// Detector names the object detector in the same way, or is empty when
	// the Worker has none it can use.
	Detector string
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
	// The counts cover the originals of the caller's visible photos. An
	// original is ready once it has its vector and, while the Worker has a
	// detector, its detection.
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

// RunAI processes embedding and detection jobs through embedder until ctx
// ends, whenever gate allows. An empty or closed queue is rechecked every idle
// interval or as soon as a thumbnail adds work; work stopped by processor or
// disk pressure looks again within seconds.
func (s *Service) RunAI(ctx context.Context, embedder Embedder, gate Gate, idle time.Duration, logError func(error)) {
	for {
		processed, err := s.ProcessAIJob(ctx, embedder, gate)
		if err != nil && ctx.Err() == nil && logError != nil {
			logError(err)
		}
		if processed && err == nil {
			continue
		}
		wait := idle
		if state, reason, _ := s.ai.get(); state == AIPaused && pausedByPressure(reason) {
			wait = min(wait, pressureRecheck)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.aiWake:
		case <-time.After(wait):
		}
	}
}

// pressureRecheck is how soon work stopped by processor or disk pressure
// looks again. The kernel averages pressure over 10 seconds and updates it
// every 2, and the gate waits for it to halve, so work resumes within seconds
// of the pressure passing, not a minute later
// (docs/investigations/2026-10-13-idle-detection-used-one-core.md).
var pressureRecheck = 5 * time.Second

// pausedByPressure reports whether work stopped because the device's other
// work waits on the processor or the disk, which passes within seconds. Use of
// the photo library and short memory keep it stopped longer, and a memory
// pause has unloaded the models, so looking again often would reload them.
func pausedByPressure(reason string) bool {
	return reason == "cpu_pressure" || reason == "io_pressure"
}

// ProcessAIJob fetches one missing label text vector or runs one ready
// embedding or detection job, and reports whether it did. Vectors come before
// detections, since search needs them. It does nothing while gate is closed or
// the Worker is away, and frees the Worker's memory when its work stops; the
// error returned is only for Catalog or storage faults.
func (s *Service) ProcessAIJob(ctx context.Context, embedder Embedder, gate Gate) (bool, error) {
	detector, _ := embedder.(Detector)
	if open, reason := gate.Open(ctx); !open {
		s.ai.set(AIPaused, reason, "")
		s.releaseModels(ctx, detector, reason)
		return false, nil
	}
	// Any call starts the Worker through its socket and keeps it from
	// exiting when idle, so once its model is known ask only when there is
	// work for it. A new model comes with a release, whose install restarts
	// this service, or shows up in a search (noticeModel).
	if model := s.knownModel(); model != "" {
		waiting, err := s.aiWorkWaiting(ctx, model)
		if err != nil || !waiting {
			if state, _, _ := s.ai.get(); err == nil && state != AIUnavailable {
				s.ai.set(AIIdle, "", model)
			}
			if err == nil {
				s.releaseModels(ctx, detector, "")
			}
			return false, err
		}
	}
	info, err := embedder.Info(ctx)
	if errors.Is(err, ErrAIUnavailable) {
		s.ai.set(AIUnavailable, "", "")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if detector == nil {
		info.Detector = ""
	}
	if err := s.requeueStale(ctx, info); err != nil {
		return false, err
	}
	// Label texts are few and short; they come before photos.
	if fetched, err := s.embedLabelText(ctx, embedder, info); fetched || err != nil {
		if fetched {
			s.ai.set(AIWorking, "", info.Model)
		}
		return fetched, err
	}
	job, err := s.claimJob(ctx, embeddingDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		if info.Detector == "" {
			s.ai.set(AIIdle, "", info.Model)
			return false, nil
		}
		return s.processDetection(ctx, detector, gate, info)
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

// gateWatchInterval is how often a running detection checks the gate, so
// that it stops soon after someone starts using the device.
const gateWatchInterval = 200 * time.Millisecond

// processDetection runs one ready detection job on the detector's enlarged
// input. When gate closes meanwhile, the detection is cancelled and the job
// given back without spending its attempt.
func (s *Service) processDetection(ctx context.Context, detector Detector, gate Gate, info EmbedderInfo) (bool, error) {
	job, err := s.claimJob(ctx, detectionDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		s.ai.set(AIIdle, "", info.Model)
		s.releaseModels(ctx, detector, "")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.ai.set(AIWorking, "", info.Model)
	input, class, err := s.openAIInput(job)
	if err != nil {
		return false, errors.Join(err, s.releaseJob(ctx, job))
	}
	if class != "" {
		return true, s.failJob(ctx, job, class)
	}
	// The Worker loads its detector for the first detection and keeps it
	// until released.
	s.aiModelMu.Lock()
	s.detectorLoaded = true
	s.aiModelMu.Unlock()
	detectCtx, cancel := context.WithCancel(ctx)
	stopWatching := watchGate(detectCtx, gate, cancel)
	model, boxes, err := detector.DetectObjects(detectCtx, input)
	paused := stopWatching()
	cancel()
	_ = input.Close()
	switch {
	case ctx.Err() != nil:
		return false, s.returnJob(context.WithoutCancel(ctx), job)
	case paused != "":
		s.ai.set(AIPaused, paused, "")
		err := s.returnJob(ctx, job)
		s.releaseModels(ctx, detector, paused)
		return false, err
	case errors.Is(err, ErrAIUnavailable):
		// Look again next round: the Worker may have lost its detector.
		s.forgetDetector()
		return false, s.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return true, s.failJob(ctx, job, jobErrorIO)
	case model != info.Detector:
		s.forgetDetector()
		return false, s.releaseJob(ctx, job)
	case !validBoxes(boxes):
		return true, s.failJob(ctx, job, jobErrorInvalidResult)
	}
	return true, s.completeDetection(ctx, job, model, boxes)
}

// watchGate cancels a running detection when gate closes. stop ends the
// watch and returns why gate closed, or "" when it stayed open.
func watchGate(ctx context.Context, gate Gate, cancel context.CancelFunc) (stop func() string) {
	done := make(chan struct{})
	var reason string
	var watcher sync.WaitGroup
	watcher.Go(func() {
		ticker := time.NewTicker(gateWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if open, why := gate.Open(ctx); !open {
					reason = why
					cancel()
					return
				}
			}
		}
	})
	return func() string {
		close(done)
		watcher.Wait()
		return reason
	}
}

// releaseModels frees the Worker's memory when background work stops
// (docs/architecture/photo-objects.md#资源策略): the detector once there is
// nothing to detect or someone uses the device, and the whole Worker under
// memory pressure. It only calls a Worker that detected since the last
// release, so it never starts one just to stop it.
//
// CPU or I/O pressure only cancels the running detection: that hands the
// processor back already, and unloading would cost a reload as soon as the
// pressure passes, which under flapping pressure is most of the work.
func (s *Service) releaseModels(ctx context.Context, detector Detector, reason string) {
	if detector == nil || (reason != "" && reason != "foreground" && !strings.HasPrefix(reason, "memory")) {
		return
	}
	s.aiModelMu.Lock()
	loaded := s.detectorLoaded
	s.detectorLoaded = false
	s.aiModelMu.Unlock()
	if loaded {
		// A Worker that has gone has released everything already.
		_ = detector.Release(ctx, strings.HasPrefix(reason, "memory"))
	}
}

// requeueStale queues every vector and detection of another model again the
// first time a model is seen. The old results stay until their replacements
// exist.
func (s *Service) requeueStale(ctx context.Context, info EmbedderInfo) error {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	now := formatTime(s.now())
	if s.aiModel != info.Model {
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, derivation, 'pending', ?, ? FROM embeddings WHERE derivation = ? AND model <> ?
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, embeddingDerivation, info.Model); err != nil {
			return err
		}
		s.aiModel = info.Model
	}
	if info.Detector != "" && s.aiDetector != info.Detector {
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, derivation, 'pending', ?, ? FROM detections WHERE derivation = ? AND model <> ?
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, detectionDerivation, info.Detector); err != nil {
			return err
		}
	}
	s.aiDetector = info.Detector
	return nil
}

// forgetDetector makes the next round ask the Worker which models it has.
func (s *Service) forgetDetector() {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	s.aiModel, s.aiDetector = "", ""
}

// knownModel is the Worker's model this service last worked for, or "" before
// the first round and after noticeModel saw another one.
func (s *Service) knownModel() string {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	return s.aiModel
}

// noticeModel lets the AI runner look again when the Worker reports a model
// other than the one it last worked for.
func (s *Service) noticeModel(model string) {
	s.aiModelMu.Lock()
	changed := s.aiModel != "" && s.aiModel != model
	if changed {
		s.aiModel = ""
	}
	s.aiModelMu.Unlock()
	if changed {
		s.wakeAI()
	}
}

// aiWorkWaiting reports whether the Worker has anything to do for model: a
// label text still without its vector, an embedding job that is due, or a
// detection job that is due while the Worker has a detector.
func (s *Service) aiWorkWaiting(ctx context.Context, model string) (bool, error) {
	if model == s.labels.Model {
		s.labelIndex.mu.Lock()
		pending := s.labelIndex.vectors == nil && !s.now().Before(s.labelIndex.retryAt)
		s.labelIndex.mu.Unlock()
		if pending {
			return true, nil
		}
	}
	s.aiModelMu.Lock()
	detecting := s.aiDetector != ""
	s.aiModelMu.Unlock()
	now := formatTime(s.now())
	var due bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM jobs WHERE (derivation = ? OR (? AND derivation = ?))
  AND ((state = 'pending' AND not_before <= ?) OR (state = 'running' AND lease_until <= ?)))`,
		embeddingDerivation, detecting, detectionDerivation, now, now).Scan(&due)
	return due, err
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
	s.aiModelMu.Lock()
	detector := s.aiDetector
	s.aiModelMu.Unlock()
	status := AIStatus{State: state, Reason: reason, Model: model, ObjectModel: detector}
	if status.Clusters, err = s.clusterState(ctx, model); err != nil {
		return AIStatus{}, err
	}
	if len(libraries) == 0 {
		return status, nil
	}
	detecting := detector != ""
	args := []any{detecting, detecting}
	for _, lib := range libraries {
		args = append(args, lib.ID)
	}
	args = append(args, embeddingDerivation, detectionDerivation, embeddingDerivation, thumbnailDerivation, detectionDerivation)
	var total int
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(CASE WHEN ready THEN 1 END), COUNT(CASE WHEN NOT ready AND failed THEN 1 END)
FROM (
  SELECT e.object_id IS NOT NULL AND (NOT ? OR d.object_id IS NOT NULL) AS ready,
         IFNULL(j.state = 'failed' OR t.state = 'failed' OR (? AND k.state = 'failed'), 0) AS failed
  FROM (SELECT DISTINCT object_id FROM assets
        WHERE trashed_at IS NULL AND library_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(libraries)), ",")+`)) a
  LEFT JOIN embeddings e ON e.object_id = a.object_id AND e.derivation = ?
  LEFT JOIN detections d ON d.object_id = a.object_id AND d.derivation = ?
  LEFT JOIN jobs j ON j.object_id = a.object_id AND j.derivation = ?
  LEFT JOIN jobs t ON t.object_id = a.object_id AND t.derivation = ?
  LEFT JOIN jobs k ON k.object_id = a.object_id AND k.derivation = ?
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
