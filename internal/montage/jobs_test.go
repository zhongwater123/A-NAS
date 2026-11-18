package montage_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// The photo library's side of jobs: every video is ten seconds, five
// windows and two described cards.

func (l *library) grantOf(grantID, assetID string) (video, error) {
	g, ok := l.grants[grantID]
	if !ok || g.Revoked {
		return video{}, refused(photosapi.GrantNotFound)
	}
	v, ok := l.videos[assetID]
	if !ok || !g.assets[assetID] || v.trashed {
		return video{}, refused(photosapi.GrantAssetNotFound)
	}
	return v, nil
}

func (l *library) Open(_ context.Context, grantID, assetID string) (*os.File, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.grantOf(grantID, assetID)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(os.DevNull)
	return file, v.object, err
}

func (l *library) Analyze(_ context.Context, _ string, ids []string, retry bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if retry {
		for _, id := range ids {
			if l.states[id] == photos.AnalysisFailed {
				l.retried = append(l.retried, id)
				delete(l.states, id)
			}
		}
	}
	return nil
}

func (l *library) Analysis(_ context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.grantOf(grantID, assetID)
	if err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	done := photos.AnalysisDone
	analysis := photos.GrantAnalysis{ObjectID: v.object, Windows: done, Identity: "model|win", Cards: done, Quality: done, Described: done,
		CardList: []photos.CardAnalysis{
			{Index: 0, StartMs: 0, EndMs: 4000, Times: []float64{1, 3}, Quality: &photos.CardQuality{}},
			{Index: 1, StartMs: 4000, EndMs: 10000, Times: []float64{5, 8}, Quality: &photos.CardQuality{}},
		},
		Descriptions: []photos.CardDescription{
			{Card: 0, Description: json.RawMessage(`{"beats":[{"shot_size":"远景","what":"海"},{"shot_size":"全景","what":"船"}]}`)},
			{Card: 1, Description: json.RawMessage(`{"beats":[{"shot_size":"近景","what":"人"},{"shot_size":"特写","what":"手"}]}`)},
		}}
	vectors := make([]byte, 5*768*4)
	for i := range 5 {
		analysis.List = append(analysis.List, photos.Window{Index: i, StartMs: 2000 * i, EndMs: 2000*i + 2000})
		binary.LittleEndian.PutUint32(vectors[(i*768+i)*4:], math.Float32bits(1))
	}
	return analysis, vectors, nil
}

func (l *library) SaveFilm(_ context.Context, grantID, versionKey, name string, film *os.File) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.grants[grantID]
	if !ok || g.Revoked {
		return "", refused(photosapi.GrantNotFound)
	}
	if l.noSpace {
		return "", refused(photosapi.GrantNoSpace)
	}
	id, saved := l.films[versionKey]
	if !saved {
		if _, err := io.ReadAll(film); err != nil {
			return "", err
		}
		id = fmt.Sprintf("asset:film-%d", len(l.films)+1)
		l.films[versionKey] = id
		l.videos[id] = video{owner: g.OwnerUserID, object: "object:" + name}
		g.assets[id] = true
		l.saves++
	}
	if l.lostSave > 0 {
		// Saved, but the answer never came back.
		l.lostSave--
		return "", errors.New("photo grant socket: connection reset")
	}
	return id, nil
}

func (l *library) filmNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var names []string
	for _, id := range l.films {
		names = append(names, strings.TrimPrefix(l.videos[id].object, "object:"))
	}
	slices.Sort(names)
	return names
}

func (l *library) set(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn()
}

// compute stands in for the compute Worker: the catalog is every card, and
// the fill asks for the clear span of two cards, from two videos when there
// are two, then fills four seconds with two seconds of each.
type compute struct {
	mu       sync.Mutex
	failFill int
	external bool
	fills    int
}

func (c *compute) Catalog(_ context.Context, request montage.CatalogRequest) ([]string, error) {
	var ids []string
	for _, card := range request.Cards {
		ids = append(ids, card.ID)
	}
	return ids, nil
}

