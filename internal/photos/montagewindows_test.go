package photos_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/vectorindex/vectorindextest"
)

// framedVideo reads every file as a 70-second video, or one of duration
// seconds, and hands over its frames: a patterned picture before 60
// seconds, black after. stopAfter,
// when set, hangs the frames up after that many, as someone starting to use
// the library would, by closing the gate. during, when set, runs before
// each frame with how many were asked for. extra adds a frame to each range
// of grey frames, as an FFmpeg rounding the last instant up would; failing,
// when set, is what every request for frames answers.
type framedVideo struct {
	mu        sync.Mutex
	requests  []ffmpegworker.FramesRequest
	duration  float64
	asked     int
	stopAfter int
	during    func(asked int)
	gate      *lockedGate
	extra     bool
	failing   error
}

// lockedGate is a gate the test and the runner's watch read at once.
type lockedGate struct {
	mu     sync.Mutex
	open   bool
	reason string
}

func (g *lockedGate) Open(context.Context, uint64) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open, g.reason
}

func (g *lockedGate) set(open bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open, g.reason = open, reason
}

func (v *framedVideo) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	duration := v.duration
	if duration == 0 {
		duration = 70
	}
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: duration},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}},
	}, nil
}
func (v *framedVideo) Image(context.Context, *os.File, int) ([]byte, error) {
	return nil, ffmpegworker.ErrUnreadable
}
func (v *framedVideo) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	return frameJPEG(true), nil
}
func (v *framedVideo) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, ffmpegworker.ErrUnreadable
}

func frameJPEG(patterned bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := range 36 {
		for x := range 64 {
			if patterned && (x/8+y/8)%2 == 0 {
				img.Set(x, y, color.RGBA{R: 240, G: 220, B: 200, A: 255})
			} else {
				img.Set(x, y, color.RGBA{A: 255})
			}
		}
	}
	var buffer bytes.Buffer
	_ = jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 95})
	return buffer.Bytes()
}

func (v *framedVideo) Frames(ctx context.Context, _ *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error) {
	v.mu.Lock()
	v.requests = append(v.requests, request)
	extra, failing := v.extra, v.failing
	v.mu.Unlock()
	if failing != nil {
		return ffmpegworker.FramesResult{}, failing
	}
	if request.Format == ffmpegworker.FormatGray {
		count := len(request.Times)
		if count == 0 {
			count = int(math.Round((request.End - request.Start) * request.Rate))
			if extra {
				count++
			}
		}
		for range count {
			if err := each(make([]byte, request.Width*request.Height)); err != nil {
				return ffmpegworker.FramesResult{}, err
			}
		}
		return ffmpegworker.FramesResult{Times: request.Times}, nil
	}
	var result ffmpegworker.FramesResult
	if request.Blur {
		for range request.Times {
			blur := 4.5
			result.Blur = append(result.Blur, &blur)
		}
	}
	for _, at := range request.Times {
		v.mu.Lock()
		v.asked++
		asked, during := v.asked, v.during
		stop := v.stopAfter > 0 && v.asked > v.stopAfter
		v.mu.Unlock()
		if during != nil {
			during(asked)
		}
		if stop {
			v.gate.set(false, "foreground")
			<-ctx.Done()
			return ffmpegworker.FramesResult{}, ctx.Err()
		}
		if err := each(frameJPEG(at < 60)); err != nil {
			return ffmpegworker.FramesResult{}, err
		}
	}
	result.Times = request.Times
	return result, nil
}

// fakeCompute stands in for the montage compute Worker: two cards per video,
// halves of its windows, and every card good. cut counts the times it was
// asked for cards; hook, when set, runs first in Cards ("cards") and Quality
// ("quality"), and its error is their answer.
type fakeCompute struct {
	mu         sync.Mutex
	cut        int
	qualities  int
	moves      []int
	empty      bool
	cardsErr   error
	qualityErr error
	hook       func(ctx context.Context, op string) error
}

// called counts the call and runs the hook of op.
func (c *fakeCompute) called(ctx context.Context, op string) error {
	c.mu.Lock()
	hook := c.hook
	if op == "cards" {
		c.cut++
	}
	c.mu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, op)
}

