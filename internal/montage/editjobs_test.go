package montage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
)

// Saving a draft as a new version (#601): the edit job fills the draft's
// slots with their beats pinned, renders and saves the film.

// SaveFilm keeps a film once per version key, as the photo library does.
func (g *editGrants) SaveFilm(_ context.Context, grantID, versionKey, _ string, film *os.File) (string, error) {
	if grantID != "grant:1" {
		return "", errors.New("another grant")
	}
	if g.films == nil {
		g.films = map[string]string{}
	}
	if id, ok := g.films[versionKey]; ok {
		return id, nil
	}
	if _, err := io.ReadAll(film); err != nil {
		return "", err
	}
	id := fmt.Sprintf("asset:edited-%d", len(g.films)+1)
	g.films[versionKey] = id
	return id, nil
}

// Fill answers a pinned fill as the Worker would for beats that fill their
// slots: each slot one piece of its beat, from a third before its keyframe.
func (*editCompute) Fill(_ context.Context, request FillRequest, _ []float32, _ int) (FillAnswer, error) {
	if request.Pins == nil {
		return FillAnswer{}, errors.New("an edit's fill without pins")
	}
	var answer FillAnswer
	for _, pin := range request.Pins {
		id, n, _ := strings.Cut(pin.Beat, "/")
		card, ok := request.Cards[id]
		if !ok || !slices.Contains(request.Catalog, id) {
			return FillAnswer{}, fmt.Errorf("pin %s outside the catalog", pin.Beat)
		}
		beat := int(n[0] - '1')
		length := pin.Slot[1] - pin.Slot[0]
		start := math.Max(card.Start, math.Min(card.Times[beat]-length/3, card.End-length))
		raw, _ := json.Marshal(map[string]any{"card": id, "beats": []int{beat + 1}, "beat": pin.Beat, "video": card.Video,
			"start": start, "end": start + length, "slot": pin.Slot, "share": length, "part": pin.Part, "picture": pin.Picture})
		answer.Clips = append(answer.Clips, raw)
	}
	return answer, nil
}

func (e *editFrames) Segment(_ context.Context, _ *os.File, request ffmpegworker.SegmentRequest) (io.ReadCloser, error) {
	e.rendered += request.Frames
	return io.NopCloser(strings.NewReader("segment;")), nil
}

func (e *editFrames) Assemble(_ context.Context, video, _ *os.File, _ ffmpegworker.AssembleRequest) (io.ReadCloser, error) {
	if _, err := io.ReadAll(video); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("film")), nil
}

func (e *editFrames) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return ffmpegworker.Probe{Format: ffmpegworker.ProbeFormat{Duration: float64(e.rendered) / FilmRate}, Streams: []ffmpegworker.ProbeStream{
		{Type: "video", Width: FilmWidth, Height: FilmHeight, FrameRate: FilmRate}, {Type: "audio"},
	}}, nil
}

type editMusic struct{ Music }

func (editMusic) Audio(context.Context, string, string) (*os.File, error) {
	file, err := os.CreateTemp("", "song")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(file.Name())
	return file, nil
}

// runJobs runs the jobs due until none is.
func (env *editEnv) runJobs(t *testing.T) {
	t.Helper()
	env.store.jobMusic, env.store.admits = editMusic{}, func(string, int64) (bool, error) { return true, nil }
	for n := 0; env.store.RunOnce(t.Context()); n++ {
		env.frames.rendered = 0
		if n > 10 {
			t.Fatal("the jobs keep running")
		}
	}
}