func (c *compute) Fill(_ context.Context, request montage.FillRequest, vectors []float32, dimensions int) (montage.FillAnswer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fills++
	if c.failFill > 0 {
		c.failFill--
		return montage.FillAnswer{}, errors.New("the montage compute worker is not available: killed")
	}
	if dimensions != 768 || len(vectors) != len(request.Rows)*768 {
		return montage.FillAnswer{}, fmt.Errorf("%d vectors for %d rows", len(vectors), len(request.Rows))
	}
	// The first card of each video first, then the others.
	var firsts, rest []montage.Card
	for _, id := range request.Catalog {
		card := request.Cards[id]
		if slices.ContainsFunc(firsts, func(c montage.Card) bool { return c.Video == card.Video }) {
			rest = append(rest, card)
		} else {
			firsts = append(firsts, card)
		}
	}
	firsts = append(firsts, rest...)
	var clips []json.RawMessage
	for i, card := range firsts[:min(2, len(firsts))] {
		name := fmt.Sprintf("%s/1@%.2f-%.2f", card.ID, card.Start, card.End)
		if _, ok := request.Known[name]; !ok {
			return montage.FillAnswer{Need: &montage.FillNeed{Name: name, Beat: card.ID + "/1", Video: card.Video,
				Key: card.Times[0], Lo: card.Start, Hi: card.End}}, nil
		}
		video := card.Video
		if c.external {
			video = "asset:b1"
		}
		clip, _ := json.Marshal(montage.Clip{Card: card.ID, Video: video, Start: card.Start, End: card.Start + 2,
			Slot: [2]float64{float64(2 * i), float64(2*i + 2)}, Share: 2})
		clips = append(clips, clip)
	}
	return montage.FillAnswer{Clips: clips}, nil
}

func (c *compute) Clear(_ context.Context, request montage.ClearRequest, key, after, before []byte, afterCount, beforeCount int) ([2]float64, error) {
	if len(key) != 256*144 || len(after) != afterCount*256*144 || len(before) != beforeCount*256*144 {
		return [2]float64{}, errors.New("frames of the wrong size")
	}
	return [2]float64{request.Lo, request.Hi}, nil
}

// editing stands in for the FFmpeg Worker.
type editing struct {
	mu          sync.Mutex
	segments    int
	failSegment int
	onSegment   func(ctx context.Context) error
	frames      int
	badFilm     bool
}

func (e *editing) Frames(_ context.Context, _ *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error) {
	count := len(request.Times)
	if count == 0 {
		count = int((request.End-request.Start)*request.Rate) + 1
	}
	for range count {
		if err := each(make([]byte, request.Width*request.Height)); err != nil {
			return ffmpegworker.FramesResult{}, err
		}
	}
	return ffmpegworker.FramesResult{}, nil
}

func (e *editing) Segment(ctx context.Context, _ *os.File, request ffmpegworker.SegmentRequest) (io.ReadCloser, error) {
	e.mu.Lock()
	e.segments++
	hook := e.onSegment
	fail := e.failSegment > 0
	if fail {
		e.failSegment--
	}
	e.mu.Unlock()
	if fail {
		return nil, errors.New("ffmpeg worker: killed")
	}
	if hook != nil {
		if err := hook(ctx); err != nil {
			return nil, err
		}
	}
	return io.NopCloser(strings.NewReader(fmt.Sprintf("segment %d+%d;", request.Offset, request.Frames))), nil
}

func (e *editing) Assemble(_ context.Context, video, _ *os.File, request ffmpegworker.AssembleRequest) (io.ReadCloser, error) {
	if _, err := io.ReadAll(video); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.frames = request.Frames
	e.mu.Unlock()
	return io.NopCloser(strings.NewReader("film")), nil
}

func (e *editing) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	duration := float64(e.frames) / 30
	if e.badFilm {
		duration++
	}
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Duration: duration}, Streams: []ffmpegworker.ProbeStream{
		{Type: "video", Width: 1920, Height: 1080, FrameRate: 30}, {Type: "audio"},
	}}, nil
}

// planner stands in for the planner: fixed answers.
type planner struct {
	mu     sync.Mutex
	picks  int
	shots  int
	last   montage.PlanInput
	unknow bool
}