func (c *fakeCompute) Cards(ctx context.Context, windows []photos.CardWindow, _ []float32, _ int, _ []bool) ([]photos.ComputedCard, error) {
	if err := c.called(ctx, "cards"); err != nil {
		return nil, err
	}
	if c.empty || c.cardsErr != nil {
		return nil, c.cardsErr
	}
	half := len(windows) / 2
	card := func(from, to int) photos.ComputedCard {
		start, end := windows[from].Start, windows[to-1].End
		return photos.ComputedCard{Start: start, End: end, Times: []float64{start + 1, (start + end) / 2, end - 1}}
	}
	return []photos.ComputedCard{card(0, half), card(half, len(windows))}, nil
}
func (c *fakeCompute) ShakeMoves(_ context.Context, frames []byte, count int) ([][2]float64, error) {
	if len(frames) != count*320*180 {
		return nil, fmt.Errorf("%d bytes for %d shake frames", len(frames), count)
	}
	return make([][2]float64, count-1), nil
}
func (c *fakeCompute) Keyframe(context.Context, []byte) (float64, float64, error) {
	return 0.1, 120, nil
}
func (c *fakeCompute) SharpPoints(context.Context, []byte, int, int) (float64, error) {
	return 0, errors.New("light keyframes have no sharp points to count")
}
func (c *fakeCompute) Quality(ctx context.Context, measures photos.CardMeasures) (photos.CardQuality, error) {
	if err := c.called(ctx, "quality"); err != nil {
		return photos.CardQuality{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.qualityErr != nil {
		return photos.CardQuality{}, c.qualityErr
	}
	c.qualities++
	c.moves = append(c.moves, len(measures.Moves))
	return photos.CardQuality{Blur: measures.Blurs[0], Blocked: 0.1, Points: measures.Points, Reasons: []string{}}, nil
}

type montageFixture struct {
	service  *photos.Service
	clock    *clocktest.Clock
	root     string
	video    *framedVideo
	media    photos.MediaDecoder
	embedder *fakeEmbedder
	gate     *lockedGate
	compute  *fakeCompute
	// release is the product release the store opens as; a restart with
	// another one is an update.
	release string
}

func newMontageFixture(t *testing.T) montageFixture {
	t.Helper()
	gate := &lockedGate{open: true}
	f := montageFixture{clock: newClock(), root: t.TempDir() + "/photos", video: &framedVideo{gate: gate},
		embedder: newFakeEmbedder(), gate: gate}
	f.media = f.video
	f.open(t)
	return f
}

// open opens the store, again after a restart.
func (f *montageFixture) open(t *testing.T) {
	t.Helper()
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.media, Compute: f.computeWorker(), DisableCapacityReserve: true,
		Release: f.release})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
}

func (f *montageFixture) computeWorker() photos.MontageCompute {
	if f.compute == nil {
		return nil
	}
	return f.compute
}

func (f *montageFixture) enableCompute(t *testing.T) {
	t.Helper()
	f.compute = &fakeCompute{}
	f.restart(t)
}

// restart reopens the store, as a release's install restarts the photo
// service; a new model comes that way.
func (f *montageFixture) restart(t *testing.T) {
	t.Helper()
	_ = f.service.Close()
	f.open(t)
}

func (f montageFixture) windows(t *testing.T, object string) (count int, identity string) {
	t.Helper()
	db := catalog(t, f.root)
	if err := db.QueryRow("SELECT COUNT(*), IFNULL(MAX(identity), '') FROM video_windows WHERE object_id = ?", object).Scan(&count, &identity); err != nil {
		t.Fatal(err)
	}
	return count, identity
}

