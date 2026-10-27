package photos

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// aiRunner is the AI Job Coordinator (docs/architecture/photo-library.md): it
// runs the AI Worker's jobs a round at a time whenever the gate allows, and
// owns what its rounds share. Each job's catalog work, from claiming it to
// storing its result, is the library's; Service.RunAI and ProcessAIJob are
// the runner's entry points.
type aiRunner struct {
	lib *Service
	// progress is what the runner last observed, for AIStatus.
	progress aiProgress
	// wake tells the runner that a thumbnail, a switch or a new model added
	// work.
	wake chan struct{}
	// ahead is the detection rendered while the Worker detected the one
	// before.
	ahead detectionAhead

	mu sync.Mutex
	// model, detector, faces and animals are the Worker's models whose stale
	// results have already been queued again; empty until a round asks.
	model, detector, faces, animals string
	// detectorLoaded is whether the Worker may still hold the detector.
	detectorLoaded bool
	// scanned counts face and pet scans stored since people last clustered.
	scanned int
}

func newAIRunner(lib *Service) *aiRunner {
	return &aiRunner{lib: lib, wake: make(chan struct{}, 1)}
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

// detectionAhead holds the detection job claimed and rendered while the
// Worker detected the one before, for the next round. RunAI is its usual
// user, but ProcessAIJob is exported and rounds may run at once: each takes
// what is held when it starts and puts back at most one when it ends.
type detectionAhead struct {
	mu    sync.Mutex
	input *detectionInput
}

// take empties the slot and returns what it held, or nil.
func (a *detectionAhead) take() *detectionInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	input := a.input
	a.input = nil
	return input
}

// put keeps input for the next round and returns the input it displaces,
// whose jobs the caller hands back to the queue.
func (a *detectionAhead) put(input *detectionInput) *detectionInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	displaced := a.input
	a.input = input
	return displaced
}