func (p *planner) Pick(_ context.Context, input montage.PlanInput) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unknow {
		return nil, montage.ErrPlanUnknown
	}
	p.picks++
	p.last = input
	return json.RawMessage(`{"parts":[{"scenes":[]}]}`), nil
}

func (p *planner) Shots(_ context.Context, input montage.PlanInput) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shots++
	return json.RawMessage(`{"answer":{"parts":[]},"slots":[]}`), nil
}

// music: four seconds of one song.
type music struct{}

func (music) Chosen(context.Context, string, string) (string, float64, error) {
	return "song:1", 12.5, nil
}

func (music) Excerpt(_ context.Context, _, songID string, start float64) (montage.Excerpt, error) {
	return montage.Excerpt{Key: fmt.Sprintf("%s@%v", songID, start), Music: json.RawMessage(`{"parts":[{"level":"主歌"}]}`), Start: start, Frames: 120}, nil
}

func (music) Audio(context.Context, string, string) (*os.File, error) { return os.Open(os.DevNull) }

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type jobEnv struct {
	store   *montage.Store
	lib     *library
	compute *compute
	editing *editing
	planner *planner
	clock   *clock
	root    string
	project montage.ProjectDetail
}

func (e *jobEnv) open(t *testing.T) {
	t.Helper()
	store, err := montage.Open(context.Background(), e.root, montage.Options{Grants: e.lib, Compute: e.compute, Editing: e.editing,
		Planner: e.planner, Music: music{}, Now: e.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetRoom(true)
	e.store = store
}

func newJobEnv(t *testing.T) *jobEnv {
	t.Helper()
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lib := newLibrary()
	lib.add("asset:a1", "user:alice", "object:1", 2)
	lib.add("asset:a2", "user:alice", "object:2", 4)
	lib.add("asset:a3", "user:alice", "object:3", 5)
	lib.add("asset:b1", "user:bob", "object:9", 3)
	env := &jobEnv{lib: lib, compute: &compute{}, editing: &editing{}, planner: &planner{}, root: root,
		clock: &clock{at: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}}
	env.open(t)
	project, _, err := env.store.CreateProject(context.Background(), "user:alice", "alice", "create", "海边", []string{"asset:a1", "asset:a2"})
	if err != nil {
		t.Fatal(err)
	}
	env.project = project
	return env
}

func (e *jobEnv) generate(t *testing.T, key string) montage.Job {
	t.Helper()
	job, created, err := e.store.Generate(context.Background(), "user:alice", e.project.ID, key)
	if err != nil || !created || job.State != montage.JobQueued {
		t.Fatalf("Generate(%s) = %+v, %v, %v", key, job, created, err)
	}
	return job
}

// settle runs the job, letting time pass while it waits to be tried again,
// until it ends or needs the member.
func (e *jobEnv) settle(t *testing.T, jobID string) montage.Job {
	t.Helper()
	for range 20 {
		if !e.store.RunOnce(context.Background()) {
			job := e.job(t, jobID)
			if job.State != montage.JobQueued {
				return job
			}
			e.clock.advance(3 * time.Minute)
		}
	}
	t.Fatalf("job %s never settled: %+v", jobID, e.job(t, jobID))
	return montage.Job{}
}

func (e *jobEnv) job(t *testing.T, jobID string) montage.Job {
	t.Helper()
	job, err := e.store.Job(context.Background(), "user:alice", jobID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (e *jobEnv) jobFiles(jobID string) bool {
	_, err := os.Stat(filepath.Join(e.root, "tmp", jobID))
	return err == nil
}

func TestAJobMakesAFilmAndSavesItOnce(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	again, created, err := env.store.Generate(ctx, "user:alice", env.project.ID, "generate:1")
	if err != nil || created || again.ID != job.ID {
		t.Fatalf("the same request again = %+v, %v, %v", again, created, err)
	}
	if _, _, err := env.store.Generate(ctx, "user:alice", env.project.ID, "generate:2"); !errors.Is(err, montage.ErrJobActive) {
		t.Fatalf("a second job while one is in progress = %v", err)
	}
	if _, err := env.store.Job(ctx, "user:bob", job.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("Bob reading Alice's job = %v", err)
	}

	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || done.Version == nil || done.Version.Number != 1 {
		t.Fatalf("job = %+v", done)
	}
	for _, st := range done.Steps {
		if st.State != "done" {
			t.Fatalf("step %s is %s", st.Name, st.State)
		}
	}
	if names := env.lib.filmNames(); env.lib.saves != 1 || !slices.Equal(names, []string{"海边 v1.mp4"}) {
		t.Fatalf("saved %d films: %v", env.lib.saves, names)
	}
	if env.editing.segments != 2 || env.editing.frames != 120 {
		t.Fatalf("rendered %d segments, %d frames", env.editing.segments, env.editing.frames)
	}
	if env.jobFiles(job.ID) {
		t.Fatal("the job's files outlived it")
	}
	notices, err := env.store.Notices(ctx, "user:alice")
	if err != nil || len(notices) != 1 || notices[0].Kind != "saved" || notices[0].FilmID != done.Version.FilmAssetID {
		t.Fatalf("notices = %+v, %v", notices, err)
	}
	if bob, _ := env.store.Notices(ctx, "user:bob"); len(bob) != 0 {
		t.Fatalf("Bob's notices = %+v", bob)
	}

	// The member deletes the film in the photo library.
	project, err := env.store.Project(ctx, "user:alice", env.project.ID)
	if err != nil || len(project.Versions) != 1 || project.Versions[0].Gone {
		t.Fatalf("versions = %+v, %v", project.Versions, err)
	}
	env.lib.set(func() { delete(env.lib.videos, done.Version.FilmAssetID) })
	if project, _ = env.store.Project(ctx, "user:alice", env.project.ID); !project.Versions[0].Gone {
		t.Fatalf("a film deleted in the photo library = %+v", project.Versions)
	}

	// The next version is the second.
	second := env.settle(t, env.generate(t, "generate:3").ID)
	if second.Version == nil || second.Version.Number != 2 || !slices.Contains(env.lib.filmNames(), "海边 v2.mp4") {
		t.Fatalf("second = %+v, films %v", second, env.lib.filmNames())
	}
}

func TestAJobWaitsForTheAnalysisAndNeedsTheMemberWhenItFails(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.states["asset:a1"] = photos.AnalysisWorking })
	job := env.generate(t, "generate:1")
	if waiting := env.settle(t, job.ID); waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitAnalysis {
		t.Fatalf("job = %+v", waiting)
	}
	env.lib.set(func() { env.lib.states["asset:a1"] = photos.AnalysisFailed })
	env.clock.advance(time.Minute)
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedAnalysis || !slices.Equal(needs.Missing, []string{"asset:a1"}) {
		t.Fatalf("job = %+v", needs)
	}
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("Retry() for a failed analysis = %v", err)
	}
	if _, err := env.store.RetryAnalysis(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env.lib.retried, []string{"asset:a1"}) {
		t.Fatalf("retried %v", env.lib.retried)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
}

func TestAKilledWorkerIsTriedThreeTimesAndNoMore(t *testing.T) {
	env := newJobEnv(t)
	env.compute.failFill = 2
	if done := env.settle(t, env.generate(t, "generate:1").ID); done.State != montage.JobDone {
		t.Fatalf("after two failures = %+v", done)
	}
	env.compute.failFill = 3
	failed := env.settle(t, env.generate(t, "generate:2").ID)
	if failed.State != montage.JobFailed || failed.Step != "fill" || failed.FinishedAt == nil {
		t.Fatalf("after three failures = %+v", failed)
	}
	if env.jobFiles(failed.ID) {
		t.Fatal("a failed job kept its files")
	}
	// The first version is still there.
	project, _ := env.store.Project(context.Background(), "user:alice", env.project.ID)
	if len(project.Versions) != 1 || project.Versions[0].Gone {
		t.Fatalf("versions = %+v", project.Versions)
	}
}

func TestATimelineThatFailsItsChecksIsNeverRendered(t *testing.T) {
	env := newJobEnv(t)
	env.compute.external = true
	failed := env.settle(t, env.generate(t, "generate:1").ID)
	if failed.State != montage.JobFailed || !strings.Contains(failed.Error, "outside the project") {
		t.Fatalf("job = %+v", failed)
	}
	if env.editing.segments != 0 || env.lib.saves != 0 || env.compute.fills != 3 {
		t.Fatalf("rendered %d segments, saved %d films, filled %d times", env.editing.segments, env.lib.saves, env.compute.fills)
	}
}

func TestAFilmThatIsNotTheTimelinesIsNotSaved(t *testing.T) {
	env := newJobEnv(t)
	env.editing.badFilm = true
	failed := env.settle(t, env.generate(t, "generate:1").ID)
	if failed.State != montage.JobFailed || failed.Step != "render" || env.lib.saves != 0 {
		t.Fatalf("job = %+v, saved %d", failed, env.lib.saves)
	}
}

func TestCancellingWhileRenderingSavesNothing(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	env.editing.onSegment = func(segmentCtx context.Context) error {
		if _, err := env.store.Cancel(ctx, "user:alice", job.ID); err != nil {
			return err
		}
		// The FFmpeg connection is closed: the segment ends.
		<-segmentCtx.Done()
		return segmentCtx.Err()
	}
	cancelled := env.settle(t, job.ID)
	if cancelled.State != montage.JobCancelled || env.lib.saves != 0 || env.editing.segments != 1 || env.jobFiles(job.ID) {
		t.Fatalf("job = %+v; saved %d, segments %d", cancelled, env.lib.saves, env.editing.segments)
	}
	if _, err := env.store.Cancel(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("cancelling again = %v", err)
	}

	// A queued job is cancelled at once.
	queued := env.generate(t, "generate:2")
	if cancelled, err := env.store.Cancel(ctx, "user:alice", queued.ID); err != nil || cancelled.State != montage.JobCancelled {
		t.Fatalf("Cancel(queued) = %+v, %v", cancelled, err)
	}
}

func TestNoRoomForTheFilmNeedsTheMemberAndARetrySavesItOnce(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.noSpace = true })
	job := env.generate(t, "generate:1")
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedStorageFull {
		t.Fatalf("job = %+v", needs)
	}
	notices, _ := env.store.Notices(ctx, "user:alice")
	if len(notices) != 1 || notices[0].Kind != "needs_input" || notices[0].Reason != montage.NeedStorageFull {
		t.Fatalf("notices = %+v", notices)
	}
	env.lib.set(func() { env.lib.noSpace = false })
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || env.lib.saves != 1 || env.editing.segments != 2 {
		t.Fatalf("job = %+v; saved %d, segments %d", done, env.lib.saves, env.editing.segments)
	}
}