func (f montageFixture) montageJobs(t *testing.T) int {
	t.Helper()
	var count int
	if err := catalog(t, f.root).QueryRow("SELECT COUNT(*) FROM jobs WHERE derivation LIKE 'montage-%'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// grant makes a project of video for p and asks for its analysis.
func (f montageFixture) grant(t *testing.T, p photos.Principal, project string, video photos.Asset) (photos.Grant, string) {
	t.Helper()
	ctx := context.Background()
	grant, err := f.service.RegisterGrant(ctx, granting(p), project, []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(p), grant.ID, []string{video.ID}, false); err != nil {
		t.Fatal(err)
	}
	looked, err := f.service.LookupGrant(ctx, granting(p), grant.ID, []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	return grant, looked[0].ObjectID
}

// analysis reads a granted video's analysis as the montage center does.
func (f montageFixture) analysis(t *testing.T, p photos.Principal, grantID, assetID string) (photos.GrantAnalysis, []byte) {
	t.Helper()
	analysis, file, err := f.service.AnalysisOfGrant(context.Background(), granting(p), grantID, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if file == nil {
		return analysis, nil
	}
	defer func() { _ = file.Close() }()
	vectors, err := photos.ReadWindows(&analysis, file)
	if err != nil {
		t.Fatal(err)
	}
	return analysis, vectors
}

const fakeWindows = "|win=2s1f,frame=edge512,jpeg82,rot=display,dim=4,l2"

func TestWindowVectorsAreMadeOnceForTheVideosGrantsAskFor(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	alicePrivate, shared := libraries(t, f.service, namedAlice)
	libraries(t, f.service, namedBob)
	video := importPhoto(t, f.service, namedAlice, shared.ID, "", "trip.mp4", mp4Bytes(1))
	importPhoto(t, f.service, namedAlice, alicePrivate.ID, "", "never handed over.mp4", mp4Bytes(2))
	if jobs := f.montageJobs(t); jobs != 0 {
		t.Fatalf("montage jobs before any grant = %d", jobs)
	}
	// Alice and Bob each make a project of the same shared video.
	aliceGrant, _ := f.grant(t, namedAlice, "project:alice", video)
	bobGrant, _ := f.grant(t, namedBob, "project:bob", video)
	if jobs := f.montageJobs(t); jobs != 1 {
		t.Fatalf("montage jobs for one video asked twice = %d", jobs)
	}
	looked, _ := f.service.LookupGrant(ctx, granting(namedAlice), aliceGrant.ID, []string{video.ID})
	if looked[0].Windows != photos.AnalysisQueued {
		t.Fatalf("before the analysis: %+v", looked[0])
	}
	// 70 seconds: 35 windows in two chunks, with no compute worker.
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 {
		t.Fatalf("rounds = %d, want two window chunks", rounds)
	}
	analysis, vectors := f.analysis(t, namedBob, bobGrant.ID, video.ID)
	if analysis.Windows != photos.AnalysisDone || len(analysis.List) != 35 || analysis.WindowCount != 35 || analysis.Dimensions != 4 ||
		len(vectors) != 35*4*4 || analysis.Identity != "fake-1"+fakeWindows {
		t.Fatalf("AnalysisOfGrant() = %+v, %d bytes", analysis, len(vectors))
	}
	if last := analysis.List[34]; last.Index != 34 || last.StartMs != 68000 || last.EndMs != 70000 || !last.Flat || analysis.List[0].Flat {
		t.Fatalf("windows = first %+v, last %+v", analysis.List[0], last)
	}
	if f.embedder.embedded != 35 {
		t.Fatalf("embedded %d frames for one video asked twice", f.embedder.embedded)
	}
	// Asking again finds it done.
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), aliceGrant.ID, []string{video.ID}, false); err != nil || f.montageJobs(t) != 0 {
		t.Fatalf("asking again queued %d jobs, %v", f.montageJobs(t), err)
	}
}

// A hung-up chunk ends the round as a paused detection does: the photos'
// own work waits too, and the analysis goes on where it stopped.
func TestAHungUpChunkEndsTheRoundAndGoesOnWhereItStopped(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	importPhoto(t, f.service, namedAlice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, f.service)
	grant, object := f.grant(t, namedAlice, "project:1", video)
	f.video.stopAfter = 10
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); worked || err != nil {
		t.Fatalf("a hung-up chunk = %v, %v", worked, err)
	}
	if f.embedder.embedded != 10 {
		t.Fatalf("embedded %d images in the round a chunk was hung up in; the photo went on", f.embedder.embedded)
	}
	if status := aiStatus(t, f.service, namedAlice); status.State != photos.AIPaused || status.Reason != "foreground" {
		t.Fatalf("status after hanging up = %+v", status)
	}
	analysis, _ := f.analysis(t, namedAlice, grant.ID, video.ID)
	var attempts int
	if err := catalog(t, f.root).QueryRow("SELECT attempts FROM jobs WHERE derivation = 'montage-windows/v1'").Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if analysis.Windows != photos.AnalysisQueued || attempts != 0 {
		t.Fatalf("after hanging up: %s, %d attempts", analysis.Windows, attempts)
	}
	if count, _ := f.windows(t, object); count != 10 {
		t.Fatalf("windows kept from before the hang-up = %d", count)
	}
	f.video.mu.Lock()
	f.video.stopAfter = 0
	f.video.mu.Unlock()
	f.gate.set(true, "")
	processAI(t, f.service, f.embedder, f.gate)
	if count, _ := f.windows(t, object); count != 35 || f.embedder.embedded != 35+1 {
		t.Fatalf("after resuming: %d windows, %d images embedded", count, f.embedder.embedded)
	}
}

// The Worker's own failures are tried again, as a photo's are; only frames
// that cannot be taken or read fail the video for good.
func TestAWorkerFailureIsTriedAgain(t *testing.T) {
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, _ := f.grant(t, namedAlice, "project:1", video)
	f.embedder.failure = errors.New("the Worker's answer was cut short")
	processAI(t, f.service, f.embedder, f.gate)
	var state, class string
	if err := catalog(t, f.root).QueryRow("SELECT state, error_class FROM jobs WHERE derivation = 'montage-windows/v1'").Scan(&state, &class); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || class != "io" {
		t.Fatalf("after a failure of the Worker: %s, %q; want it tried again", state, class)
	}
	f.embedder.failure = nil
	f.clock.Advance(time.Hour)
	processAI(t, f.service, f.embedder, f.gate)
	if analysis, _ := f.analysis(t, namedAlice, grant.ID, video.ID); analysis.Windows != photos.AnalysisDone {
		t.Fatalf("after trying again: %s", analysis.Windows)
	}
}

func TestAnalysesNoGrantRefersToGoAfterAWeekAndANewModelRedoesOnlyTheRest(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	kept := importPhoto(t, f.service, namedAlice, private.ID, "", "kept.mp4", mp4Bytes(1))
	left := importPhoto(t, f.service, namedAlice, private.ID, "", "left.mp4", mp4Bytes(2))
	_, keptObject := f.grant(t, namedAlice, "project:a", kept)
	leftGrant, leftObject := f.grant(t, namedAlice, "project:b", left)
	processAI(t, f.service, f.embedder, f.gate)
	if err := f.service.RevokeGrant(ctx, leftGrant.ID); err != nil {
		t.Fatal(err)
	}
	// A new model comes with a release, which restarts the photo service:
	// only the video a grant still refers to is redone.
	f.restart(t)
	f.embedder.model = "fake-2"
	processAI(t, f.service, f.embedder, f.gate)
	if _, identity := f.windows(t, keptObject); identity != "fake-2"+fakeWindows {
		t.Fatalf("the kept video's windows = %s", identity)
	}
	if _, identity := f.windows(t, leftObject); !strings.HasPrefix(identity, "fake-1|") {
		t.Fatalf("the left video's windows = %s; it was redone", identity)
	}
	// A week after the last grant let go of it, the left video's analysis goes.
	if removed, err := f.service.PruneMontageAnalysis(ctx); err != nil || removed != 0 {
		t.Fatalf("PruneMontageAnalysis() at once = %d, %v", removed, err)
	}
	f.clock.Advance(7*24*time.Hour + time.Minute)
	if removed, err := f.service.PruneMontageAnalysis(ctx); err != nil || removed != 1 {
		t.Fatalf("PruneMontageAnalysis() a week later = %d, %v", removed, err)
	}
	if count, _ := f.windows(t, leftObject); count != 0 {
		t.Fatalf("windows left of an analysis no grant refers to = %d", count)
	}
	if count, _ := f.windows(t, keptObject); count != 35 {
		t.Fatalf("windows of the kept video = %d", count)
	}
}

// A video granted again within the week, after a new model came, has its
// windows made again with that model rather than handed over as they were.
func TestAVideoGrantedAgainAfterANewModelIsRedone(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	first, object := f.grant(t, namedAlice, "project:1", video)
	processAI(t, f.service, f.embedder, f.gate)
	if err := f.service.RevokeGrant(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.embedder.model = "fake-2"
	processAI(t, f.service, f.embedder, f.gate)
	again, _ := f.grant(t, namedAlice, "project:2", video)
	if analysis, vectors := f.analysis(t, namedAlice, again.ID, video.ID); analysis.Windows != photos.AnalysisQueued || vectors != nil {
		t.Fatalf("granted again after a new model: %+v", analysis)
	}
	processAI(t, f.service, f.embedder, f.gate)
	analysis, vectors := f.analysis(t, namedAlice, again.ID, video.ID)
	if analysis.Windows != photos.AnalysisDone || analysis.Identity != "fake-2"+fakeWindows || len(vectors) != 35*4*4 {
		t.Fatalf("after redoing: %+v", analysis)
	}
	if _, identity := f.windows(t, object); identity != "fake-2"+fakeWindows {
		t.Fatalf("windows = %s", identity)
	}
}

// A video no grant refers to any more is not worked on, and a chunk whose
// video goes while it runs keeps nothing.
func TestAnalysisStopsForVideosThatWent(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	dropped := importPhoto(t, f.service, namedAlice, private.ID, "", "dropped.mp4", mp4Bytes(1))
	purged := importPhoto(t, f.service, namedAlice, private.ID, "", "purged.mp4", mp4Bytes(2))
	droppedGrant, _ := f.grant(t, namedAlice, "project:1", dropped)
	if err := f.service.RevokeGrant(ctx, droppedGrant.ID); err != nil {
		t.Fatal(err)
	}
	_, purgedObject := f.grant(t, namedAlice, "project:2", purged)
	f.video.during = func(asked int) {
		if asked != 5 {
			return
		}
		if _, err := f.service.Trash(ctx, namedAlice, purged.ID); err != nil {
			t.Error(err)
		}
		if err := f.service.Purge(ctx, namedAlice, purged.ID); err != nil {
			t.Error(err)
		}
	}
	processAI(t, f.service, f.embedder, f.gate)
	if f.video.asked != 30 {
		t.Fatalf("took %d frames; want only the purged video's first chunk", f.video.asked)
	}
	if jobs := f.montageJobs(t); jobs != 0 {
		t.Fatalf("montage jobs left = %d", jobs)
	}
	if count, _ := f.windows(t, purgedObject); count != 0 {
		t.Fatalf("windows of a video purged while its chunk ran = %d", count)
	}
}

// indexRecorder records the size of each upsert of window points.
type indexRecorder struct {
	*vectorindex.Client
	mu       sync.Mutex
	upserts  []int
	refusing string
}

func (x *indexRecorder) Upsert(ctx context.Context, collection string, points []vectorindex.Point) error {
	if strings.HasPrefix(collection, "video_windows_v1_") {
		x.mu.Lock()
		x.upserts = append(x.upserts, len(points))
		x.mu.Unlock()
	}
	return x.Client.Upsert(ctx, collection, points)
}

func (x *indexRecorder) CreateCollection(ctx context.Context, collection vectorindex.Collection) error {
	if x.refusing != "" && strings.HasPrefix(collection.Name, x.refusing) {
		return errors.New("the index refuses the collection")
	}
	return x.Client.CreateCollection(ctx, collection)
}

func (x *indexRecorder) written() (calls, points int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, n := range x.upserts {
		points += n
	}
	return len(x.upserts), points
}

type windowIndex struct {
	f     *montageFixture
	index *indexRecorder
}

func newWindowIndex(t *testing.T, f *montageFixture) windowIndex {
	t.Helper()
	instance := vectorindextest.Start(t)
	client := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = client.Close() })
	return windowIndex{f: f, index: &indexRecorder{Client: client}}
}