func TestADraftSavedIsANewVersionOnce(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); err != nil {
		t.Fatal(err)
	}
	job, created, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil || !created || job.Kind != KindEdit || !slices.Equal(stepNamesOf(job), []string{"source", "fill", "render", "save"}) {
		t.Fatalf("SaveDraft() = %+v, %v, %v", job, created, err)
	}
	if again, created, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1"); err != nil || created || again.ID != job.ID {
		t.Fatalf("the same request again = %v, %v, %v", again.ID, created, err)
	}
	// One job at a time: saving again, generating or another save wait.
	if _, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:2"); !errors.Is(err, ErrJobActive) {
		t.Fatalf("saving while a job runs = %v", err)
	}
	env.store.Exec(t, "UPDATE projects SET song_id = 'song:x'")
	if _, _, err := env.store.Generate(ctx, editOwner, "project:1", "generate:2"); !errors.Is(err, ErrJobActive) {
		t.Fatalf("generating while an edit runs = %v", err)
	}
	if _, _, err := env.store.SaveDraft(ctx, editOther, draft.ID, "save:3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member saving the draft = %v", err)
	}
	env.runJobs(t)
	done, err := env.store.Job(ctx, editOwner, job.ID)
	if err != nil || done.State != JobDone || done.Version == nil {
		t.Fatalf("the edit job = %+v, %v", done, err)
	}
	edited := *done.Version
	if edited.Kind != "edit" || edited.SourceVersionID != env.version || edited.SourceNumber != 1 || edited.Number != 3 ||
		edited.DraftRevision == nil || *edited.DraftRevision != 1 || !edited.Editable {
		t.Fatalf("the new version = %+v", edited)
	}
	// The version it came from and its film stay as they were.
	if n := env.store.Count(t, "SELECT COUNT(*) FROM versions WHERE id = ? AND film_asset_id = 'asset:film1' AND timeline_json LIKE '%c1/1%'", env.version); n != 1 {
		t.Fatal("the version edited changed")
	}
	// The new version is edited on as any: a draft of its own, as it was saved.
	again, err := env.store.VersionDraft(ctx, editOwner, edited.ID)
	if err != nil || !slices.Equal(beats(again), []string{"c1/3", "c2/1", "c2/2", "c3/2"}) || slices.ContainsFunc(again.Shots, func(s DraftShot) bool { return s.Changed }) {
		t.Fatalf("the new version's draft = %v, %v", beats(again), err)
	}
	if list, err := env.store.VersionShots(ctx, editOwner, edited.ID); err != nil || len(readList(t, list)) != 4 {
		t.Fatalf("the new version's shot list = %v", err)
	}
	// Saved again at the same revision, the film is the same one.
	second, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:4")
	if err != nil {
		t.Fatal(err)
	}
	env.runJobs(t)
	if done, err := env.store.Job(ctx, editOwner, second.ID); err != nil || done.State != JobDone || done.Version == nil || done.Version.ID != edited.ID {
		t.Fatalf("saving the same revision again = %+v, %v", done.Version, err)
	}
	if len(env.grants.films) != 1 || env.store.Count(t, "SELECT COUNT(*) FROM versions") != 3 {
		t.Fatalf("%d films, %d versions", len(env.grants.films), env.store.Count(t, "SELECT COUNT(*) FROM versions"))
	}
}

func stepNamesOf(job Job) []string {
	var names []string
	for _, step := range job.Steps {
		names = append(names, step.Name)
	}
	return names
}

func TestAnEditWaitsForAVideoInTheTrash(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	env.grants.trashed = map[string]bool{videoB: true}
	job, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil {
		t.Fatal(err)
	}
	env.runJobs(t)
	waiting, err := env.store.Job(ctx, editOwner, job.ID)
	if err != nil || waiting.State != JobWaiting || waiting.WaitReason != WaitTrashed || !slices.Equal(waiting.Missing, []string{videoB}) {
		t.Fatalf("an edit of a video in the trash = %+v, %v", waiting, err)
	}
	env.grants.trashed = nil
	env.store.Exec(t, "UPDATE jobs SET not_before = '2000-01-01T00:00:00Z' WHERE id = ?", job.ID)
	env.runJobs(t)
	if done, err := env.store.Job(ctx, editOwner, job.ID); err != nil || done.State != JobDone {
		t.Fatalf("the edit after the video came back = %+v, %v", done, err)
	}
}