func TestNoRoomToRenderNeedsTheMember(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	env.store.SetRoom(false)
	job := env.generate(t, "generate:1")
	if needs := env.settle(t, job.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedStorageFull || env.editing.segments != 0 {
		t.Fatalf("job = %+v, segments %d", needs, env.editing.segments)
	}
	env.store.SetRoom(true)
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || env.lib.saves != 1 {
		t.Fatalf("job = %+v, saved %d", done, env.lib.saves)
	}
}

func TestAFilmSavedBeforeItsVersionWasRecordedIsNotSavedAgain(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.lostSave = 1 })
	done := env.settle(t, env.generate(t, "generate:1").ID)
	if done.State != montage.JobDone || env.lib.saves != 1 || done.Version == nil || done.Version.FilmAssetID != "asset:film-1" {
		t.Fatalf("job = %+v; saved %d", done, env.lib.saves)
	}
}

func TestARetriedJobDoesNotRedoTheStepsItFinished(t *testing.T) {
	env := newJobEnv(t)
	env.editing.failSegment = 1
	done := env.settle(t, env.generate(t, "generate:1").ID)
	if done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	// Two clear spans and the answer, once; the render twice.
	if env.planner.picks != 1 || env.planner.shots != 1 || env.compute.fills != 3 || env.editing.segments != 3 {
		t.Fatalf("picked %d, shot %d, filled %d, segments %d", env.planner.picks, env.planner.shots, env.compute.fills, env.editing.segments)
	}
}

