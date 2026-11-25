package montage_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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

func (l *library) grantedVideo(grantID, assetID string) (video, error) {
	g, ok := l.grants[grantID]
	if !ok || g.revoked {
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
	v, err := l.grantedVideo(grantID, assetID)
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
		return nil
	}
	l.asked = append(l.asked, ids...)
	for _, id := range ids {
		// Only asking queues what a new grant left out.
		if state, ok := l.states[id]; ok && state == photos.AnalysisNone {
			l.states[id] = photos.AnalysisQueued
		}
	}
	return nil
}

func (l *library) Analysis(_ context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.grantedVideo(grantID, assetID)
	if err != nil {
		return photos.GrantAnalysis{}, nil, err
	}
	done := photos.AnalysisDone
	analysis := photos.GrantAnalysis{Dimensions: 768, ObjectID: v.object, Windows: done, Identity: "model|win", Cards: done, Quality: done, Described: done,
		CardList: []photos.CardAnalysis{
			{Index: 0, StartMs: 0, EndMs: 4000, Times: []float64{1, 3}, Quality: &photos.CardQuality{}},
			{Index: 1, StartMs: 4000, EndMs: 10000, Times: []float64{5, 8}, Quality: &photos.CardQuality{}},
		},
		Descriptions: []photos.CardDescription{
			{Card: 0, Description: json.RawMessage(`{"beats":[{"frames":[1,1],"shot_size":"远景","what":"海"},{"frames":[2,2],"shot_size":"全景","what":"船"}]}`)},
			{Card: 1, Description: json.RawMessage(`{"beats":[{"frames":[1,1],"shot_size":"近景","what":"人"},{"frames":[2,2],"shot_size":"特写","what":"手"}]}`)},
		}}
	if l.described != nil {
		l.described(assetID, &analysis)
	}
	vectors := make([]byte, 5*768*4)
	for i := range 5 {
		analysis.List = append(analysis.List, photos.Window{Index: i, StartMs: 2000 * i, EndMs: 2000*i + 2000})
		binary.LittleEndian.PutUint32(vectors[(i*768+i)*4:], math.Float32bits(1))
	}
	return analysis, vectors, nil
}

