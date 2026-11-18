package photos_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/vectorindex/vectorindextest"
)

// framedVideo reads every file as a 70-second video and hands over its
// frames: a patterned picture before 60 seconds, black after. stopAfter,
// when set, hangs the frames up after that many, as someone starting to use
// the library would, by closing the gate.
type framedVideo struct {
	mu        sync.Mutex
	asked     int
	stopAfter int
	gate      *lockedGate
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
	return ffmpegworker.Probe{
		Format:  ffmpegworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 70},
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
	for _, at := range request.Times {
		v.mu.Lock()
		v.asked++
		stop := v.stopAfter > 0 && v.asked > v.stopAfter
		v.mu.Unlock()
		if stop {
			v.gate.set(false, "foreground")
			<-ctx.Done()
			return ffmpegworker.FramesResult{}, ctx.Err()
		}
		if err := each(frameJPEG(at < 60)); err != nil {
			return ffmpegworker.FramesResult{}, err
		}
	}
	return ffmpegworker.FramesResult{Times: request.Times}, nil
}

type montageFixture struct {
	service  *photos.Service
	clock    *clocktest.Clock
	root     string
	video    *framedVideo
	embedder *fakeEmbedder
	gate     *lockedGate
}

func newMontageFixture(t *testing.T) montageFixture {
	t.Helper()
	gate := &lockedGate{open: true}
	f := montageFixture{clock: newClock(), root: filepath.Join(t.TempDir(), "photos"), video: &framedVideo{gate: gate},
		embedder: newFakeEmbedder(), gate: gate}
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.video, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	return f
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
	var grants []photos.Grant
	for _, p := range []photos.Principal{namedAlice, namedBob} {
		grant, err := f.service.RegisterGrant(ctx, granting(p), "project:"+p.Username, []string{video.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.AnalyzeGrant(ctx, granting(p), grant.ID, []string{video.ID}, false); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	if jobs := f.montageJobs(t); jobs != 1 {
		t.Fatalf("montage jobs for one video asked twice = %d", jobs)
	}
	looked, _ := f.service.LookupGrant(ctx, granting(namedAlice), grants[0].ID, []string{video.ID})
	if looked[0].Windows != photos.AnalysisQueued {
		t.Fatalf("before the analysis: %+v", looked[0])
	}
	// 70 seconds: 35 windows, in two chunks.
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 {
		t.Fatalf("rounds = %d, want a chunk each", rounds)
	}
	analysis, vectors, err := f.service.AnalysisOfGrant(ctx, granting(namedBob), grants[1].ID, video.ID)
	if err != nil || analysis.Windows != photos.AnalysisDone || len(analysis.List) != 35 || len(vectors) != 35*4*4 {
		t.Fatalf("AnalysisOfGrant() = %d windows, %s, %d bytes, %v", len(analysis.List), analysis.Windows, len(vectors), err)
	}
	if last := analysis.List[34]; last.StartMs != 68000 || last.EndMs != 70000 || !last.Flat || analysis.List[0].Flat {
		t.Fatalf("windows = first %+v, last %+v", analysis.List[0], last)
	}
	if f.embedder.embedded != 35 {
		t.Fatalf("embedded %d frames for one video asked twice", f.embedder.embedded)
	}
	// Asking again finds it done.
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grants[0].ID, []string{video.ID}, false); err != nil || f.montageJobs(t) != 0 {
		t.Fatalf("asking again queued %d jobs, %v", f.montageJobs(t), err)
	}
}

func TestAHungUpChunkGoesOnWhereItStopped(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, err := f.service.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID}, false); err != nil {
		t.Fatal(err)
	}
	f.video.stopAfter = 10
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); worked || err != nil {
		t.Fatalf("a hung-up chunk = %v, %v", worked, err)
	}
	analysis, _, _ := f.service.AnalysisOfGrant(ctx, granting(namedAlice), grant.ID, video.ID)
	looked, _ := f.service.LookupGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID})
	var attempts int
	if err := catalog(t, f.root).QueryRow("SELECT attempts FROM jobs WHERE derivation = 'montage-windows/v1'").Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if analysis.Windows != photos.AnalysisQueued || attempts != 0 {
		t.Fatalf("after hanging up: %s, %d attempts", analysis.Windows, attempts)
	}
	if count, _ := f.windows(t, looked[0].ObjectID); count != 10 {
		t.Fatalf("windows kept from before the hang-up = %d", count)
	}
	f.video.mu.Lock()
	f.video.stopAfter = 0
	f.video.mu.Unlock()
	f.gate.set(true, "")
	processAI(t, f.service, f.embedder, f.gate)
	if count, _ := f.windows(t, looked[0].ObjectID); count != 35 || f.embedder.embedded != 35 {
		t.Fatalf("after resuming: %d windows, %d frames embedded", count, f.embedder.embedded)
	}
}

