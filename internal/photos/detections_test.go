package photos_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// detectingEmbedder is a fake AI Worker with an object detector. It records
// the size of every input and every release; wait, when set, runs inside a
// detection with its context.
type detectingEmbedder struct {
	*fakeEmbedder
	mu       sync.Mutex
	detector string
	boxes    []photos.Box
	inputs   []image.Config
	released []bool
	wait     func(ctx context.Context, detection int)
	// memory is what the Worker says its models take.
	memory uint64
}

func newDetectingEmbedder() *detectingEmbedder {
	return &detectingEmbedder{
		fakeEmbedder: newFakeEmbedder(),
		detector:     "detector-1",
		boxes:        []photos.Box{{Class: 140, Score: 0.9, X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.6}},
	}
}

func (d *detectingEmbedder) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	info, err := d.fakeEmbedder.Info(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		info.Detector, info.MemoryBytes = d.detector, d.memory
	}
	return info, err
}

func (d *detectingEmbedder) DetectObjects(ctx context.Context, input *os.File) (string, []photos.Box, error) {
	content, err := io.ReadAll(input)
	if err != nil {
		return "", nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return "", nil, photos.ErrAIRejected
	}
	d.mu.Lock()
	d.inputs = append(d.inputs, config)
	detection, wait, boxes, model := len(d.inputs), d.wait, d.boxes, d.detector
	d.mu.Unlock()
	if wait != nil {
		wait(ctx, detection)
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
	}
	return model, boxes, nil
}

func (d *detectingEmbedder) Release(_ context.Context, all bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.released = append(d.released, all)
	return nil
}

func (d *detectingEmbedder) releases() []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.released...)
}

// switchGate is a gate the test opens and closes while work runs. It
// records the memory needs it was asked about.
type switchGate struct {
	mu     sync.Mutex
	open   bool
	reason string
	needs  []uint64
}

func (g *switchGate) Open(_ context.Context, need uint64) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.needs = append(g.needs, need)
	return g.open, g.reason
}

func (g *switchGate) asked() []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]uint64(nil), g.needs...)
}

// roomGate opens for work whose models take at most room.
type roomGate struct {
	mu   sync.Mutex
	room uint64
}

func (g *roomGate) Open(_ context.Context, need uint64) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if need > g.room {
		return false, "memory_for_models"
	}
	return true, ""
}

func (g *switchGate) set(open bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open, g.reason = open, reason
}

func widePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		img.Set(x, height/2, color.RGBA{R: uint8(x), G: 0x40, B: 0x80, A: 0xff})
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func jobState(t *testing.T, root, derivation string) (state string, attempts int, class string) {
	t.Helper()
	err := catalogDB(t, root).QueryRow("SELECT state, attempts, error_class FROM jobs WHERE derivation = ?", derivation).
		Scan(&state, &attempts, &class)
	if err != nil {
		t.Fatalf("read %s job: %v", derivation, err)
	}
	return state, attempts, class
}

func TestDetectionFollowsTheVectorOnAnEnlargedUprightImage(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &fakeGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "wide.png", widePNG(t, 2400, 1200))
	importPhoto(t, service, alice, private.ID, "", "small.png", encodePNG(1))
	processAll(t, service)

	// Vectors first: after two rounds both photos have one, but no detection.
	for range 2 {
		if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	if status := aiStatus(t, service, alice); status.Ready != 0 || status.Pending != 2 || status.ObjectModel != "detector-1" {
		t.Fatalf("status with vectors only = %+v; a photo is ready once it is also detected", status)
	}
	if processed := processAI(t, service, worker, gate); processed != 2 {
		t.Fatalf("processed %d detections, want 2", processed)
	}
	if status := aiStatus(t, service, alice); status.Ready != 2 || status.Pending != 0 || status.Failed != 0 {
		t.Fatalf("status after detection = %+v", status)
	}
	sizes := map[image.Point]bool{}
	for _, input := range worker.inputs {
		sizes[image.Pt(input.Width, input.Height)] = true
	}
	if !sizes[image.Pt(1600, 800)] || !sizes[image.Pt(4, 3)] {
		t.Fatalf("detector inputs = %v; want the wide photo at 1600 px and the small one unscaled", worker.inputs)
	}
	var stored int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM detections WHERE model = 'detector-1' AND length(boxes) = 15").Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored detections = %d, %v; want two of one box each", stored, err)
	}
}