func (w windowIndex) sync(t *testing.T) {
	t.Helper()
	for range 100 {
		worked, err := w.f.service.SyncWindows(context.Background(), w.index, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("SyncWindows() never caught up")
}

// points counts the points in the windows collection, by object.
func (w windowIndex) points(t *testing.T) (map[string]int, string) {
	t.Helper()
	ctx := context.Background()
	names, err := w.index.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var collection string
	for _, name := range names {
		if strings.HasPrefix(name, "video_windows_v1_") {
			collection = name
		}
	}
	counts := map[string]int{}
	if collection == "" {
		return counts, ""
	}
	err = w.index.Scan(ctx, collection, []string{"object"}, func(entry vectorindex.Entry) error {
		counts[entry.Keywords["object"][0]]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts, collection
}

func TestWindowVectorsReachTheirOwnCollectionAndGoWithTheirVideo(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	w := newWindowIndex(t, &f)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	_, object := f.grant(t, namedAlice, "project:1", video)
	// The first chunk is stored, but the video waits until its analysis is
	// done: its points are written once, not after every chunk.
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil {
		t.Fatalf("first chunk = %v, %v", worked, err)
	}
	w.sync(t)
	if counts, _ := w.points(t); counts[object] != 0 {
		t.Fatalf("points while the analysis runs = %d", counts[object])
	}
	processAI(t, f.service, f.embedder, f.gate)
	w.sync(t)
	counts, collection := w.points(t)
	if counts[object] != 35 {
		t.Fatalf("points = %d in %q", counts[object], collection)
	}
	if calls, written := w.index.written(); calls != 1 || written != 35 {
		t.Fatalf("wrote %d points in %d calls for 35 windows", written, calls)
	}
	// The index lost points of the collection the Catalog knows: the
	// comparison finds the video and writes it again.
	if err := w.index.DeleteMatching(ctx, collection, "object", []string{object}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReconcileWindows(ctx, w.index, nil); err != nil {
		t.Fatal(err)
	}
	w.sync(t)
	if counts, again := w.points(t); counts[object] != 35 || again != collection {
		t.Fatalf("after reconciling: %d points in %q", counts[object], again)
	}
	// The index lost the collection: it is built again from the Catalog.
	if err := w.index.DeleteCollection(ctx, collection); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReconcileWindows(ctx, w.index, nil); err != nil {
		t.Fatal(err)
	}
	w.sync(t)
	if counts, again := w.points(t); counts[object] != 35 || again == collection {
		t.Fatalf("after rebuilding: %d points in %q", counts[object], again)
	}
	// The video purged, its object and windows go, and so do its points.
	if _, err := f.service.Trash(ctx, namedAlice, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Purge(ctx, namedAlice, video.ID); err != nil {
		t.Fatal(err)
	}
	w.sync(t)
	if counts, _ := w.points(t); counts[object] != 0 {
		t.Fatalf("points left of a purged video = %d", counts[object])
	}
}

func TestCardsAndTheirQualityFollowTheWindows(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, err := f.service.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID}, false); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	analysis, file, err := f.service.AnalysisOfGrant(ctx, granting(namedAlice), grant.ID, video.ID)
	if err != nil || file == nil {
		t.Fatalf("analysis payload = %v, %v", file, err)
	}
	defer func() { _ = file.Close() }()
	_, err = photos.ReadWindows(&analysis, file)
	if err != nil || analysis.Cards != photos.AnalysisDone || analysis.Quality != photos.AnalysisDone || len(analysis.CardList) != 2 {
		t.Fatalf("analysis = cards %s, quality %s, %d cards, %v", analysis.Cards, analysis.Quality, len(analysis.CardList), err)
	}
	for _, card := range analysis.CardList {
		if card.Quality == nil || card.Quality.Blur == nil || *card.Quality.Blur != 4.5 || len(card.Times) != 3 {
			t.Fatalf("card = %+v", card)
		}
	}
	looked, _ := f.service.LookupGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID})
	if looked[0].Cards != photos.AnalysisDone || looked[0].Quality != photos.AnalysisDone {
		t.Fatalf("lookup = %+v", looked[0])
	}
	// Keyframes 512 wide with blurdetect; shake frames 320 x 180 ten a
	// second over the 34-second card in two chunks that share a frame.
	var keyframes, shakes int
	for _, request := range f.video.requests {
		switch {
		case request.Format == ffmpegworker.FormatJPEG && request.Width == 512:
			keyframes++
			if !request.Blur || request.Quality != 3 {
				t.Errorf("keyframe request = %+v", request)
			}
		case request.Format == ffmpegworker.FormatGray && request.Width == 320:
			shakes++
			if request.Height != 180 || request.Rate != 10 || request.End-request.Start > 30 {
				t.Errorf("shake request = %+v", request)
			}
		}
	}
	if keyframes != 2 || shakes != 4 {
		t.Fatalf("keyframe requests %d, shake requests %d; want one and two a card", keyframes, shakes)
	}
	if f.compute.moves[0] != 34*10-1 {
		t.Fatalf("moves of the first card = %d, want one between each pair of its 340 frames", f.compute.moves[0])
	}
}

func TestMontageAnalysisBackfillsAndFinishesAnEmptyResult(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	f.enableCompute(t)
	f.compute.empty = true
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	owner := granting(namedAlice)
	grant, err := f.service.RegisterGrant(ctx, owner, "project:empty", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, owner, grant.ID, []string{video.ID}, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil {
			t.Fatalf("process windows = %v, %v", worked, err)
		}
	}
	// Simulate a result made on M5, where the windows left no card job.
	if _, err := catalog(t, f.root).Exec("DELETE FROM jobs WHERE derivation = 'montage-cards/v1'"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, owner, grant.ID, []string{video.ID}, false); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	analysis, file, err := f.service.AnalysisOfGrant(ctx, owner, grant.ID, video.ID)
	if file != nil {
		_ = file.Close()
	}
	if err != nil || analysis.Cards != photos.AnalysisDone || analysis.Quality != photos.AnalysisDone ||
		analysis.CardsIdentity == "" || analysis.QualityIdentity == "" || len(analysis.CardList) != 0 ||
		analysis.Described != photos.AnalysisDone || !strings.HasSuffix(analysis.DescriptionsIdentity, "|quality="+analysis.QualityIdentity) {
		t.Fatalf("empty analysis = %+v, %v", analysis, err)
	}
	f.restart(t)
	restored, file, err := f.service.AnalysisOfGrant(ctx, owner, grant.ID, video.ID)
	if file != nil {
		_ = file.Close()
	}
	if err != nil || restored.Described != photos.AnalysisDone || restored.DescriptionsIdentity != analysis.DescriptionsIdentity || restored.DescriptionCount != 0 {
		t.Fatalf("empty analysis after restart = %+v, %v", restored, err)
	}

}

func TestMontageQualityPausesForForegroundWithoutSpendingAnAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := newMontageFixture(t)
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, _ := f.grant(t, namedAlice, "project:foreground", video)
	for range 3 {
		if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil {
			t.Fatalf("process predecessor = %v, %v", worked, err)
		}
	}
	// Enter the foreground while the quality round is taking keyframes.
	f.video.stopAfter = f.video.asked
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); worked || err != nil || ctx.Err() != nil {
		t.Fatalf("quality round = %v, %v; context = %v", worked, err, ctx.Err())
	}
	var attempts int
	if err := catalog(t, f.root).QueryRow("SELECT attempts FROM jobs WHERE derivation = 'montage-quality/v1'").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("quality attempts after pausing = %d, %v", attempts, err)
	}
	f.video.stopAfter = 0
	f.gate.set(true, "")
	processAI(t, f.service, f.embedder, f.gate)
	looked, err := f.service.LookupGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID})
	if err != nil || len(looked) != 1 || looked[0].Quality != photos.AnalysisDone {
		t.Fatalf("retrying the quality round = %+v, %v", looked, err)
	}
}