func TestAnalysesNoGrantRefersToGoAfterAWeekAndANewModelRedoesOnlyTheRest(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	kept := importPhoto(t, f.service, namedAlice, private.ID, "", "kept.mp4", mp4Bytes(1))
	left := importPhoto(t, f.service, namedAlice, private.ID, "", "left.mp4", mp4Bytes(2))
	var grants []photos.Grant
	for i, video := range []photos.Asset{kept, left} {
		grant, err := f.service.RegisterGrant(ctx, granting(namedAlice), "project:"+string(rune('a'+i)), []string{video.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID}, false); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	processAI(t, f.service, f.embedder, f.gate)
	objects := map[string]string{}
	for i, video := range []photos.Asset{kept, left} {
		looked, _ := f.service.LookupGrant(ctx, granting(namedAlice), grants[i].ID, []string{video.ID})
		objects[video.ID] = looked[0].ObjectID
	}
	if err := f.service.RevokeGrant(ctx, grants[1].ID); err != nil {
		t.Fatal(err)
	}
	// A new model comes with a release, which restarts the photo service:
	// only the video a grant still refers to is redone.
	_ = f.service.Close()
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.video, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	f.embedder.model = "fake-2"
	processAI(t, f.service, f.embedder, f.gate)
	if _, identity := f.windows(t, objects[kept.ID]); identity != "fake-2|win=2s1f,frame=edge512,jpeg82,rot=display,dim=768,l2" {
		t.Fatalf("the kept video's windows = %s", identity)
	}
	if _, identity := f.windows(t, objects[left.ID]); identity[:7] != "fake-1|" {
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
	if count, _ := f.windows(t, objects[left.ID]); count != 0 {
		t.Fatalf("windows left of an analysis no grant refers to = %d", count)
	}
	if count, _ := f.windows(t, objects[kept.ID]); count != 35 {
		t.Fatalf("windows of the kept video = %d", count)
	}
}

func TestWindowVectorsReachTheirOwnCollectionAndGoWithTheirVideo(t *testing.T) {
	instance := vectorindextest.Start(t)
	index := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = index.Close() })
	ctx := context.Background()
	f := newMontageFixture(t)
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
	sync := func() {
		t.Helper()
		for {
			worked, err := f.service.SyncWindows(ctx, index, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
	}
	points := func() (count int, collection string) {
		t.Helper()
		names, err := index.Collections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if len(name) > 17 && name[:17] == "video_windows_v1_" {
				collection = name
			}
		}
		if collection == "" {
			return 0, ""
		}
		if err := index.Scan(ctx, collection, []string{"object"}, func(vectorindex.Entry) error { count++; return nil }); err != nil {
			t.Fatal(err)
		}
		return count, collection
	}
	sync()
	count, collection := points()
	if count != 35 {
		t.Fatalf("points = %d in %q", count, collection)
	}
	// The index lost the collection: it is built again from the Catalog.
	if err := index.DeleteCollection(ctx, collection); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReconcileWindows(ctx, index, nil); err != nil {
		t.Fatal(err)
	}
	sync()
	if count, again := points(); count != 35 || again == collection {
		t.Fatalf("after rebuilding: %d points in %q", count, again)
	}
	// The video purged, its object and windows go, and so do its points.
	if _, err := f.service.Trash(ctx, namedAlice, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Purge(ctx, namedAlice, video.ID); err != nil {
		t.Fatal(err)
	}
	sync()
	if count, _ := points(); count != 0 {
		t.Fatalf("points left of a purged video = %d", count)
	}
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