func TestDetectionNeedsAWorkerWithADetector(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)

	// A Worker that cannot detect leaves detection waiting and the photo
	// counts as ready with its vector.
	plain, gate := newFakeEmbedder(), &fakeGate{open: true}
	processAI(t, service, plain, gate)
	if state, _, _ := jobState(t, root, "objects/v1"); state != "pending" {
		t.Fatalf("detection job = %s, want pending", state)
	}
	if status := aiStatus(t, service, alice); status.Ready != 1 || status.ObjectModel != "" {
		t.Fatalf("status without a detector = %+v", status)
	}

	// So does one whose detector is unavailable, and it is left alone to
	// exit when idle.
	broken := newDetectingEmbedder()
	broken.detector = ""
	processAI(t, service, broken, gate)
	infos := broken.infos
	for range 3 {
		if ok, err := service.ProcessAIJob(context.Background(), broken, gate); ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	if len(broken.inputs) != 0 || broken.infos != infos {
		t.Fatalf("%d detections and %d more Info calls without a detector", len(broken.inputs), broken.infos-infos)
	}
}

func TestUsingTheDeviceStopsADetectionAndFreesTheDetector(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	processAI(t, service, worker, gate)
	if releases := worker.releases(); len(releases) != 1 || releases[0] {
		t.Fatalf("releases after the queue emptied = %v, want the detector once", releases)
	}

	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	worker.wait = func(ctx context.Context, detection int) {
		// Someone opens the photo library while the detector works.
		gate.set(false, "foreground")
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
	for {
		ok, err := service.ProcessAIJob(context.Background(), worker, gate)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	state, attempts, _ := jobState(t, root, "objects/v1")
	if state != "pending" || attempts != 0 {
		t.Fatalf("detection job = %s after %d attempts; a cancelled detection must not spend one", state, attempts)
	}
	if status := aiStatus(t, service, alice); status.State != photos.AIPaused || status.Reason != "foreground" {
		t.Fatalf("status = %+v, want paused for foreground use", status)
	}
	if releases := worker.releases(); len(releases) != 2 || releases[1] {
		t.Fatalf("releases = %v, want the detector freed again when use began", releases)
	}
}

func TestProcessorPressureCancelsButKeepsTheDetector(t *testing.T) {
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	for range 3 { // two vectors, one detection
		if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	worker.wait = func(ctx context.Context, detection int) {
		// Another service keeps the processor busy for a moment.
		gate.set(false, "cpu_pressure")
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() under processor pressure = %v, %v", ok, err)
	}
	if state, attempts, _ := jobState(t, root, "objects/v1"); state != "pending" || attempts != 0 {
		t.Fatalf("detection job = %s after %d attempts; want it back in the queue at no cost", state, attempts)
	}
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() while the pressure lasts = %v, %v", ok, err)
	}
	if releases := worker.releases(); len(releases) != 0 {
		t.Fatalf("releases = %v; processor pressure must not unload the detector", releases)
	}
	worker.wait = nil
	gate.set(true, "")
	c.Advance(10 * time.Minute)
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
		t.Fatalf("ProcessAIJob() once the pressure passed = %v, %v", ok, err)
	}
	if releases := worker.releases(); len(releases) != 0 {
		t.Fatalf("releases = %v; the detector stayed loaded for the next photo", releases)
	}
}

func TestWorkResumesWithinSecondsOfThePressurePassing(t *testing.T) {
	defer photos.SetPressureRecheck(20 * time.Millisecond)()
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	var once sync.Once
	worker.wait = func(ctx context.Context, detection int) {
		once.Do(func() {
			// Another service holds the processor for a moment.
			gate.set(false, "cpu_pressure")
			<-ctx.Done()
			time.AfterFunc(100*time.Millisecond, func() { gate.set(true, "") })
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// An hour between looks at an empty queue, as long as never.
		service.RunAI(ctx, worker, gate, time.Hour, func(err error) { t.Error(err) })
	}()
	defer func() { cancel(); <-stopped }()

	db := catalogDB(t, root)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var detected int
		if err := db.QueryRow("SELECT COUNT(*) FROM detections").Scan(&detected); err != nil {
			t.Fatal(err)
		}
		if detected == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 2 photos detected 10 s after a brief pressure; work must resume soon after it passes", detected)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheNextInputIsRenderedWhileTheWorkerDetects(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	for i, name := range []string{"a.png", "b.png", "c.png"} {
		importPhoto(t, service, alice, private.ID, "", name, encodePNG(uint8(i+1)))
	}
	processAll(t, service)
	db := catalogDB(t, root)
	claimed := func() int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE derivation = 'objects/v1' AND state = 'running'").Scan(&n); err != nil {
			t.Error(err)
		}
		return n
	}
	// waitForAhead waits until the job after the running one is claimed.
	waitForAhead := func() bool {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if claimed() == 2 {
				return true
			}
		}
		return false
	}
	var ahead bool
	worker.wait = func(ctx context.Context, detection int) {
		if detection == 1 {
			ahead = waitForAhead()
		}
	}
	for range 4 { // three vectors, the first detection
		if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	if !ahead {
		t.Fatal("the next photo was not claimed and rendered while the Worker detected")
	}

	// A new photo's vector comes first, and the detection rendered ahead
	// goes back to the queue.
	importPhoto(t, service, alice, private.ID, "", "new.png", encodePNG(9))
	processAll(t, service)
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
		t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
	}
	if n := claimed(); n != 0 {
		t.Fatalf("%d detections still claimed after a vector came first", n)
	}

	// A pause hands back both the running detection and the one ahead.
	worker.wait = func(ctx context.Context, detection int) {
		if detection == 2 && waitForAhead() {
			gate.set(false, "cpu_pressure")
			<-ctx.Done()
		}
	}
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() under pressure = %v, %v", ok, err)
	}
	var waiting int
	if err := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE derivation = 'objects/v1' AND state = 'pending' AND attempts = 0").Scan(&waiting); err != nil || waiting != 3 {
		t.Fatalf("%d detections back in the queue at no cost (%v), want 3", waiting, err)
	}
	worker.wait = nil
	gate.set(true, "")
	if processed := processAI(t, service, worker, gate); processed != 3 {
		t.Fatalf("processed %d detections after the pause, want 3", processed)
	}
	var detected int
	if err := db.QueryRow("SELECT COUNT(*) FROM detections").Scan(&detected); err != nil || detected != 4 {
		t.Fatalf("detections = %d, %v; want all 4 photos", detected, err)
	}
}