func TestARestartPutsTheRunningJobBackAndGoesOn(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	if claimed := env.store.Claim(ctx); claimed != job.ID {
		t.Fatalf("claimed %q", claimed)
	}
	if running := env.job(t, job.ID); running.State != montage.JobRunning {
		t.Fatalf("job = %+v", running)
	}
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.open(t)
	if err := env.store.RecoverJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
}

func TestDroppingAMissingVideoPlansAgainWithoutIt(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	// The second video is deleted for good while the film renders, after
	// the plan was taken.
	env.editing.onSegment = func(context.Context) error {
		env.lib.set(func() { delete(env.lib.videos, "asset:a2") })
		return nil
	}
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedMissing || !slices.Equal(needs.Missing, []string{"asset:a2"}) {
		t.Fatalf("job = %+v", needs)
	}
	env.editing.onSegment = nil
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, []string{"asset:a1", "asset:a2"}); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("dropping every video = %v", err)
	}
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, []string{"asset:a2"}); err != nil {
		t.Fatal(err)
	}
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || env.planner.picks != 2 {
		t.Fatalf("job = %+v, picked %d times", done, env.planner.picks)
	}
	for _, card := range env.planner.last.Cards {
		if card.Video != "asset:a1" {
			t.Fatalf("the new plan saw %s", card.Video)
		}
	}
	project, _ := env.store.Project(ctx, "user:alice", env.project.ID)
	if project.AssetCount != 1 {
		t.Fatalf("project = %+v", project)
	}
}

