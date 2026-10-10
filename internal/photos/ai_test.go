package photos_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// fakeEmbedder is a deterministic AI Worker: the vector is derived from the
// image bytes it receives, so equal thumbnails get equal vectors.
type fakeEmbedder struct {
	mu          sync.Mutex
	model       string
	unavailable bool
	failure     error
	dimensions  int
	embedded    int
	// infos counts Info calls: each one would start the real Worker.
	infos int
}

func newFakeEmbedder() *fakeEmbedder { return &fakeEmbedder{model: "fake-1", dimensions: 4} }

func (f *fakeEmbedder) Info(context.Context) (photos.EmbedderInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infos++
	if f.unavailable {
		return photos.EmbedderInfo{}, photos.ErrAIUnavailable
	}
	return photos.EmbedderInfo{Model: f.model, Dimensions: 4}, nil
}

func (f *fakeEmbedder) EmbedImage(_ context.Context, image *os.File) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failure != nil {
		return nil, f.failure
	}
	content, err := io.ReadAll(image)
	if err != nil {
		return nil, err
	}
	f.embedded++
	sum := sha256.Sum256(content)
	vector := make([]float32, f.dimensions)
	for i := range vector {
		vector[i] = float32(sum[i]) / 255
	}
	return vector, nil
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return nil, photos.ErrAIUnavailable
	}
	sum := sha256.Sum256([]byte(text))
	vector := make([]float32, f.dimensions)
	for i := range vector {
		vector[i] = float32(sum[i]) / 255
	}
	return vector, nil
}

type fakeGate struct {
	open   bool
	reason string
}

func (g *fakeGate) Open(context.Context) (bool, string) { return g.open, g.reason }

func processAI(t *testing.T, service *photos.Service, embedder photos.Embedder, gate photos.Gate) int {
	t.Helper()
	var processed int
	for {
		ok, err := service.ProcessAIJob(context.Background(), embedder, gate)
		if err != nil {
			t.Fatalf("ProcessAIJob() error = %v", err)
		}
		if !ok {
			return processed
		}
		processed++
	}
}

func aiStatus(t *testing.T, service *photos.Service, p photos.Principal) photos.AIStatus {
	t.Helper()
	status, err := service.AIStatus(context.Background(), p)
	if err != nil {
		t.Fatalf("AIStatus() error = %v", err)
	}
	return status
}

func catalogDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestThumbnailsQueueVectorsThatTheWorkerFills(t *testing.T) {
	service, _, root := newService(t)
	private, shared := libraries(t, service, alice)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))

	// Without a thumbnail there is no AI input yet.
	if processed := processAI(t, service, embedder, gate); processed != 0 {
		t.Fatalf("processed %d AI jobs before thumbnails", processed)
	}
	processAll(t, service)
	if status := aiStatus(t, service, alice); status.Pending != 2 || status.Ready != 0 {
		t.Fatalf("status after thumbnails = %+v, want 2 pending", status)
	}
	if processed := processAI(t, service, embedder, gate); processed != 2 {
		t.Fatalf("processed %d AI jobs, want 2", processed)
	}
	status := aiStatus(t, service, alice)
	if status.Ready != 2 || status.Pending != 0 || status.State != photos.AIIdle || status.Model != "fake-1" {
		t.Fatalf("status = %+v, want 2 ready and idle", status)
	}

	// A copy reuses the original's vector.
	importPhoto(t, service, alice, shared.ID, "", "a copy.png", encodePNG(1))
	processAll(t, service)
	if processed := processAI(t, service, embedder, gate); processed != 0 || embedder.embedded != 2 {
		t.Fatalf("a duplicate original was embedded again (%d jobs, %d embeddings)", processed, embedder.embedded)
	}
	var vectors int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&vectors); err != nil || vectors != 2 {
		t.Fatalf("stored vectors = %d, %v; want 2", vectors, err)
	}
}

func TestAIWaitsForTheGateAndTheWorkerWithoutSpendingAttempts(t *testing.T) {
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	embedder := newFakeEmbedder()
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)

	closed := &fakeGate{reason: "foreground"}
	if processed := processAI(t, service, embedder, closed); processed != 0 {
		t.Fatalf("processed %d AI jobs while the gate was closed", processed)
	}
	if status := aiStatus(t, service, alice); status.State != photos.AIPaused || status.Reason != "foreground" {
		t.Fatalf("status while paused = %+v", status)
	}

	open := &fakeGate{open: true}
	embedder.unavailable = true
	if processed := processAI(t, service, embedder, open); processed != 0 {
		t.Fatalf("processed %d AI jobs without a Worker", processed)
	}
	if status := aiStatus(t, service, alice); status.State != photos.AIUnavailable || status.Pending != 1 {
		t.Fatalf("status without a Worker = %+v", status)
	}

	// The Worker goes away between describing itself and embedding: the
	// job waits without spending an attempt.
	embedder.unavailable = false
	embedder.failure = photos.ErrAIUnavailable
	for range 5 {
		processAI(t, service, embedder, open)
		c.Advance(2 * time.Minute)
	}
	var attempts int
	var state string
	if err := catalogDB(t, root).QueryRow("SELECT attempts, state FROM jobs WHERE derivation = 'embedding/v1'").Scan(&attempts, &state); err != nil || attempts != 0 || state != "pending" {
		t.Fatalf("job after an absent Worker = %d attempts, %q, %v; want 0 and pending", attempts, state, err)
	}

	embedder.failure = nil
	if processed := processAI(t, service, embedder, open); processed != 1 {
		t.Fatalf("processed %d AI jobs once the Worker returned, want 1", processed)
	}
}

