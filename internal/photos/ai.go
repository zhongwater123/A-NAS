package photos

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
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
	defer func() {
		// A detection rendered ahead goes back to the queue.
		if err := s.returnDetection(context.WithoutCancel(ctx), s.nextDetection); err != nil && logError != nil {
			logError(err)
		}
		s.nextDetection = nil
	}()
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
func (s *Service) ProcessAIJob(ctx context.Context, embedder Embedder, gate Gate) (processed bool, err error) {
	detector, _ := embedder.(Detector)
	faceDetector, _ := embedder.(FaceDetector)
	animals, _ := embedder.(AnimalEmbedder)
	// The input the last detection rendered ahead is used only if a
	// detection comes next; otherwise its job goes back to the queue.
	next := s.nextDetection
	s.nextDetection = nil
	defer func() { err = errors.Join(err, s.returnDetection(context.WithoutCancel(ctx), next)) }()
	if open, reason := gate.Open(ctx); !open {
		s.ai.set(AIPaused, reason, "")
		s.releaseModels(ctx, detector, reason)
		return false, nil
	}
	// Any call starts the Worker through its socket and keeps it from
	// exiting when idle, so once its model is known ask only when there is
	// work for it. A new model comes with a release, whose install restarts
	// this service, or shows up in a search (noticeModel).
	if model := s.knownModel(); model != "" && next == nil {
		waiting, err := s.aiWorkWaiting(ctx, model)
		if err != nil || !waiting {
			if state, _, _ := s.ai.get(); err == nil && state != AIUnavailable {
				s.ai.set(AIIdle, "", model)
			}
			if err == nil {
				s.releaseModels(ctx, detector, "")
				// People cluster in the photo service, without the Worker.
				clustered, err := s.clusterPeople(ctx)
				return clustered, err
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
	if faceDetector == nil {
		info.Faces = ""
	}
	if animals == nil {
		info.Animals = ""
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
		if info.Detector == "" && info.Faces == "" && info.Animals == "" {
			s.ai.set(AIIdle, "", info.Model)
			return false, nil
		}
		// A long run of photos clusters as it goes, not only at its end.
		if s.scansSinceClustering() >= clusterEvery {
			return s.clusterPeople(ctx)
		}
		input := next
		next = nil
		return s.processDetection(ctx, models{detector, faceDetector, animals}, gate, info, input)
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
	case ctx.Err() != nil:
		// Stopping: the job keeps its place and its attempt.
		return false, s.returnJob(context.WithoutCancel(ctx), job)
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

// models are the Worker's background models the runner may use.
type models struct {
	detector     Detector
	faceDetector FaceDetector
	animals      AnimalEmbedder
}

// processDetection runs one object's ready detection job, face scan and pet
// scan, or those of them that are due, on the enlarged input, the given one
// or else the next due. While the Worker works, the next input is rendered,
// so the Worker's threads need not wait for it
// (docs/architecture/photo-objects.md#资源策略). When gate closes meanwhile,
// the work is cancelled and every job goes back to its place in the queue
// without spending an attempt.
func (s *Service) processDetection(ctx context.Context, m models, gate Gate, info EmbedderInfo, input *detectionInput) (bool, error) {
	detector, faceDetector, animals := m.detector, m.faceDetector, m.animals
	wanted := wantedScans{detecting: info.Detector != "", facing: info.Faces != "", petting: info.Animals != ""}
	if input == nil {
		var err error
		input, err = s.claimDetection(ctx, wanted)
		if errors.Is(err, sql.ErrNoRows) {
			s.ai.set(AIIdle, "", info.Model)
			s.releaseModels(ctx, detector, "")
			clustered, err := s.clusterPeople(ctx)
			return clustered, err
		}
		if err != nil {
			return false, err
		}
	}
	jobs := input.jobs()
	s.ai.set(AIWorking, "", info.Model)
	if ctx.Err() != nil {
		return false, s.returnDetection(context.WithoutCancel(ctx), input)
	}
	if input.err != nil || input.class == jobWaiting {
		// The input waits for storage or the Media Worker; its jobs keep
		// their attempts.
		var err error
		for _, job := range jobs {
			err = errors.Join(err, s.releaseJob(ctx, job))
		}
		return false, errors.Join(input.err, err)
	}
	if input.class != "" {
		var err error
		for _, job := range jobs {
			err = errors.Join(err, s.failJob(ctx, job, input.class))
		}
		return true, err
	}
	// A model the Worker lost since the input was claimed waits for it.
	var objectsJob, facesJob, petsJob *claimedJob
	for i := range jobs {
		switch {
		case jobs[i].derivation == detectionDerivation && wanted.detecting && detector != nil:
			objectsJob = &jobs[i]
		case jobs[i].derivation == facesDerivation && wanted.facing && faceDetector != nil:
			facesJob = &jobs[i]
		case jobs[i].derivation == petsDerivation && wanted.petting && animals != nil:
			petsJob = &jobs[i]
		default:
			if err := s.releaseJob(ctx, jobs[i]); err != nil {
				return false, err
			}
		}
	}
	// The Worker loads its detector for the first detection and keeps it
	// until released.
	s.aiModelMu.Lock()
	s.detectorLoaded = true
	s.aiModelMu.Unlock()
	if objectsJob == nil && facesJob == nil && petsJob == nil {
		_ = input.file.Close()
		return false, nil
	}
	detectCtx, cancel := context.WithCancel(ctx)
	stopWatching := watchGate(detectCtx, gate, cancel)
	ahead := make(chan *detectionInput, 1)
	go func() {
		// A fault shows again at the next claim.
		next, _ := s.claimDetection(ctx, wanted)
		ahead <- next
	}()
	var model, faceModel, petModel string
	var boxes, pets []Box
	var faces []Face
	var petVectors [][]float32
	var detectErr, faceErr, petErr error
	if objectsJob != nil {
		model, boxes, detectErr = detector.DetectObjects(detectCtx, input.file)
	}
	if facesJob != nil && detectCtx.Err() == nil {
		// The Worker read the shared input to its end.
		if _, err := input.file.Seek(0, io.SeekStart); err != nil {
			faceErr = err
		} else {
			faceModel, faces, faceErr = faceDetector.DetectFaces(detectCtx, input.file)
		}
	}
	if petsJob != nil && detectCtx.Err() == nil {
		// The pets come from this round's detection, or else the stored one.
		switch {
		case objectsJob != nil && detectErr != nil:
			petErr = detectErr
		case objectsJob != nil:
			pets = s.petsIn(model, boxes)
		default:
			pets, petErr = s.storedPets(ctx, petsJob.objectID)
		}
		petModel = info.Animals
		if petErr == nil && len(pets) > 0 {
			corners := make([][4]float32, len(pets))
			for i, pet := range pets {
				corners[i] = [4]float32{pet.X0, pet.Y0, pet.X1, pet.Y1}
			}
			if _, err := input.file.Seek(0, io.SeekStart); err != nil {
				petErr = err
			} else {
				petModel, petVectors, petErr = animals.EmbedAnimals(detectCtx, input.file, corners)
			}
		}
	}
	paused := stopWatching()
	cancel()
	next := <-ahead
	defer input.file.Close()
	carryOn := func(err error) (bool, error) {
		s.nextDetection = next
		return true, err
	}
	stop := func(err error) (bool, error) {
		return false, errors.Join(err, s.returnDetection(context.WithoutCancel(ctx), next))
	}
	claimed := []*claimedJob{objectsJob, facesJob, petsJob}
	every := func(do func(claimedJob) error) error {
		var err error
		for _, job := range claimed {
			if job != nil {
				err = errors.Join(err, do(*job))
			}
		}
		return err
	}
	switch {
	case ctx.Err() != nil:
		return stop(every(func(job claimedJob) error { return s.returnJob(context.WithoutCancel(ctx), job) }))
	case paused != "":
		s.ai.set(AIPaused, paused, "")
		err := every(func(job claimedJob) error { return s.returnJob(ctx, job) })
		s.releaseModels(ctx, detector, paused)
		return stop(err)
	case errors.Is(detectErr, ErrAIUnavailable) || errors.Is(faceErr, ErrAIUnavailable) || errors.Is(petErr, ErrAIUnavailable) ||
		(objectsJob != nil && detectErr == nil && model != info.Detector) ||
		(facesJob != nil && faceErr == nil && faceModel != info.Faces) ||
		(petsJob != nil && petErr == nil && petModel != info.Animals):
		// Look again next round: the Worker may have lost or changed a model.
		s.forgetDetector()
		return stop(every(func(job claimedJob) error { return s.releaseJob(ctx, job) }))
	}
	var err error
	if objectsJob != nil {
		err = errors.Join(err, s.finishDetection(ctx, *objectsJob, model, boxes, detectErr))
	}
	if facesJob != nil {
		// Faces are told from animals' by this round's detection, or else the
		// stored one.
		seen, seenErr := boxes, error(nil)
		if objectsJob == nil || detectErr != nil {
			_, seen, seenErr = s.storedDetection(ctx, facesJob.objectID)
		}
		if seenErr != nil {
			// The scan waits for the catalog rather than keep an animal's face.
			err = errors.Join(err, seenErr, s.releaseJob(ctx, *facesJob))
		} else {
			err = errors.Join(err, s.finishFaces(ctx, *facesJob, faceModel, withoutAnimalFaces(faces, seen), faceErr, input.file))
		}
	}
	if petsJob != nil {
		err = errors.Join(err, s.finishPets(ctx, *petsJob, petModel, pets, petVectors, petErr, input.file))
	}
	return carryOn(err)
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
	if info.Faces != "" && s.aiFaces != info.Faces {
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inPeopleLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, facesDerivation, info.Faces); err != nil {
			return err
		}
	}
	s.aiFaces = info.Faces
	if info.Animals != "" && s.aiAnimals != info.Animals {
		if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inPeopleLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, petsDerivation, info.Animals); err != nil {
			return err
		}
		if err := s.enqueueMissingPets(ctx, s.objectCalibration.Model); err != nil {
			return err
		}
	}
	s.aiAnimals = info.Animals
	return nil
}

// forgetDetector makes the next round ask the Worker which models it has.
func (s *Service) forgetDetector() {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	s.aiModel, s.aiDetector, s.aiFaces, s.aiAnimals = "", "", "", ""
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
// detection job, face or pet scan that is due while the Worker has its
// models.
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
	detecting, facing, petting := s.aiDetector != "", s.aiFaces != "", s.aiAnimals != ""
	s.aiModelMu.Unlock()
	now := formatTime(s.now())
	var due bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM jobs WHERE (derivation = ? OR (? AND derivation = ?) OR (? AND derivation = ?) OR (? AND derivation = ?))
  AND ((state = 'pending' AND not_before <= ?) OR (state = 'running' AND lease_until <= ?)))`,
		embeddingDerivation, detecting, detectionDerivation, facing, facesDerivation, petting, petsDerivation, now, now).Scan(&due)
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
WHERE NOT EXISTS (SELECT 1 FROM embeddings WHERE object_id = ? AND derivation = ?) AND `+notVideo("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, embeddingDerivation, now, now, objectID, embeddingDerivation, objectID)
	return err
}

func (s *Service) wakeAI() {
	select {
	case s.aiWake <- struct{}{}:
	default:
	}
}

// AIStatus reports what local AI is doing and how far it has come with the
// photos p can see. Counts never include libraries p cannot view, nor
// videos, which local AI leaves alone.
func (s *Service) AIStatus(ctx context.Context, p Principal) (AIStatus, error) {
	libraries, err := s.Libraries(ctx, p)
	if err != nil {
		return AIStatus{}, err
	}
	state, reason, model := s.ai.get()
	s.aiModelMu.Lock()
	detector, faces, animals := s.aiDetector, s.aiFaces, s.aiAnimals
	s.aiModelMu.Unlock()
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