func (l *library) SaveFilm(_ context.Context, grantID, versionKey, name string, film *os.File) (string, error) {
	l.mu.Lock()
	arrived := l.saving
	l.mu.Unlock()
	if arrived != nil {
		arrived()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.grants[grantID]
	if !ok || g.revoked {
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
	refuse   bool
	external bool
	fills    int
	// clears are the frames after and before the key frame of each span.
	clears [][2]int
	// briefs counts the briefs built, by step; turned are the outlines the
	// second step's answers were turned back with.
	briefs map[string]int
	turned []string
	// onBrief, when set, runs as a brief is being built, outside the lock:
	// a test holds the Worker there.
	onBrief func(step string)
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
	if c.refuse {
		return montage.FillAnswer{}, fmt.Errorf("%w: the arrays are larger than the Worker takes", montage.ErrWorkerRefused)
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

// Likeness is how many of the dimensions both beats' first windows share.
func (c *compute) Likeness(_ context.Context, a, b []float32, dimensions int) (float64, error) {
	if dimensions <= 0 || len(a) < dimensions || len(b) < dimensions {
		return 0, fmt.Errorf("%w: no windows", montage.ErrWorkerRefused)
	}
	var dot float64
	for d := range dimensions {
		dot += float64(a[d] * b[d])
	}
	return dot, nil
}

// PlanBrief briefs either step with a schema that wants a non-empty list of
// parts, each a name of at most the catalog's card ids. As the Worker's
// plan_brief, it reads each card's beats by their number among the card's
// times, and the second step's from the outline taken.
func (c *compute) PlanBrief(_ context.Context, request montage.PlanBriefRequest) (montage.PlanBrief, error) {
	c.mu.Lock()
	if c.briefs == nil {
		c.briefs = map[string]int{}
	}
	c.briefs[request.Step]++
	hold := c.onBrief
	c.mu.Unlock()
	if hold != nil {
		hold(request.Step)
	}
	ids := make([]string, len(request.Cards))
	for i, card := range request.Cards {
		ids[i] = card.ID
		var answer struct {
			Beats []json.RawMessage `json:"beats"`
		}
		if err := json.Unmarshal(card.Answer, &answer); err != nil || len(answer.Beats) == 0 || len(answer.Beats) != len(card.Times) {
			return montage.PlanBrief{}, fmt.Errorf("%w: card %s has %d beats for %d times", montage.ErrWorkerRefused, card.ID, len(answer.Beats), len(card.Times))
		}
	}
	if request.Step == "shots" && !outlined(request.Picked) {
		return montage.PlanBrief{}, fmt.Errorf("%w: TypeError: 'NoneType' object is not subscriptable", montage.ErrWorkerRefused)
	}
	enum, _ := json.Marshal(ids)
	schema := `{"type":"object","required":["parts"],"properties":{"parts":{"type":"array","minItems":1,"items":{"type":"object",
		"required":["name","cards"],"properties":{"name":{"type":"string"},"cards":{"type":"array","items":{"enum":` + string(enum) + `}}}}}}}`
	brief := montage.PlanBrief{Data: json.RawMessage(`{"step":"` + request.Step + `"}`), Schema: json.RawMessage(schema), Template: "plan-v2"}
	if request.Step == "shots" {
		brief.Slots, brief.Pictures = json.RawMessage(`[[[[0,2],[2,4]]]]`), []int{1}
	}
	return brief, nil
}

// outlined reports whether picked is an outline with parts, which the
// Worker's shots_request and shots_answer subscript.
func outlined(picked json.RawMessage) bool {
	var outline struct {
		Parts []json.RawMessage `json:"parts"`
	}
	return json.Unmarshal(picked, &outline) == nil && len(outline.Parts) > 0
}

func (c *compute) ShotsAnswer(_ context.Context, answer, picked json.RawMessage, pictures []int) (json.RawMessage, error) {
	if !outlined(picked) || len(pictures) == 0 {
		return nil, fmt.Errorf("%w: TypeError: 'NoneType' object is not subscriptable", montage.ErrWorkerRefused)
	}
	c.mu.Lock()
	c.turned = append(c.turned, string(picked))
	c.mu.Unlock()
	return answer, nil
}

func (c *compute) Clear(_ context.Context, request montage.ClearRequest, key, after, before []byte, afterCount, beforeCount int) ([2]float64, error) {
	if len(key) != 256*144 || len(after) != afterCount*256*144 || len(before) != beforeCount*256*144 {
		return [2]float64{}, errors.New("frames of the wrong size")
	}
	c.mu.Lock()
	c.clears = append(c.clears, [2]int{afterCount, beforeCount})
	c.mu.Unlock()
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
	// ranges are the ranges of frames asked for: start, end and rate.
	ranges [][3]float64
}

func (e *editing) Frames(_ context.Context, _ *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error) {
	count := len(request.Times)
	if count == 0 {
		// A range is half-open: the instants Start + k/Rate before End.
		count = int(math.Ceil((request.End - request.Start) * request.Rate))
		e.mu.Lock()
		e.ranges = append(e.ranges, [3]float64{request.Start, request.End, request.Rate})
		e.mu.Unlock()
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
	return json.RawMessage(`{"answer":{"parts":[]},"slots":[[[[0,2],[2,4]]]]}`), nil
}

// music: four seconds of one song.
type music struct{}

func (music) Chosen(context.Context, string, string) (string, float64, error) {
	return "song:1", 12.5, nil
}

func (music) ExcerptKey(_ context.Context, _, songID string, start float64) (string, error) {
	return fmt.Sprintf("%s@%v", songID, start), nil
}

func (music) Excerpt(_ context.Context, _, songID string, start float64) (montage.Excerpt, error) {
	return montage.Excerpt{Key: fmt.Sprintf("%s@%v", songID, start), Music: json.RawMessage(`{"parts":[{"level":"主歌"}]}`), Start: start, Frames: 120}, nil
}

func (music) Audio(context.Context, string, string) (*os.File, error) { return os.Open(os.DevNull) }

type jobEnv struct {
	store   *montage.Store
	lib     *library
	compute *compute
	editing *editing
	planner *planner
	clock   *clock
	root    string
	project montage.ProjectDetail
	// markers, when set, has the member's VNAS plan, leaving markers here.
	markers string
	// plans is how many 方案 each job makes: one unless a test asks for more.
	plans int
}

func (e *jobEnv) open(t *testing.T) {
	t.Helper()
	options := montage.Options{Grants: e.lib, JobCompute: e.compute, Editing: e.editing, Now: e.clock.now, Plans: max(e.plans, 1)}
	if e.markers != "" {
		options.PlanMarkers = e.markers
	} else {
		options.Planner = e.planner
	}
	store, err := montage.Open(context.Background(), e.root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetRoom(true)
	store.UseMusic(music{})
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
			waitingForPhotos := job.State == montage.JobWaiting && job.WaitReason == montage.WaitPhotos
			if job.State != montage.JobQueued && !waitingForPhotos {
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
	// The project's page picks the job up again after a reload.
	if detail, err := env.store.Project(ctx, "user:alice", env.project.ID); err != nil || detail.Job == nil || detail.Job.ID != job.ID {
		t.Fatalf("project = %+v, %v", detail.Job, err)
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
	// The version keeps the timeline it was made from, for editing it (#596).
	if n := env.store.Count(t, "SELECT COUNT(*) FROM versions WHERE job_id = ? AND json_array_length(timeline_json, '$.clips') = 2", job.ID); n != 1 {
		t.Fatalf("%d versions keep their timeline", n)
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
	if again, err := env.store.RetryAnalysis(ctx, "user:alice", job.ID); err != nil || len(again.Missing) != 0 {
		t.Fatalf("RetryAnalysis() = %+v, %v", again, err)
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
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, []string{"asset:a1"}); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("dropping a usable video = %v", err)
	}
	env.lib.setDown(true)
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, []string{"asset:a2"}); !errors.Is(err, montage.ErrPhotosUnavailable) {
		t.Fatalf("dropping while the grant service is unavailable = %v", err)
	}
	if got := env.job(t, job.ID); got.State != montage.JobNeedsInput || !slices.Equal(got.Missing, []string{"asset:a2"}) {
		t.Fatalf("after failed removal = %+v", got)
	}
	env.lib.setDown(false)
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

// Generating reads the project's song in the job's own transaction: the
// database has one connection, so a second one would wait forever.
func TestGeneratingWithTheStoresOwnSongsDoesNotWaitOnItself(t *testing.T) {
	env := newJobEnv(t)
	env.store.UseMusic(env.store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := env.store.Generate(ctx, "user:alice", env.project.ID, "generate:1"); !errors.Is(err, montage.ErrNoSong) {
		t.Fatalf("Generate without a selected song = %v", err)
	}
}

func TestCancellationSurvivesARestartAndReleasesTheProject(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	job := env.generate(t, "first")
	if env.store.Claim(ctx) != job.ID {
		t.Fatal("job was not claimed")
	}
	if _, err := env.store.Cancel(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.open(t)
	if err := env.store.RecoverJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.job(t, job.ID); got.State != montage.JobCancelled || got.FinishedAt == nil || env.store.RunOnce(ctx) {
		t.Fatalf("recovered job = %+v", got)
	}
	env.generate(t, "next")
}

func TestANewStepHasItsOwnRetryBudget(t *testing.T) {
	env := newJobEnv(t)
	env.compute.failFill = 2
	env.editing.failSegment = 1
	job := env.generate(t, "first")
	if done := env.settle(t, job.ID); done.State != montage.JobDone || env.editing.segments != 3 {
		t.Fatalf("job = %+v; %d segment attempts", done, env.editing.segments)
	}
}

func TestCancelAfterRunningOutOfSpaceRemovesTheRenderedFilm(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	env.lib.noSpace = true
	job := env.generate(t, "first")
	if got := env.settle(t, job.ID); got.WaitReason != montage.NeedStorageFull || !env.jobFiles(job.ID) {
		t.Fatalf("expected a rendered film waiting for space, got %+v", got)
	}
	got, err := env.store.Cancel(ctx, "user:alice", job.ID)
	if err != nil || got.State != montage.JobCancelled || env.jobFiles(job.ID) {
		t.Fatalf("after cancellation = %+v, %v; files present = %v", got, err, env.jobFiles(job.ID))
	}
}

func TestAGenerateKeyCannotReferToAnotherProject(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	env.generate(t, "first")
	project, _, err := env.store.CreateProject(ctx, "user:alice", "alice", "other", "", []string{"asset:a1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.Generate(ctx, "user:alice", project.ID, "first"); !errors.Is(err, montage.ErrConflict) {
		t.Fatalf("key reused on a different project: %v", err)
	}
}

// ownSongs reopens the store with its own songs, decoded by a stand-in FFmpeg
// worker, and returns how to give the project one.
func (e *jobEnv) ownSongs(t *testing.T) func(name string) montage.SongDetail {
	t.Helper()
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := montage.Open(t.Context(), e.root, montage.Options{Grants: e.lib, Media: &songMedia{}, JobCompute: e.compute, Editing: e.editing, Planner: e.planner})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.store = store
	return func(name string) montage.SongDetail {
		t.Helper()
		song, err := store.PutSong(t.Context(), "user:alice", e.project.ID, montage.SongSource{Title: name},
			strings.NewReader("song 200 "+name))
		if err != nil {
			t.Fatal(err)
		}
		return song
	}
}

func TestReplacingASongKeepsTheActiveJobsCopyUntilCancellation(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	put := env.ownSongs(t)
	store := env.store
	first := put("original")
	job := env.generate(t, "first")
	put("replacement")
	if err := store.ReclaimSongs(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := store.Audio(ctx, "user:alice", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAllFrom(f); got != "song 200 original" {
		t.Fatalf("job holds %q", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Audio(ctx, "user:alice", first.ID); !errors.Is(err, montage.ErrSongUnusable) {
		t.Fatalf("old song after cancellation = %v", err)
	}
}

func TestFullProjectStillShowsTheSavedFilmAndItsVideos(t *testing.T) {
	env := newJobEnv(t)
	job := env.generate(t, "generate")
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	ids := make([]string, montage.MaxAssets-len(env.project.Assets))
	for i := range ids {
		ids[i] = fmt.Sprintf("asset:extra-%d", i)
		env.lib.add(ids[i], "user:alice", fmt.Sprintf("object:extra-%d", i), 1)
	}
	project, err := env.store.AddAssets(t.Context(), "user:alice", "alice", env.project.ID, "full-project", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Assets) != montage.MaxAssets || len(project.Versions) != 1 || project.Versions[0].Gone {
		t.Fatalf("project: %d videos, versions %+v", len(project.Assets), project.Versions)
	}
	for _, asset := range project.Assets {
		if asset.Status != montage.AssetAvailable {
			t.Errorf("%s: %s", asset.AssetID, asset.Status)
		}
	}
}

// A crash between a cancellation and its settling, or a release that left
// one so, has a job queued with its cancellation asked for: nothing claims
// it, so it must not count as due either.
func TestACancelledJobLeftQueuedDoesNotKeepTheSchedulerBusy(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	job := env.generate(t, "first")
	env.store.Exec(t, "UPDATE jobs SET cancel_requested = 1 WHERE id = ?", job.ID)
	if env.store.RunOnce(ctx) {
		t.Fatal("a job asked to cancel ran")
	}
	if wait := env.store.NextDue(ctx); wait < time.Minute {
		t.Fatalf("the scheduler would look again in %v", wait)
	}
	// Nor does the member put one back in the queue.
	env.store.Exec(t, "UPDATE jobs SET state = 'needs_input', wait_reason = 'storage_full' WHERE id = ?", job.ID)
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("going on with a job asked to cancel = %v", err)
	}
	if err := env.store.RecoverJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.job(t, job.ID); got.State != montage.JobCancelled || got.FinishedAt == nil {
		t.Fatalf("recovered job = %+v", got)
	}
	env.generate(t, "next")
}

func TestAJobWhoseSettingsCannotBeReadFailsAndLetsTheNextOneRun(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	job := env.generate(t, "first")
	env.store.Exec(t, "UPDATE jobs SET settings_json = '{' WHERE id = ?", job.ID)
	if env.store.RunOnce(ctx) {
		t.Fatal("a job without settings ran")
	}
	if got := env.job(t, job.ID); got.State != montage.JobFailed || got.FinishedAt == nil {
		t.Fatalf("job = %+v", got)
	}
	if wait := env.store.NextDue(ctx); wait < time.Minute {
		t.Fatalf("the scheduler would look again in %v", wait)
	}
	if done := env.settle(t, env.generate(t, "next").ID); done.State != montage.JobDone {
		t.Fatalf("the next job = %+v", done)
	}
}

// The same request would be refused again: the job fails at once, without
// reading every analysis and vector three times.
func TestARequestTheComputeWorkerRefusesIsNotTriedAgain(t *testing.T) {
	env := newJobEnv(t)
	env.compute.refuse = true
	failed := env.settle(t, env.generate(t, "first").ID)
	if failed.State != montage.JobFailed || failed.Step != "fill" || env.compute.fills != 1 {
		t.Fatalf("job = %+v after %d fills", failed, env.compute.fills)
	}
}

// Once the film went to the photo library the job cannot be cancelled: the
// library may already have it, so the job goes on to record the version.
func TestCancellingOnceTheFilmWentToThePhotoLibraryIsRefused(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	job := env.generate(t, "first")
	var refusal error
	asked := 0
	env.lib.set(func() {
		env.lib.saving = func() {
			asked++
			_, refusal = env.store.Cancel(ctx, "user:alice", job.ID)
		}
	})
	done := env.settle(t, job.ID)
	if asked != 1 || !errors.Is(refusal, montage.ErrJobState) {
		t.Fatalf("Cancel() while saving, asked %d times = %v", asked, refusal)
	}
	if done.State != montage.JobDone || done.Version == nil || env.lib.saves != 1 || env.jobFiles(job.ID) {
		t.Fatalf("job = %+v; saved %d", done, env.lib.saves)
	}
}

// A video longer than the photo library analyses fails its windows for
// good: the member is told which videos, and takes them out.
func TestVideosTooLongToAnalyseAreNamedAndDropped(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	if _, err := env.store.AddAssets(ctx, "user:alice", "alice", env.project.ID, "add", []string{"asset:a3"}); err != nil {
		t.Fatal(err)
	}
	env.lib.set(func() {
		for _, id := range []string{"asset:a1", "asset:a3"} {
			v := env.lib.videos[id]
			v.seconds = 7 * 3600
			env.lib.videos[id] = v
			env.lib.states[id] = photos.AnalysisFailed
		}
	})
	job := env.generate(t, "first")
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedTooLong || !slices.Equal(needs.Missing, []string{"asset:a1", "asset:a3"}) {
		t.Fatalf("job = %+v", needs)
	}
	if _, err := env.store.RetryAnalysis(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("RetryAnalysis() of videos too long = %v", err)
	}
	if len(env.lib.retried) != 0 {
		t.Fatalf("asked the photo library to analyse %v again", env.lib.retried)
	}
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, needs.Missing); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || len(done.Missing) != 0 {
		t.Fatalf("job = %+v", done)
	}
	// A failure of a video of ordinary length is still one to try again.
	env.lib.set(func() { env.lib.states["asset:a2"] = photos.AnalysisFailed })
	if failed := env.settle(t, env.generate(t, "second").ID); failed.WaitReason != montage.NeedAnalysis {
		t.Fatalf("job = %+v", failed)
	}
}

// A grant registered again leaves the analysis the revocation dropped
// unqueued until it is asked for; the job asks.
func TestAJobAsksForTheAnalysisANewGrantLeftOut(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.states["asset:a2"] = photos.AnalysisNone })
	job := env.generate(t, "first")
	if waiting := env.settle(t, job.ID); waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitAnalysis {
		t.Fatalf("job = %+v", waiting)
	}
	env.lib.set(func() {
		if !slices.Equal(env.lib.asked, []string{"asset:a2"}) || env.lib.states["asset:a2"] != photos.AnalysisQueued {
			t.Errorf("asked for %v, asset:a2 is %q", env.lib.asked, env.lib.states["asset:a2"])
		}
		delete(env.lib.states, "asset:a2")
	})
	env.clock.advance(time.Minute)
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
}

// The photo library describes a card of many key frames from some of them:
// a beat is placed at the key frame its "frames" name, not at the key frame
// of its own number.
func TestABeatIsPlacedAtTheKeyFrameItsDescriptionNames(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() {
		env.lib.described = func(assetID string, analysis *photos.GrantAnalysis) {
			if assetID != "asset:a1" {
				return
			}
			// Five key frames, three of them described; and a card whose
			// description names a key frame it does not have.
			analysis.CardList[0].Times = []float64{0.5, 1, 2, 3, 3.5}
			analysis.Descriptions[0].Description = json.RawMessage(`{"beats":[{"frames":[1,2],"shot_size":"远景","what":"海"},
				{"frames":[3,3],"shot_size":"全景","what":"船"},{"frames":[5,5],"shot_size":"近景","what":"岸"}]}`)
			analysis.Descriptions[1].Description = json.RawMessage(`{"beats":[{"frames":[1,1],"shot_size":"近景","what":"人"},{"frames":[3,3],"shot_size":"特写","what":"手"}]}`)
		}
	})
	if done := env.settle(t, env.generate(t, "first").ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	cards := map[string]montage.Card{}
	for _, card := range env.planner.last.Cards {
		cards[card.ID] = card
	}
	sampled, ok := cards["v1-0"]
	if !ok || !slices.Equal(sampled.Times, []float64{0.5, 2, 3.5}) || !slices.Equal(sampled.Frames, []string{"v1-0/1", "v1-0/2", "v1-0/3"}) {
		t.Fatalf("the card described from some of its key frames = %+v", sampled)
	}
	if _, ok := cards["v1-1"]; ok || len(cards) != 3 {
		t.Fatalf("the plan saw %v", slices.Sorted(maps.Keys(cards)))
	}
	if whole := cards["v2-0"]; !slices.Equal(whole.Times, []float64{1, 3}) || !slices.Equal(whole.Frames, []string{"v2-0/1", "v2-0/2"}) {
		t.Fatalf("a card described from every key frame = %+v", whole)
	}
}

// The clear span is searched in the frames from the key frame up to the
// span's end and from its start up to the key frame, both half-open ranges
// of the FFmpeg Worker: the first frame after is the key frame's instant,
// and the frames before stop short of it.
func TestTheClearSpanReadsTheFramesEitherSideOfTheKeyFrame(t *testing.T) {
	env := newJobEnv(t)
	if done := env.settle(t, env.generate(t, "first").ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	// Each video's first card: key frame at 1 second of a card from 0 to 4.
	want := [][3]float64{{1, 4, 2}, {0, 1, 2}, {1, 4, 2}, {0, 1, 2}}
	if !slices.Equal(env.editing.ranges, want) {
		t.Fatalf("ranges = %v, want %v", env.editing.ranges, want)
	}
	if !slices.Equal(env.compute.clears, [][2]int{{6, 2}, {6, 2}}) {
		t.Fatalf("frames after and before = %v", env.compute.clears)
	}
}

// The plan is cut from the cards, their quality and their descriptions: the
// same videos analysed by another version of any of them are another input,
// and what was planned from the old one is planned again.
func TestAnotherAnalysisIdentityPlansAgain(t *testing.T) {
	for name, change := range map[string]func(*photos.GrantAnalysis){
		"cards":        func(analysis *photos.GrantAnalysis) { analysis.CardsIdentity = "cards/v2" },
		"quality":      func(analysis *photos.GrantAnalysis) { analysis.QualityIdentity = "quality/v2" },
		"descriptions": func(analysis *photos.GrantAnalysis) { analysis.DescriptionsIdentity = "reader:2|filter=2" },
	} {
		t.Run(name, func(t *testing.T) {
			env := newJobEnv(t)
			env.lib.set(func() { env.lib.noSpace = true })
			job := env.generate(t, "first")
			if needs := env.settle(t, job.ID); needs.WaitReason != montage.NeedStorageFull || env.planner.picks != 1 || env.planner.shots != 1 {
				t.Fatalf("job = %+v, picks = %d, shots = %d", needs, env.planner.picks, env.planner.shots)
			}
			env.lib.set(func() {
				env.lib.noSpace = false
				env.lib.described = func(_ string, analysis *photos.GrantAnalysis) { change(analysis) }
			})
			if _, err := env.store.Retry(t.Context(), "user:alice", job.ID); err != nil {
				t.Fatal(err)
			}
			if done := env.settle(t, job.ID); done.State != montage.JobDone || env.planner.picks != 2 || env.planner.shots != 2 {
				t.Fatalf("job = %+v, picks = %d, shots = %d", done, env.planner.picks, env.planner.shots)
			}
		})
	}
}

// Videos without a card are analysed, not failed: with none in any of them
// the job says there is nothing to cut from.
func TestVideosWithoutCardsLeaveNothingToCutFrom(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() {
		env.lib.described = func(_ string, analysis *photos.GrantAnalysis) { analysis.CardList, analysis.Descriptions = nil, nil }
	})
	needs := env.settle(t, env.generate(t, "first").ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedNoFootage || needs.Step != "catalog" || env.planner.picks != 0 {
		t.Fatalf("job = %+v, picks = %d", needs, env.planner.picks)
	}
}

// An analysis that goes stale between the lookup and the read, as when the
// photo library makes the windows again for a new model, is waited for
// again: the library leaves out what is no longer current, and planning
// from what is left would miss the video's cards (#584).
func TestAnalysisInvalidatedAfterLookupWaits(t *testing.T) {
	for name, stale := range map[string]func(*photos.GrantAnalysis){
		"windows": func(analysis *photos.GrantAnalysis) {
			analysis.Windows, analysis.Identity = photos.AnalysisQueued, ""
		},
		"cards": func(analysis *photos.GrantAnalysis) {
			analysis.Cards, analysis.CardsIdentity, analysis.CardList = photos.AnalysisQueued, "", nil
		},
		"quality": func(analysis *photos.GrantAnalysis) {
			analysis.Quality, analysis.QualityIdentity = photos.AnalysisQueued, ""
		},
		"descriptions": func(analysis *photos.GrantAnalysis) {
			analysis.Described, analysis.DescriptionsIdentity, analysis.Descriptions = photos.AnalysisQueued, "", nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newJobEnv(t)
			env.lib.set(func() {
				env.lib.described = func(_ string, analysis *photos.GrantAnalysis) { stale(analysis) }
			})
			job := env.generate(t, "first")
			waiting := env.settle(t, job.ID)
			if waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitAnalysis || waiting.Step == "catalog" || env.planner.picks != 0 {
				t.Fatalf("job = %+v, picks = %d", waiting, env.planner.picks)
			}
			env.lib.set(func() { env.lib.described = nil })
			env.clock.advance(time.Minute)
			if done := env.settle(t, job.ID); done.State != montage.JobDone {
				t.Fatalf("job = %+v", done)
			}
		})
	}
}

// A deleted account's running job is stopped, and its intermediate files go
// with its jobs.
func TestRemovingAnAccountStopsItsRunningJobAndTakesItsFiles(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	job := env.generate(t, "first")
	stopped := false
	env.editing.onSegment = func(segmentCtx context.Context) error {
		if !env.jobFiles(job.ID) {
			return errors.New("rendering without the job's directory")
		}
		if err := env.store.RemoveAccount(ctx, "user:alice"); err != nil {
			return err
		}
		select {
		case <-segmentCtx.Done():
			stopped = true
		case <-time.After(10 * time.Second):
		}
		return context.Cause(segmentCtx)
	}
	if !env.store.RunOnce(ctx) {
		t.Fatal("the job did not run")
	}
	if !stopped || env.editing.segments != 1 || env.lib.saves != 0 || env.jobFiles(job.ID) {
		t.Fatalf("stopped %v, %d segments, %d films saved, files left %v", stopped, env.editing.segments, env.lib.saves, env.jobFiles(job.ID))
	}
	if _, err := env.store.Job(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("the removed account's job = %v", err)
	}
}

func TestAJobMakesTwoPlansToChooseFrom(t *testing.T) {
	ctx := context.Background()
	env := newJobEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.plans = 2
	env.open(t)
	done := env.settle(t, env.generate(t, "generate:1").ID)
	if done.State != montage.JobDone || len(done.Versions) != 2 || done.Version == nil || done.Version.Plan != 1 {
		t.Fatalf("job = %+v", done)
	}
	for i, version := range done.Versions {
		if version.Plan != i+1 || version.Number != i+1 || version.JobID != done.ID {
			t.Fatalf("version %d = %+v", i, version)
		}
	}
	var names []string
	for _, st := range done.Steps {
		names = append(names, st.Name)
		if st.State != "done" {
			t.Fatalf("step %s is %s", st.Name, st.State)
		}
	}
	if !slices.Equal(names[3:], []string{"pick", "shots", "fill", "render", "save", "pick-2", "shots-2", "fill-2", "render-2", "save-2"}) {
		t.Fatalf("steps = %v", names)
	}
	if films := env.lib.filmNames(); env.lib.saves != 2 || !slices.Equal(films, []string{"海边 v1.mp4", "海边 v2.mp4"}) {
		t.Fatalf("saved %d films: %v", env.lib.saves, films)
	}
	if env.jobFiles(done.ID) {
		t.Fatal("the job's files outlived it")
	}
	project, err := env.store.Project(ctx, "user:alice", env.project.ID)
	if err != nil || len(project.Versions) != 2 || project.Versions[0].Plan != 2 {
		t.Fatalf("versions = %+v, %v", project.Versions, err)
	}
	// One notice for the job, not one per 方案.
	if notices, err := env.store.Notices(ctx, "user:alice"); err != nil || len(notices) != 1 {
		t.Fatalf("notices = %+v, %v", notices, err)
	}
}

func TestTheProjectListShowsAFewOfEachProjectsVideos(t *testing.T) {
	env := newJobEnv(t)
	page, err := env.store.Projects(context.Background(), "user:alice", "")
	if err != nil || len(page.Items) != 1 || !slices.Equal(page.Items[0].PreviewAssetIDs, []string{"asset:a1", "asset:a2"}) {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestTheHeroFilmIsTheDevicesOwnWhenItHasOne(t *testing.T) {
	env := newJobEnv(t)
	if _, err := env.store.OpenHero("film"); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("without a hero = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(env.root, "hero"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.root, "hero", "hero.mp4"), []byte("film"), 0o640); err != nil {
		t.Fatal(err)
	}
	hero, err := env.store.OpenHero("film")
	if err != nil || hero.ContentType != "video/mp4" || hero.Info.Size() != 4 {
		t.Fatalf("hero = %+v, %v", hero, err)
	}
	_ = hero.File.Close()
	if _, err := env.store.OpenHero("poster"); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("a poster the device does not have = %v", err)
	}
	if _, err := env.store.OpenHero("../montage.db"); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("another file = %v", err)
	}
}

// A job makes as many 方案 as the store made when it started: one from
// before the store made two goes on, and ends, with its one, and shows the
// steps of one.
func TestAJobKeepsTheNumberOfPlansItStartedWith(t *testing.T) {
	env := newJobEnv(t)
	job := env.generate(t, "generate:1")
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.plans = 2
	env.open(t)
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || len(done.Steps) != 8 || len(done.Versions) != 1 || env.lib.saves != 1 {
		t.Fatalf("the job from before = %+v, %d films saved", done, env.lib.saves)
	}
	for _, st := range done.Steps {
		if st.State != "done" {
			t.Fatalf("step %s of a finished job is %s", st.Name, st.State)
		}
	}
	next := env.settle(t, env.generate(t, "generate:2").ID)
	if next.State != montage.JobDone || len(next.Steps) != 13 || len(next.Versions) != 2 {
		t.Fatalf("a job started now = %+v", next)
	}
	// The contract names the steps of two 方案: a store is not opened for more.
	if _, err := montage.Open(context.Background(), t.TempDir(), montage.Options{Grants: env.lib, Plans: montage.MaxPlans + 1}); err == nil {
		t.Fatal("a store for three 方案 a job was opened")
	}
}