func TestRoundsAtOnceShareTheDetectionAhead(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	const photoCount = 8
	for i := range photoCount {
		importPhoto(t, service, alice, private.ID, "", string(rune('a'+i))+".png", encodePNG(uint8(i+1)))
	}
	processAll(t, service)

	// ProcessAIJob is exported, so rounds may run at once: each takes the
	// detection rendered ahead and leaves one behind.
	var rounds sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		rounds.Go(func() {
			for {
				processed, err := service.ProcessAIJob(context.Background(), worker, gate)
				if err != nil {
					failures <- err
					return
				}
				if !processed {
					return
				}
			}
		})
	}
	rounds.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("ProcessAIJob() at once: %v", err)
	}
	// A round that found every job claimed stopped early; the rest follows.
	processAI(t, service, worker, gate)

	db := catalogDB(t, root)
	var detected, claimed int
	if err := db.QueryRow("SELECT COUNT(*) FROM detections").Scan(&detected); err != nil || detected != photoCount {
		t.Fatalf("detections = %d, %v; want all %d photos", detected, err, photoCount)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE state = 'running'").Scan(&claimed); err != nil || claimed != 0 {
		t.Fatalf("%d jobs still claimed (%v); a detection rendered ahead was lost", claimed, err)
	}
}

func TestMemoryPressureEndsTheWholeWorker(t *testing.T) {
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	for range 3 { // two vectors, one detection
		if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	gate.set(false, "memory_pressure")
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() under pressure = %v, %v", ok, err)
	}
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() under pressure = %v, %v", ok, err)
	}
	if releases := worker.releases(); len(releases) != 1 || !releases[0] {
		t.Fatalf("releases = %v, want the whole Worker released once", releases)
	}
}