func TestAJobKeepsTheVideosAndSettingsItStartedWith(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	if _, err := env.store.AddAssets(ctx, "user:alice", "alice", env.project.ID, "add", []string{"asset:a3"}); err != nil {
		t.Fatal(err)
	}
	ask := "多一点海"
	if _, err := env.store.UpdateProject(ctx, "user:alice", env.project.ID, montage.ProjectChange{Ask: &ask}); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	if env.planner.last.Settings.Ask != "" || env.planner.last.Settings.SongStart != 12.5 || len(env.planner.last.Cards) != 4 {
		t.Fatalf("the plan saw %+v", env.planner.last)
	}
}

func TestVideosTrashedOrDeletedWhileTheJobRuns(t *testing.T) {
	env := newJobEnv(t)
	trash := func(trashed bool) {
		env.lib.set(func() {
			v := env.lib.videos["asset:a1"]
			v.trashed = trashed
			env.lib.videos["asset:a1"] = v
		})
	}
	// In the trash before the job starts.
	trash(true)
	job := env.generate(t, "generate:1")
	if waiting := env.settle(t, job.ID); waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitTrashed {
		t.Fatalf("job = %+v", waiting)
	}
	trash(false)
	env.clock.advance(10 * time.Minute)
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}

	// Into the trash while the film renders: within ten minutes the job
	// waits.
	env.store.SetHeartbeat(5 * time.Millisecond)
	env.editing.onSegment = func(ctx context.Context) error {
		trash(true)
		env.clock.advance(11 * time.Minute)
		<-ctx.Done()
		return ctx.Err()
	}
	second := env.generate(t, "generate:2")
	if waiting := env.settle(t, second.ID); waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitTrashed {
		t.Fatalf("job = %+v", waiting)
	}
	env.editing.onSegment = nil
	// Deleted for good.
	env.lib.set(func() { delete(env.lib.videos, "asset:a1") })
	env.clock.advance(10 * time.Minute)
	if needs := env.settle(t, second.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedMissing {
		t.Fatalf("job = %+v", needs)
	}
}

func TestAPlanThatNeverCameBackNeedsTheMember(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	env.planner.unknow = true
	job := env.generate(t, "generate:1")
	if needs := env.settle(t, job.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown {
		t.Fatalf("job = %+v", needs)
	}
	env.planner.unknow = false
	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
}

func TestDeletingAProjectTakesItsJobs(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	if err := env.store.DeleteProject(ctx, "user:alice", env.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Job(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("the deleted project's job = %v", err)
	}
	if env.store.RunOnce(ctx) {
		t.Fatal("a deleted project's job ran")
	}
}