func TestMontageAnalysisRetriesBothDownstreamStages(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	owner := granting(namedAlice)
	grant, err := f.service.RegisterGrant(ctx, owner, "project:retry", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	analyze := func(retry bool) {
		t.Helper()
		if err := f.service.AnalyzeGrant(ctx, owner, grant.ID, []string{video.ID}, retry); err != nil {
			t.Fatal(err)
		}
		processAI(t, f.service, f.embedder, f.gate)
	}
	state := func(cards, quality photos.AnalysisState) {
		t.Helper()
		asset, err := f.service.LookupGrant(ctx, owner, grant.ID, []string{video.ID})
		if err != nil || len(asset) != 1 || asset[0].Cards != cards || asset[0].Quality != quality {
			t.Fatalf("lookup = %+v, %v; want %q and %q", asset, err, cards, quality)
		}
	}
	f.compute.cardsErr = photos.ErrComputeInvalidResult
	analyze(false)
	state(photos.AnalysisFailed, photos.AnalysisNone)
	f.compute.cardsErr = nil
	f.compute.qualityErr = photos.ErrComputeInvalidResult
	analyze(true)
	state(photos.AnalysisDone, photos.AnalysisFailed)
	f.compute.qualityErr = nil
	analyze(true)
	state(photos.AnalysisDone, photos.AnalysisDone)
}

// A long video's points go to the index in batches small enough for any
// request limit.
func TestALongVideosPointsGoInBatches(t *testing.T) {
	f := newMontageFixture(t)
	f.video.duration = 1100
	w := newWindowIndex(t, &f)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "long.mp4", mp4Bytes(1))
	_, object := f.grant(t, namedAlice, "project:1", video)
	processAI(t, f.service, f.embedder, f.gate)
	w.sync(t)
	if counts, _ := w.points(t); counts[object] != 550 {
		t.Fatalf("points = %d, want 550", counts[object])
	}
	w.index.mu.Lock()
	defer w.index.mu.Unlock()
	if len(w.index.upserts) != 2 || w.index.upserts[0] != 500 {
		t.Fatalf("upserts = %v, want 500 and the rest", w.index.upserts)
	}
}