// run is Service.RunAI.
func (r *aiRunner) run(ctx context.Context, embedder Embedder, gate Gate, idle time.Duration, logError func(error)) {
	defer func() {
		// A detection rendered ahead goes back to the queue.
		if err := r.lib.returnDetection(context.WithoutCancel(ctx), r.ahead.take()); err != nil && logError != nil {
			logError(err)
		}
	}()
	for {
		processed, err := r.round(ctx, embedder, gate)
		if err != nil && ctx.Err() == nil && logError != nil {
			logError(err)
		}
		if processed && err == nil {
			continue
		}
		wait := idle
		if state, reason, _ := r.progress.get(); state == AIPaused && pausedByPressure(reason) {
			wait = min(wait, pressureRecheck)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
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

// round is Service.ProcessAIJob.
func (r *aiRunner) round(ctx context.Context, embedder Embedder, gate Gate) (processed bool, err error) {
	detector, _ := embedder.(Detector)
	faceDetector, _ := embedder.(FaceDetector)
	animals, _ := embedder.(AnimalEmbedder)
	// The input the last detection rendered ahead is used only if a
	// detection comes next; otherwise its job goes back to the queue.
	next := r.ahead.take()
	defer func() { err = errors.Join(err, r.lib.returnDetection(context.WithoutCancel(ctx), next)) }()
	if open, reason := gate.Open(ctx); !open {
		r.progress.set(AIPaused, reason, "")
		r.releaseModels(ctx, detector, reason)
		return false, nil
	}
	// Any call starts the Worker through its socket and keeps it from
	// exiting when idle, so once its model is known ask only when there is
	// work for it. A new model comes with a release, whose install restarts
	// this service, or shows up in a search (noticeModel).
	if model := r.knownModel(); model != "" && next == nil {
		waiting, err := r.workWaiting(ctx, model)
		if err != nil || !waiting {
			if state, _, _ := r.progress.get(); err == nil && state != AIUnavailable {
				r.progress.set(AIIdle, "", model)
			}
			if err == nil {
				r.releaseModels(ctx, detector, "")
				// People cluster in the photo service, without the Worker.
				clustered, err := r.lib.clusterPeople(ctx)
				return clustered, err
			}
			return false, err
		}
	}
	info, err := embedder.Info(ctx)
	if errors.Is(err, ErrAIUnavailable) {
		r.progress.set(AIUnavailable, "", "")
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
	if err := r.requeueStale(ctx, info); err != nil {
		return false, err
	}
	// Label texts are few and short; they come before photos.
	if fetched, err := r.lib.embedLabelText(ctx, embedder, info); fetched || err != nil {
		if fetched {
			r.progress.set(AIWorking, "", info.Model)
		}
		return fetched, err
	}
	job, err := r.lib.claimJob(ctx, embeddingDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		if info.Detector == "" && info.Faces == "" && info.Animals == "" {
			r.progress.set(AIIdle, "", info.Model)
			return false, nil
		}
		// A long run of photos clusters as it goes, not only at its end.
		if r.scansSinceClustering() >= clusterEvery {
			return r.lib.clusterPeople(ctx)
		}
		input := next
		next = nil
		return r.detect(ctx, models{detector, faceDetector, animals}, gate, info, input)
	}
	if err != nil {
		return false, err
	}
	r.progress.set(AIWorking, "", info.Model)

	thumbnail, err := r.lib.root.Open(derivedPath(thumbnailDerivation, job.objectID))
	if errors.Is(err, fs.ErrNotExist) {
		// Reconciliation is rendering the thumbnail again.
		return false, r.lib.releaseJob(ctx, job)
	}
	if err != nil {
		return false, errors.Join(err, r.lib.releaseJob(ctx, job))
	}
	vector, err := embedder.EmbedImage(ctx, thumbnail)
	_ = thumbnail.Close()
	switch {
	case ctx.Err() != nil:
		// Stopping: the job keeps its place and its attempt.
		return false, r.lib.returnJob(context.WithoutCancel(ctx), job)
	case errors.Is(err, ErrAIUnavailable):
		r.progress.set(AIUnavailable, "", "")
		return false, r.lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, r.lib.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return true, r.lib.failJob(ctx, job, jobErrorIO)
	case !validVector(vector, info.Dimensions):
		return true, r.lib.failJob(ctx, job, jobErrorInvalidResult)
	}
	return true, r.lib.completeEmbedding(ctx, job, info.Model, vector)
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

// detect runs one object's ready detection job, face scan and pet
// scan, or those of them that are due, on the enlarged input, the given one
// or else the next due. While the Worker works, the next input is rendered,
// so the Worker's threads need not wait for it
// (docs/architecture/photo-objects.md#资源策略). When gate closes meanwhile,
// the work is cancelled and every job goes back to its place in the queue
// without spending an attempt.
func (r *aiRunner) detect(ctx context.Context, m models, gate Gate, info EmbedderInfo, input *detectionInput) (bool, error) {
	detector, faceDetector, animals := m.detector, m.faceDetector, m.animals
	wanted := wantedScans{detecting: info.Detector != "", facing: info.Faces != "", petting: info.Animals != ""}
	if input == nil {
		var err error
		input, err = r.lib.claimDetection(ctx, wanted)
		if errors.Is(err, sql.ErrNoRows) {
			r.progress.set(AIIdle, "", info.Model)
			r.releaseModels(ctx, detector, "")
			clustered, err := r.lib.clusterPeople(ctx)
			return clustered, err
		}
		if err != nil {
			return false, err
		}
	}
	jobs := input.jobs()
	r.progress.set(AIWorking, "", info.Model)
	if ctx.Err() != nil {
		return false, r.lib.returnDetection(context.WithoutCancel(ctx), input)
	}
	if input.err != nil || input.class == jobWaiting {
		// The input waits for storage or the Media Worker; its jobs keep
		// their attempts.
		var err error
		for _, job := range jobs {
			err = errors.Join(err, r.lib.releaseJob(ctx, job))
		}
		return false, errors.Join(input.err, err)
	}
	if input.class != "" {
		var err error
		for _, job := range jobs {
			err = errors.Join(err, r.lib.failJob(ctx, job, input.class))
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
			if err := r.lib.releaseJob(ctx, jobs[i]); err != nil {
				return false, err
			}
		}
	}
	// The Worker loads its detector for the first detection and keeps it
	// until released.
	r.markDetectorLoaded()
	if objectsJob == nil && facesJob == nil && petsJob == nil {
		_ = input.file.Close()
		return false, nil
	}
	detectCtx, cancel := context.WithCancel(ctx)
	stopWatching := watchGate(detectCtx, gate, cancel)
	ahead := make(chan *detectionInput, 1)
	go func() {
		// A fault shows again at the next claim.
		next, _ := r.lib.claimDetection(ctx, wanted)
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
			pets = r.lib.petsIn(model, boxes)
		default:
			pets, petErr = r.lib.storedPets(ctx, petsJob.objectID)
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
	defer func() { _ = input.file.Close() }()
	carryOn := func(err error) (bool, error) {
		// Another round that ran meanwhile may have left one ahead too.
		displaced := r.ahead.put(next)
		return true, errors.Join(err, r.lib.returnDetection(context.WithoutCancel(ctx), displaced))
	}
	stop := func(err error) (bool, error) {
		return false, errors.Join(err, r.lib.returnDetection(context.WithoutCancel(ctx), next))
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
		return stop(every(func(job claimedJob) error { return r.lib.returnJob(context.WithoutCancel(ctx), job) }))
	case paused != "":
		r.progress.set(AIPaused, paused, "")
		err := every(func(job claimedJob) error { return r.lib.returnJob(ctx, job) })
		r.releaseModels(ctx, detector, paused)
		return stop(err)
	case errors.Is(detectErr, ErrAIUnavailable) || errors.Is(faceErr, ErrAIUnavailable) || errors.Is(petErr, ErrAIUnavailable) ||
		(objectsJob != nil && detectErr == nil && model != info.Detector) ||
		(facesJob != nil && faceErr == nil && faceModel != info.Faces) ||
		(petsJob != nil && petErr == nil && petModel != info.Animals):
		// Look again next round: the Worker may have lost or changed a model.
		r.forgetModels()
		return stop(every(func(job claimedJob) error { return r.lib.releaseJob(ctx, job) }))
	}
	var err error
	if objectsJob != nil {
		err = errors.Join(err, r.lib.finishDetection(ctx, *objectsJob, model, boxes, detectErr))
	}
	if facesJob != nil {
		// Faces are told from animals' by this round's detection, or else the
		// stored one.
		seen, seenErr := boxes, error(nil)
		if objectsJob == nil || detectErr != nil {
			_, seen, seenErr = r.lib.storedDetection(ctx, facesJob.objectID)
		}
		if seenErr != nil {
			// The scan waits for the catalog rather than keep an animal's face.
			err = errors.Join(err, seenErr, r.lib.releaseJob(ctx, *facesJob))
		} else {
			err = errors.Join(err, r.lib.finishFaces(ctx, *facesJob, faceModel, withoutAnimalFaces(faces, seen), faceErr, input.file))
		}
	}
	if petsJob != nil {
		err = errors.Join(err, r.lib.finishPets(ctx, *petsJob, petModel, pets, petVectors, petErr, input.file))
	}
	return carryOn(err)
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
func (r *aiRunner) releaseModels(ctx context.Context, detector Detector, reason string) {
	if detector == nil || (reason != "" && reason != "foreground" && !strings.HasPrefix(reason, "memory")) {
		return
	}
	r.mu.Lock()
	loaded := r.detectorLoaded
	r.detectorLoaded = false
	r.mu.Unlock()
	if loaded {
		// A Worker that has gone has released everything already.
		_ = detector.Release(ctx, strings.HasPrefix(reason, "memory"))
	}
}

// markDetectorLoaded records that the Worker loaded its detector, which it
// keeps until released: for the first detection of a round, or for an image
// search's.
func (r *aiRunner) markDetectorLoaded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detectorLoaded = true
}

// requeueStale queues every vector and detection of another model again the
// first time a model is seen. The old results stay until their replacements
// exist.
func (r *aiRunner) requeueStale(ctx context.Context, info EmbedderInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lib := r.lib
	now := formatTime(lib.now())
	if r.model != info.Model {
		if _, err := lib.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, derivation, 'pending', ?, ? FROM embeddings WHERE derivation = ? AND model <> ?
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, embeddingDerivation, info.Model); err != nil {
			return err
		}
		r.model = info.Model
	}
	if info.Detector != "" && r.detector != info.Detector {
		if _, err := lib.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, derivation, 'pending', ?, ? FROM detections WHERE derivation = ? AND model <> ?
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, detectionDerivation, info.Detector); err != nil {
			return err
		}
	}
	r.detector = info.Detector
	if info.Faces != "" && r.faces != info.Faces {
		if _, err := lib.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inPeopleLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, facesDerivation, info.Faces); err != nil {
			return err
		}
	}
	r.faces = info.Faces
	if info.Animals != "" && r.animals != info.Animals {
		if _, err := lib.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inPeopleLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, petsDerivation, info.Animals); err != nil {
			return err
		}
		if err := lib.enqueueMissingPets(ctx, lib.objectCalibration.Model); err != nil {
			return err
		}
	}
	r.animals = info.Animals
	return nil
}

// forgetModels makes the next round ask the Worker which models it has.
func (r *aiRunner) forgetModels() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.model, r.detector, r.faces, r.animals = "", "", "", ""
}

// knownModel is the Worker's model the runner last worked for, or "" before
// the first round and after noticeModel saw another one.
func (r *aiRunner) knownModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.model
}