func TestAVersionKeepsTheSongItWasMadeWith(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	old := strings.Repeat("a", 64)
	path := env.store.songPath(old)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("the old song"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.store.Exec(t, `INSERT INTO songs(id, owner_user_id, sha256, title, duration, content_type, state, created_at, updated_at)
		VALUES('song:old', ?, ?, '旧歌', 200, 'audio/mpeg', 'ready', '2026-11-20T00:00:00Z', '2026-11-20T00:00:00Z')`, editOwner, old)
	env.store.Exec(t, `UPDATE jobs SET settings_json = '{"songId":"song:old","songStart":31.5}' WHERE id = 'job:1'`)
	// The project has another song now; no job runs.
	env.store.Exec(t, "UPDATE projects SET song_id = 'song:new'")
	if err := env.store.ReclaimSongs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM songs WHERE id = 'song:old'"); n != 1 {
		t.Fatal("the song a version was made with was reclaimed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("its copy: %v", err)
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := env.store.OpenDraftSongAudio(ctx, editOwner, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audio.File.Close() }()
	if heard, _ := io.ReadAll(audio.File); string(heard) != "the old song" || audio.ContentType != "audio/mpeg" {
		t.Fatalf("the draft's song = %q %q", heard, audio.ContentType)
	}
	if _, err := env.store.OpenDraftSongAudio(ctx, editOther, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's draft song = %v", err)
	}
	// The edit job takes the version's song, not the project's.
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "lock", Shot: 1}); err != nil {
		t.Fatal(err)
	}
	job, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil {
		t.Fatal(err)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM jobs WHERE id = ? AND json_extract(settings_json, '$.songId') = 'song:old'", job.ID); n != 1 {
		t.Fatal("the edit job does not take the version's song")
	}
}

func TestAnEditOfAMissingVideoNeedsTheMember(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil {
		t.Fatal(err)
	}
	// Video B's content is not the one the version was cut from.
	env.store.Exec(t, "UPDATE job_assets SET object_id = 'object:other' WHERE job_id = ? AND asset_id = ?", job.ID, videoB)
	env.runJobs(t)
	needs, err := env.store.Job(ctx, editOwner, job.ID)
	if err != nil || needs.State != JobNeedsInput || needs.WaitReason != NeedMissing || !slices.Equal(needs.Missing, []string{videoB}) {
		t.Fatalf("an edit of a missing video = %+v, %v", needs, err)
	}
	if _, err := env.store.DropAssets(ctx, editOwner, job.ID, []string{videoB}); !errors.Is(err, ErrJobState) {
		t.Fatalf("dropping an edit's video = %v", err)
	}
}

// A timeline that does not show a beat where the draft has it is not the
// draft's film: the member is told which shots to change (#609 review).
func TestAnEditsTimelineIsCutWithTheDraftsBeats(t *testing.T) {
	pins := []FillPin{
		{Part: 1, Picture: 1, Slot: [2]float64{0, 4}, Beat: "c1/1"},
		{Part: 1, Picture: 1, Slot: [2]float64{4, 8}, Beat: "c1/2"},
		{Part: 2, Picture: 1, Slot: [2]float64{8, 14}, Beat: "c2/2"},
	}
	slots := [][2]int{{0, 120}, {120, 240}, {240, 420}}
	if err := checkPinnedSlots(pins, slots, [][]string{{"c1/1"}, {"c1/2", "c3/1"}, {"c3/2", "c2/2"}}); err != nil {
		t.Fatalf("a timeline with every pin = %v", err)
	}
	err := checkPinnedSlots(pins, slots, [][]string{{"c1/1"}, {"c1/3"}, {"c3/2"}})
	var stopped *stepFailed
	if !errors.As(err, &stopped) || !strings.Contains(err.Error(), "第 2、3 镜选的节拍放不进它的位置") {
		t.Fatalf("a timeline without two pins = %v", err)
	}
	short := []json.RawMessage{json.RawMessage(`[1, 1, 4.0, 8.0, 10]`)}
	if err := shortOfPins(pins, short); !errors.As(err, &stopped) || !strings.Contains(err.Error(), "第 2 镜选的节拍不够长") {
		t.Fatalf("a slot left short = %v", err)
	}
}

// A save that finds the version's videos analysed again with another model
// stops at once and says why, rather than trying again (#611).
func TestASaveOfAVersionAnalysedAgainStops(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil {
		t.Fatal(err)
	}
	env.grants.identity = "another-model|win=2s1f"
	env.runJobs(t)
	stopped, err := env.store.Job(ctx, editOwner, job.ID)
	if err != nil || stopped.State != JobFailed || stopped.Error != AnalysisChangedReason {
		t.Fatalf("a save of a version analysed again = %+v, %v", stopped, err)
	}
	if env.frames.rendered != 0 || len(env.grants.films) != 0 {
		t.Fatalf("it rendered %d frames and saved %d films", env.frames.rendered, len(env.grants.films))
	}
}