// After a new model, the collection follows the windows made with it, even
// when the video whose windows were stored last is not redone.
func TestTheWindowsCollectionFollowsTheNewModel(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	w := newWindowIndex(t, &f)
	private, _ := libraries(t, f.service, namedAlice)
	kept := importPhoto(t, f.service, namedAlice, private.ID, "", "kept.mp4", mp4Bytes(1))
	_, keptObject := f.grant(t, namedAlice, "project:1", kept)
	processAI(t, f.service, f.embedder, f.gate)
	last := importPhoto(t, f.service, namedAlice, private.ID, "", "last.mp4", mp4Bytes(2))
	lastGrant, _ := f.grant(t, namedAlice, "project:2", last)
	processAI(t, f.service, f.embedder, f.gate)
	if err := f.service.RevokeGrant(ctx, lastGrant.ID); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.embedder.model = "fake-2"
	f.clock.Advance(time.Minute)
	processAI(t, f.service, f.embedder, f.gate)
	w.sync(t)
	if counts, collection := w.points(t); counts[keptObject] != 35 || len(counts) != 1 {
		t.Fatalf("points after a new model = %v in %q; want the redone video's", counts, collection)
	}
}

// A windows collection the index keeps refusing holds up neither the photos'
// sync nor their daily comparison.
func TestAFailingWindowsCollectionLeavesThePhotosAlone(t *testing.T) {
	f := newMontageFixture(t)
	w := newWindowIndex(t, &f)
	w.index.refusing = "video_windows_v1_"
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	importPhoto(t, f.service, namedAlice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, f.service)
	f.grant(t, namedAlice, "project:1", video)
	processAI(t, f.service, f.embedder, f.gate)
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan photos.VectorSyncEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.service.RunVectorSync(ctx, w.index, &fakeGate{open: true}, 10*time.Millisecond, func(event photos.VectorSyncEvent) {
			select {
			case events <- event:
			default:
			}
		}, func(error) {})
	}()
	defer func() { cancel(); <-done }()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == "reconciled" && strings.HasPrefix(event.Collection, "images_v1_") {
				return
			}
		case <-deadline:
			t.Fatal("the photos' collection was not compared while the windows' failed")
		}
	}
}