func TestWaitingForMemoryKeepsTheModelsAlreadyLoaded(t *testing.T) {
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &switchGate{open: true}
	worker.memory = 1536 << 20
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	for range 3 { // two vectors, one detection
		if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", ok, err)
		}
	}
	if needs := gate.asked(); !slices.Contains(needs, worker.memory) {
		t.Fatalf("the gate was asked about %v, never about the models' %d bytes", needs, worker.memory)
	}
	gate.set(false, "memory_for_models")
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() while waiting for memory = %v, %v", ok, err)
	}
	if releases := worker.releases(); len(releases) != 0 {
		t.Fatalf("releases = %v; waiting for memory must not unload the models already loaded", releases)
	}
	if status := aiStatus(t, service, alice); status.State != photos.AIPaused || status.Reason != "memory_for_models" {
		t.Fatalf("AI status = %+v, want paused for memory_for_models", status)
	}
}

func TestTheModelsWaitForRoomBeforeTheirFirstLoad(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker, gate := newDetectingEmbedder(), &roomGate{room: 1 << 30}
	worker.memory = 1536 << 20
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	// The first round learns what the models take only from the Worker's
	// answer, and asks the gate again before any model loads.
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); ok || err != nil {
		t.Fatalf("ProcessAIJob() without room for the models = %v, %v", ok, err)
	}
	if state, attempts, _ := jobState(t, root, "embedding/v1"); state != "pending" || attempts != 0 {
		t.Fatalf("vector job = %s after %d attempts; want it waiting at no cost", state, attempts)
	}
	gate.mu.Lock()
	gate.room = 4 << 30
	gate.mu.Unlock()
	if ok, err := service.ProcessAIJob(context.Background(), worker, gate); !ok || err != nil {
		t.Fatalf("ProcessAIJob() with room = %v, %v", ok, err)
	}
}

func TestDetectionsThatMakeNoSenseFail(t *testing.T) {
	for name, box := range map[string]photos.Box{
		"score above one": {Class: 1, Score: 1.5, X1: 1, Y1: 1},
		"unknown class":   {Class: 400, Score: 0.5, X1: 1, Y1: 1},
		"corners swapped": {Class: 1, Score: 0.5, X0: 0.8, X1: 0.2, Y1: 1},
		"corner outside":  {Class: 1, Score: 0.5, X1: 1.2, Y1: 1},
	} {
		t.Run(name, func(t *testing.T) {
			service, _, root := newService(t)
			private, _ := libraries(t, service, alice)
			worker := newDetectingEmbedder()
			worker.boxes = []photos.Box{box}
			importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
			processAll(t, service)
			processAI(t, service, worker, &fakeGate{open: true})
			if state, _, class := jobState(t, root, "objects/v1"); state != "failed" || class != "invalid_result" {
				t.Fatalf("detection job = %s (%s), want failed as an invalid result", state, class)
			}
			if status := aiStatus(t, service, alice); status.Failed != 1 {
				t.Fatalf("status = %+v, want the photo counted as failed", status)
			}
		})
	}
}

func TestPurgeDropsDetections(t *testing.T) {
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	worker := newDetectingEmbedder()
	asset := importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	processAI(t, service, worker, &fakeGate{open: true})
	if _, err := service.Trash(context.Background(), alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Purge(context.Background(), alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM detections").Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("detections after a purge = %d, %v; want 1", stored, err)
	}
}