// workerModels are the detector, face and animal models the runner last
// found the Worker to have.
func (r *aiRunner) workerModels() (detector, faces, animals string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detector, r.faces, r.animals
}

// noticeModel lets the runner look again when the Worker reports a model
// other than the one it last worked for.
func (r *aiRunner) noticeModel(model string) {
	r.mu.Lock()
	changed := r.model != "" && r.model != model
	if changed {
		r.model = ""
	}
	r.mu.Unlock()
	if changed {
		r.wakeUp()
	}
}

// workWaiting reports whether the Worker has anything to do for model: a
// label text still without its vector, an embedding job that is due, or a
// detection job, face or pet scan that is due while the Worker has its
// models.
func (r *aiRunner) workWaiting(ctx context.Context, model string) (bool, error) {
	lib := r.lib
	if model == lib.labels.Model {
		lib.labelIndex.mu.Lock()
		pending := lib.labelIndex.vectors == nil && !lib.now().Before(lib.labelIndex.retryAt)
		lib.labelIndex.mu.Unlock()
		if pending {
			return true, nil
		}
	}
	detector, faces, animals := r.workerModels()
	detecting, facing, petting := detector != "", faces != "", animals != ""
	now := formatTime(lib.now())
	var due bool
	err := lib.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM jobs WHERE (derivation = ? OR (? AND derivation = ?) OR (? AND derivation = ?) OR (? AND derivation = ?))
  AND ((state = 'pending' AND not_before <= ?) OR (state = 'running' AND lease_until <= ?)))`,
		embeddingDerivation, detecting, detectionDerivation, facing, facesDerivation, petting, petsDerivation, now, now).Scan(&due)
	return due, err
}

// countScan records a face or pet scan stored since people last clustered.
func (r *aiRunner) countScan() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanned++
}

// scansSinceClustering is how many scans were stored since people last
// clustered.
func (r *aiRunner) scansSinceClustering() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scanned
}

// clustered starts the count of scans again, as people cluster.
func (r *aiRunner) clustered() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanned = 0
}

// wakeUp tells the runner there is work, without waiting for its next look.
func (r *aiRunner) wakeUp() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