// ReadWindows takes exactly a done analysis's windows and nothing else.
func TestReadWindowsTakesExactlyTheWindows(t *testing.T) {
	analysis := photos.GrantAnalysis{Windows: photos.AnalysisDone, WindowCount: 2, Dimensions: 3}
	var file bytes.Buffer
	for i := range 6 {
		_ = binary.Write(&file, binary.LittleEndian, float32(i))
	}
	_ = binary.Write(&file, binary.LittleEndian, []uint32{0, 2000, 0, 2000, 3500, 1})
	vectors, err := photos.ReadWindows(&analysis, bytes.NewReader(file.Bytes()))
	if err != nil || len(vectors) != 2*3*4 {
		t.Fatalf("ReadWindows() = %d bytes, %v", len(vectors), err)
	}
	if second := analysis.List[1]; second != (photos.Window{Index: 1, StartMs: 2000, EndMs: 3500, Flat: true}) {
		t.Fatalf("second window = %+v", second)
	}
	for name, content := range map[string][]byte{
		"short": file.Bytes()[:file.Len()-1],
		"long":  append(bytes.Clone(file.Bytes()), 0),
	} {
		if _, err := photos.ReadWindows(&photos.GrantAnalysis{WindowCount: 2, Dimensions: 3}, bytes.NewReader(content)); err == nil {
			t.Errorf("ReadWindows() of a %s file succeeded", name)
		}
	}
	for _, shape := range [][2]int{{0, 3}, {photos.MaxVideoWindows + 1, 3}, {2, 0}, {2, photos.MaxWindowDimensions + 1}} {
		if _, err := photos.ReadWindows(&photos.GrantAnalysis{WindowCount: shape[0], Dimensions: shape[1]}, bytes.NewReader(file.Bytes())); err == nil {
			t.Errorf("ReadWindows() of %d windows of %d dimensions succeeded", shape[0], shape[1])
		}
	}
}