func TestBadOrFailingInputsEndAsFailures(t *testing.T) {
	service, c, _ := newService(t)
	private, _ := libraries(t, service, alice)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "rejected.png", encodePNG(1))
	processAll(t, service)
	embedder.failure = photos.ErrAIRejected
	processAI(t, service, embedder, gate)
	if status := aiStatus(t, service, alice); status.Failed != 1 || status.Pending != 0 {
		t.Fatalf("status after a rejected input = %+v, want 1 failed", status)
	}

	// A Worker that dies on an input spends an attempt each time.
	importPhoto(t, service, alice, private.ID, "", "crashes.png", encodePNG(2))
	processAll(t, service)
	embedder.failure = errors.New("worker died")
	for range 5 {
		processAI(t, service, embedder, gate)
		c.Advance(10 * time.Minute)
	}
	if status := aiStatus(t, service, alice); status.Failed != 2 {
		t.Fatalf("status after a crashing input = %+v, want 2 failed", status)
	}

	// A vector of the wrong size is never stored.
	importPhoto(t, service, alice, private.ID, "", "short.png", encodePNG(3))
	processAll(t, service)
	embedder.failure, embedder.dimensions = nil, 3
	processAI(t, service, embedder, gate)
	if status := aiStatus(t, service, alice); status.Failed != 3 || status.Ready != 0 {
		t.Fatalf("status after a short vector = %+v, want 3 failed", status)
	}
}

func TestAnIdleRunnerLeavesTheWorkerAlone(t *testing.T) {
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	processAI(t, service, embedder, gate)

	// Socket activation starts the Worker for any call and keeps it from
	// exiting when idle, so rounds without work must not make one.
	asked := embedder.infos
	for range 3 {
		if processed := processAI(t, service, embedder, gate); processed != 0 {
			t.Fatalf("processed %d AI jobs without work", processed)
		}
	}
	if embedder.infos != asked {
		t.Fatalf("idle rounds asked the Worker %d more times", embedder.infos-asked)
	}
	if status := aiStatus(t, service, alice); status.State != photos.AIIdle || status.Model != "fake-1" {
		t.Fatalf("idle status = %+v", status)
	}

	// New work brings the Worker back.
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	if processed := processAI(t, service, embedder, gate); processed != 1 {
		t.Fatalf("processed %d AI jobs after a new photo, want 1", processed)
	}
}

func TestASearchThatMeetsANewModelRequeuesVectors(t *testing.T) {
	c := newClock()
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Now: c.Now, DisableCapacityReserve: true, AI: embedder})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	processAI(t, service, embedder, gate)

	embedder.model = "fake-2"
	if processed := processAI(t, service, embedder, gate); processed != 0 {
		t.Fatalf("an idle round asked the Worker and processed %d jobs", processed)
	}
	if _, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: "cat"}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if processed := processAI(t, service, embedder, gate); processed != 1 {
		t.Fatalf("processed %d AI jobs after a search met the new model, want 1", processed)
	}
}

func TestANewModelReplacesEveryVector(t *testing.T) {
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	processAI(t, service, embedder, gate)

	// A new model comes with a release, whose install restarts the service.
	embedder.model = "fake-2"
	_ = service.Close()
	service = openService(t, root, c.Now)
	if processed := processAI(t, service, embedder, gate); processed != 2 {
		t.Fatalf("processed %d AI jobs after a model change, want 2", processed)
	}
	var stale int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM embeddings WHERE model <> 'fake-2'").Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("vectors of the old model = %d, %v", stale, err)
	}
}

func TestAIStatusCountsOnlyVisibleLibrariesAndPurgeDropsVectors(t *testing.T) {
	service, _, root := newService(t)
	alicePrivate, _ := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	asset := importPhoto(t, service, alice, alicePrivate.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, alicePrivate.ID, "", "b.png", encodePNG(2))
	importPhoto(t, service, bob, bobPrivate.ID, "", "c.png", encodePNG(3))
	processAll(t, service)
	processAI(t, service, embedder, gate)

	if status := aiStatus(t, service, bob); status.Ready != 1 {
		t.Fatalf("bob's status = %+v; it must not count alice's photos", status)
	}
	if _, err := service.Trash(context.Background(), alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Purge(context.Background(), alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	var vectors int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&vectors); err != nil || vectors != 2 {
		t.Fatalf("vectors after a purge = %d, %v; want 2", vectors, err)
	}
	if status := aiStatus(t, service, alice); status.Ready != 1 {
		t.Fatalf("alice's status after a purge = %+v", status)
	}
}