func FuzzReadWindows(f *testing.F) {
	f.Add(2, 3, make([]byte, 2*3*4+2*12))
	f.Add(1, 1, []byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, count, dimensions int, content []byte) {
		analysis := photos.GrantAnalysis{WindowCount: count, Dimensions: dimensions}
		vectors, err := photos.ReadWindows(&analysis, bytes.NewReader(content))
		if err != nil {
			return
		}
		if len(content) != count*(dimensions*4+12) || len(vectors) != count*dimensions*4 || len(analysis.List) != count {
			t.Fatalf("ReadWindows() took %d bytes as %d windows of %d dimensions", len(content), count, dimensions)
		}
	})
}

// A video with more windows than the montage center takes fails as too
// long rather than being analysed.
func TestAVideoTooLongForTheMontageCenterFails(t *testing.T) {
	f := newMontageFixture(t)
	f.media, f.root = &longVideo{framedVideo: f.video}, t.TempDir()+"/long"
	f.open(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "night.mp4", mp4Bytes(1))
	grant, _ := f.grant(t, namedAlice, "project:1", video)
	processAI(t, f.service, f.embedder, f.gate)
	if analysis, _ := f.analysis(t, namedAlice, grant.ID, video.ID); analysis.Windows != photos.AnalysisFailed || f.video.asked != 0 {
		t.Fatalf("a seven-hour video's analysis = %s after %d frames", analysis.Windows, f.video.asked)
	}
}

// longVideo reads every file as a video of seven hours.
type longVideo struct{ *framedVideo }

func (v *longVideo) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 7 * 3600},
		Streams: []ffmpegworker.ProbeStream{{Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}},
	}, nil
}

// A video queued for the window index when no video has windows any more,
// as after the last one's analysis was pruned, leaves the queue: there is
// no collection to bring in line, and the photo index's sync goes on.
func TestWindowSyncWithNoWindowsLeftEmptiesItsQueue(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	if err := f.service.QueueWindows(ctx, "object:gone"); err != nil {
		t.Fatal(err)
	}
	// No windows and no collection: the index is never asked.
	worked, err := f.service.SyncWindows(ctx, nil, nil)
	if err != nil || !worked {
		t.Fatalf("SyncWindows() = %v, %v", worked, err)
	}
	if worked, err = f.service.SyncWindows(ctx, nil, nil); err != nil || worked {
		t.Fatalf("SyncWindows() again = %v, %v", worked, err)
	}
}

// libraryInUseGate is a gate on which someone uses the photo library, while
// member says whether the work a member waits for may go on.
type libraryInUseGate struct {
	lockedGate
	member lockedGate
}

func (g *libraryInUseGate) OpenForMember(ctx context.Context, need uint64) (bool, string) {
	return g.member.Open(ctx, need)
}

func (g *libraryInUseGate) ContinueForMember(ctx context.Context, need uint64) (bool, string) {
	return g.member.Open(ctx, need)
}

// While someone uses the photo library, the analysis a member's job waits
// for goes on, and only it: the photos' own work waits for the library to be
// quiet. Short memory still holds the analysis back. Before, a member who
// kept the montage center's window open, which plays videos of the library,
// kept their own job waiting for as long as the window was open.
func TestTheMontageAnalysisGoesOnWhileTheLibraryIsInUse(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	importPhoto(t, f.service, namedAlice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, f.service)
	grant, object := f.grant(t, namedAlice, "project:in-use", video)
	gate := &libraryInUseGate{}
	gate.set(false, "foreground")
	gate.member.set(false, "memory_low")
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, gate); worked || err != nil || f.embedder.embedded != 0 {
		t.Fatalf("a round with memory short = %v, %v; %d images embedded", worked, err, f.embedder.embedded)
	}
	if status := aiStatus(t, f.service, namedAlice); status.State != photos.AIPaused || status.Reason != "memory_low" {
		t.Fatalf("status with memory short = %+v", status)
	}
	gate.member.set(true, "")
	processAI(t, f.service, f.embedder, gate)
	if count, _ := f.windows(t, object); count != 35 || f.embedder.embedded != 35 {
		t.Fatalf("while the library is in use: %d windows, %d images embedded; the photo must wait", count, f.embedder.embedded)
	}
	if analysis, _ := f.analysis(t, namedAlice, grant.ID, video.ID); analysis.Windows != photos.AnalysisDone {
		t.Fatalf("windows while the library is in use = %s", analysis.Windows)
	}
	if status := aiStatus(t, f.service, namedAlice); status.State != photos.AIPaused || status.Reason != "foreground" {
		t.Fatalf("status once the analysis is done = %+v", status)
	}
	gate.set(true, "")
	processAI(t, f.service, f.embedder, gate)
	if f.embedder.embedded != 36 {
		t.Fatalf("once the library is quiet, %d images embedded; want the photo too", f.embedder.embedded)
	}
}
