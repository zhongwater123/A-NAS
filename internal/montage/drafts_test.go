package montage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// The edit of a film after it was made (#596): a project with one job whose
// first 方案 was saved as a version. Its music has two parts, the first of
// one picture over two slots, the second of two pictures of a slot each:
//
//	slot 1 [0,4]   part 1 picture 1   c1/1
//	slot 2 [4,8]   part 1 picture 1   c2/1
//	slot 3 [8,11]  part 2 picture 1   c2/2
//	slot 4 [11,14] part 2 picture 2   c3/2
//
// Video A is 16:9, video B is 竖. c4 is a card of one second.

const (
	editOwner = "user:a"
	editOther = "user:b"
	videoA    = "asset:a"
	videoB    = "asset:b"
)

type editGrants struct {
	Grants
	// windows are each video's windows, each a one-hot vector of 4
	// dimensions on the given axis.
	windows map[string][]int
	opened  []string
	// analysed are the videos whose analysis was read, in order.
	analysed []string
	// identity is the windows' model now: the versions were cut with "".
	identity string
	// trashed are the videos in the trash; films the films saved, by
	// version key.
	trashed map[string]bool
	films   map[string]string
	// gone are the videos the photo library no longer has.
	gone map[string]bool
}

func (g *editGrants) Analysis(_ context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error) {
	if grantID != "grant:1" {
		return photos.GrantAnalysis{}, nil, errors.New("another grant")
	}
	g.analysed = append(g.analysed, assetID)
	if g.gone[assetID] {
		return photos.GrantAnalysis{}, nil, &photosapi.GrantError{Code: photosapi.GrantAssetNotFound, Message: "the asset is not available"}
	}
	var list []photos.Window
	var raw []byte
	for i, axis := range g.windows[assetID] {
		list = append(list, photos.Window{Index: i, StartMs: 2000 * i, EndMs: 2000 * (i + 1)})
		for d := range 4 {
			value := float32(0)
			if d == axis {
				value = 1
			}
			raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(value))
		}
	}
	return photos.GrantAnalysis{ObjectID: "object:" + assetID, Identity: g.identity, Dimensions: 4, List: list}, raw, nil
}

func (g *editGrants) Lookup(_ context.Context, _ string, ids []string) ([]photos.GrantedAsset, error) {
	var found []photos.GrantedAsset
	for _, id := range ids {
		status := photos.GrantAvailable
		if g.trashed[id] {
			status = photos.GrantTrashed
		}
		found = append(found, photos.GrantedAsset{AssetID: id, ObjectID: "object:" + id, Status: status})
	}
	return found, nil
}

func (g *editGrants) Revoke(context.Context, string) error { return nil }

func (g *editGrants) Open(_ context.Context, _, assetID string) (*os.File, string, error) {
	g.opened = append(g.opened, assetID)
	if g.trashed[assetID] {
		// As the photo library answers for a video deleted or in the trash.
		return nil, "", &photosapi.GrantError{Code: photosapi.GrantAssetNotFound, Message: "the asset is not available"}
	}
	file, err := os.CreateTemp("", "video")
	if err != nil {
		return nil, "", err
	}
	_ = os.Remove(file.Name())
	return file, "object:" + assetID, nil
}

// editCompute's likeness is the dot product of the first windows; it
// keeps the briefs asked for, and briefs a recut for plan-v2 when stale.
type editCompute struct {
	Compute
	briefed []PlanBriefRequest
	stale   bool
}

func (*editCompute) Likeness(_ context.Context, a, b []float32, dimensions int) (float64, error) {
	var dot float64
	for d := range dimensions {
		dot += float64(a[d] * b[d])
	}
	return dot, nil
}

// editSearch finds, for any text, the windows listed in hits.
type editSearch struct {
	hits  []photos.WindowMatch
	texts [][]string
	err   error
}

func (s *editSearch) SearchWindows(_ context.Context, grantID string, texts []string, _ int) ([][]photos.WindowMatch, error) {
	if grantID != "grant:1" {
		return nil, errors.New("another grant")
	}
	s.texts = append(s.texts, texts)
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]photos.WindowMatch, len(texts))
	for i := range texts {
		out[i] = s.hits
	}
	return out, nil
}

type editFrames struct {
	Editing
	asked []ffmpegworker.FramesRequest
	// rendered counts the frames of the film being rendered.
	rendered int
}

func (e *editFrames) Frames(_ context.Context, _ *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error) {
	e.asked = append(e.asked, request)
	return ffmpegworker.FramesResult{Times: request.Times}, each([]byte("\xff\xd8jpeg"))
}

func card(id, video string, start, end float64, times []float64, scene string, sizes ...string) Card {
	var beats []map[string]any
	for n := range times {
		beats = append(beats, map[string]any{"shot_size": sizes[n], "what": id + " 的第 " + string(rune('1'+n)) + " 拍", "frames": []int{n + 1}})
	}
	answer, _ := json.Marshal(map[string]any{"scene": scene, "beats": beats})
	frames := make([]string, len(times))
	return Card{ID: id, Video: video, Start: start, End: end, Times: times, Frames: frames, Answer: answer}
}

func clipJSON(beat, video string, start, end float64, slot [2]float64, part, picture int) json.RawMessage {
	id, n, _ := strings.Cut(beat, "/")
	raw, _ := json.Marshal(map[string]any{"card": id, "beats": []int{int(n[0] - '0')}, "beat": beat, "video": video, "start": start, "end": end,
		"slot": slot, "share": slot[1] - slot[0], "part": part, "picture": picture, "partName": "", "level": "主歌", "query": "q"})
	return raw
}

type editEnv struct {
	store   *Store
	grants  *editGrants
	search  *editSearch
	frames  *editFrames
	version string
}

func newEditEnv(t *testing.T) *editEnv {
	t.Helper()
	ctx := t.Context()
	env := &editEnv{grants: &editGrants{windows: map[string][]int{videoA: make([]int, 20), videoB: make([]int, 6)}},
		search: &editSearch{}, frames: &editFrames{}}
	// Video A's windows 7–9 (c1/3) and 15–19 (c2/2) look alike; 10–14
	// (c2/1) like neither; the rest of A and all of B like none of these.
	for i := 7; i < 20; i++ {
		env.grants.windows[videoA][i] = 2
	}
	for i := 10; i < 15; i++ {
		env.grants.windows[videoA][i] = 1
	}
	store, err := Open(ctx, t.TempDir(), Options{Grants: env.grants, JobCompute: &editCompute{}, Search: env.search, Editing: env.frames, Planner: FixedPlanner{},
		Now: func() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	env.store = store
	cards := map[string]Card{
		"c1": card("c1", videoA, 0, 20, []float64{2, 8, 14}, "海边", "远景", "全景", "近景"),
		"c2": card("c2", videoA, 20, 40, []float64{22, 30}, "海边", "全景", "中景"),
		"c3": card("c3", videoB, 0, 10, []float64{1, 5}, "山路", "近景", "特写"),
		"c4": card("c4", videoB, 10, 11, []float64{10.5}, "山路", "全景"),
		// Described but left out of the catalog (blurred): never cut from.
		"c5": card("c5", videoA, 0, 20, []float64{10}, "海边", "全景"),
	}
	taken := time.Date(2026, 8, 23, 13, 38, 50, 0, time.UTC)
	analysis, _ := json.Marshal(analysisResult{Videos: []analysedVideo{
		{AssetID: videoA, ObjectID: "object:" + videoA, Dimensions: 4, Width: 1920, Height: 1080, TakenAt: &taken, Windows: windowsOf(20)},
		{AssetID: videoB, ObjectID: "object:" + videoB, Dimensions: 4, Width: 1080, Height: 1920, Windows: windowsOf(6)},
	}})
	catalog, _ := json.Marshal(catalogResult{Cards: cards, Catalog: []string{"c1", "c2", "c3", "c4"}})
	music := `{"seconds":14,"parts":[{"start":0,"end":8,"level":"主歌","slots":[[0,4],[4,8]]},{"start":8,"end":14,"level":"高潮","slots":[[8,11],[11,14]]}]}`
	song, _ := json.Marshal(Excerpt{Key: "song", Music: json.RawMessage(music), Start: 31.5, Frames: 420})
	pick := `{"design":"从海边走到山里","opening":"海","ending":"山","unmet":["直升机"," 雪地 "],"parts":[{"name":"海边","summary":"走","scenes":[{"query":"海边散步","cards":["c1","c2"]}]},
		{"name":"山里","summary":"爬","scenes":[{"query":"海浪","cards":["c2"]},{"query":"山路","cards":["c3","c4"]}]}]}`
	shots := `{"answer":{"unmet":["雪地","日落"],"parts":[{"name":"海边","pictures":[{"beats":["c1/1","c2/1","c1/3"],"shows":"","link":""}]},
		{"name":"山里","pictures":[{"beats":["c2/2"],"shows":"","link":""},{"beats":["c3/2","c3/1"],"shows":"","link":""}]}]}}`
	fill, _ := json.Marshal(timelineResult{Clips: []json.RawMessage{
		clipJSON("c1/1", videoA, 0.7, 4.7, [2]float64{0, 4}, 1, 1),
		clipJSON("c2/1", videoA, 21, 25, [2]float64{4, 8}, 1, 1),
		clipJSON("c2/2", videoA, 29, 32, [2]float64{8, 11}, 2, 1),
		clipJSON("c3/2", videoB, 4, 7, [2]float64{11, 14}, 2, 2),
	}})
	at := "2026-11-20T08:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id, owner_user_id, name, grant_id, state, created_at, updated_at) VALUES('project:1', ?, '海边', 'grant:1', 'ready', ?, ?)`, []any{editOwner, at, at}},
		{`INSERT INTO batches(id, owner_user_id, project_id, request_key, kind, asset_ids, state, created_at) VALUES('batch:1', ?, 'project:1', 'k', 'create', '[]', 'done', ?)`, []any{editOwner, at}},
		{`INSERT INTO project_assets(project_id, asset_id, object_id, batch_id, added_at) VALUES('project:1', ?, ?, 'batch:1', ?), ('project:1', ?, ?, 'batch:1', ?)`,
			[]any{videoA, "object:" + videoA, at, videoB, "object:" + videoB, at}},
		{`INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, state, not_before, created_at, updated_at, plans)
			VALUES('job:1', ?, 'project:1', 'k', '{}', 'done', ?, ?, ?, 2)`, []any{editOwner, at, at, at}},
		{`INSERT INTO job_assets(job_id, asset_id, object_id) VALUES('job:1', ?, ?), ('job:1', ?, ?)`,
			[]any{videoA, "object:" + videoA, videoB, "object:" + videoB}},
	} {
		store.Exec(t, statement.query, statement.args...)
	}
	for name, result := range map[string]string{"analysis": string(analysis), "catalog": string(catalog), "song": string(song),
		"pick": pick, "shots": shots, "fill": string(fill), "pick-2": pick, "shots-2": shots} {
		store.Exec(t, `INSERT INTO steps(job_id, name, input_key, state, result_json, started_at, finished_at) VALUES('job:1', ?, 'key', 'done', ?, ?, ?)`,
			name, result, at, at)
	}
	store.Exec(t, `INSERT INTO versions(id, project_id, number, job_id, plan, version_key, film_asset_id, created_at, timeline_json)
		VALUES('version:1', 'project:1', 1, 'job:1', 1, 'v1', 'asset:film1', ?, ?), ('version:2', 'project:1', 2, 'job:1', 2, 'v2', 'asset:film2', ?, NULL)`,
		at, string(fill), at)
	env.version = "version:1"
	return env
}

func windowsOf(n int) []photos.Window {
	var list []photos.Window
	for i := range n {
		list = append(list, photos.Window{Index: i, StartMs: 2000 * i, EndMs: 2000 * (i + 1)})
	}
	return list
}

func beats(draft Draft) []string {
	var out []string
	for _, shot := range draft.Shots {
		out = append(out, shot.Beat)
	}
	return out
}

func refusedWith(t *testing.T, err error, reason string) {
	t.Helper()
	var refusal *EditRefusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, reason) {
		t.Fatalf("got %v, want a refusal saying %q", err, reason)
	}
}

func TestADraftStartsAsItsVersionAndIsTheMembersAlone(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beats(draft), []string{"c1/1", "c2/1", "c2/2", "c3/2"}) || draft.Revision != 0 || draft.CanUndo {
		t.Fatalf("a new draft = %v revision %d undo %v", beats(draft), draft.Revision, draft.CanUndo)
	}
	last := draft.Shots[3]
	if last.Start != 11 || last.End != 14 || last.Query != "山路" || !last.Portrait || last.Changed || last.From != 4 || last.To != 7 {
		t.Fatalf("the last shot = %+v", last)
	}
	if draft.Counts != (DraftCounts{Shots: 4, Videos: 2, Portrait: 1}) || draft.SongStart != 31.5 || draft.Plan.Design != "从海边走到山里" || len(draft.Parts) != 2 {
		t.Fatalf("the draft = %+v", draft)
	}
	again, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil || again.ID != draft.ID {
		t.Fatalf("the draft again = %v, %v", again.ID, err)
	}
	// Another member reaches neither the version nor the draft.
	if _, err := env.store.VersionDraft(ctx, editOther, env.version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's VersionDraft = %v", err)
	}
	if _, err := env.store.EditDraft(ctx, editOther, draft.ID, 0, DraftOp{Type: "lock", Shot: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's EditDraft = %v", err)
	}
	if _, err := env.store.DraftCandidates(ctx, editOther, draft.ID, CandidateQuery{Shot: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's DraftCandidates = %v", err)
	}
	if _, err := env.store.DraftShots(ctx, editOther, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's DraftShots = %v", err)
	}
	if _, err := env.store.VersionShots(ctx, editOther, env.version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's VersionShots = %v", err)
	}
	if _, err := env.store.Thumbnail(ctx, editOther, "project:1", videoA, 1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's Thumbnail = %v", err)
	}
	if _, err := env.store.ConfirmVersion(ctx, editOther, env.version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's ConfirmVersion = %v", err)
	}
	// Deleting the project takes the draft with it.
	if err := env.store.DeleteProject(ctx, editOwner, "project:1"); err != nil {
		t.Fatal(err)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM drafts") + env.store.Count(t, "SELECT COUNT(*) FROM draft_revisions"); n != 0 {
		t.Fatalf("%d draft rows outlive their project", n)
	}
}

func TestAVersionFromBeforeTakesItsTimelineFromItsJob(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	detail, err := env.store.Project(ctx, editOwner, "project:1")
	if err != nil {
		t.Fatal(err)
	}
	editable := func() []bool {
		t.Helper()
		detail, err := env.store.Project(ctx, editOwner, "project:1")
		if err != nil {
			t.Fatal(err)
		}
		return []bool{detail.Versions[0].Editable, detail.Versions[1].Editable}
	}
	if len(detail.Versions) != 2 || detail.Versions[0].ID != "version:2" || !slices.Equal(editable(), []bool{false, true}) {
		t.Fatalf("versions = %+v", detail.Versions)
	}
	// Version 2 has no timeline of its own, and its job's fill-2 step is gone.
	if _, err := env.store.VersionDraft(ctx, editOwner, "version:2"); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("a version whose fill is gone = %v", err)
	}
	// Version 1 without its timeline finds it in the fill step, and keeps it.
	env.store.Exec(t, "UPDATE versions SET timeline_json = NULL WHERE id = 'version:1'")
	if !slices.Equal(editable(), []bool{false, true}) {
		t.Fatal("a version whose job has its fill is not editable")
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, "version:1")
	if err != nil || len(draft.Shots) != 4 {
		t.Fatalf("a version from before = %d shots, %v", len(draft.Shots), err)
	}
	env.store.Exec(t, "DELETE FROM steps WHERE name = 'fill'")
	if !slices.Equal(editable(), []bool{false, true}) {
		t.Fatal("the version did not keep the timeline it was given")
	}
	env.store.Exec(t, "UPDATE versions SET timeline_json = NULL WHERE id = 'version:1'")
	if !slices.Equal(editable(), []bool{false, false}) {
		t.Fatal("a version with no timeline anywhere is editable")
	}
}

func TestEachStepFollowsTheRules(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(op DraftOp) (Draft, error) {
		t.Helper()
		next, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, op)
		if err == nil {
			draft = next
		}
		return next, err
	}
	refusals := []struct {
		op     DraftOp
		reason string
	}{
		{DraftOp{Type: "replace", Shot: 1, Beat: "c2/2"}, "已用在第 3 镜"},
		{DraftOp{Type: "replace", Shot: 1, Beat: "c4/1"}, "不够第 1 镜"},
		{DraftOp{Type: "replace", Shot: 1, Beat: "c5/1"}, "不在这一版能用的素材里"},
		{DraftOp{Type: "move", Shot: 2, To: 3}, "同一个段落"},
		{DraftOp{Type: "revert", Shot: 1}, "没有改过"},
	}
	for _, refusal := range refusals {
		_, err := edit(refusal.op)
		refusedWith(t, err, refusal.reason)
	}
	if _, err := edit(DraftOp{Type: "replace", Shot: 1, Beat: "c9/1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a beat of no card = %v", err)
	}
	if draft.Revision != 0 {
		t.Fatalf("refused steps moved the draft to revision %d", draft.Revision)
	}
	// Replace, and the shot is changed; a portrait beat may be chosen by hand.
	if _, err := edit(DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); err != nil {
		t.Fatal(err)
	}
	if !draft.Shots[0].Changed || draft.Message != "第 1 镜已替换" || !draft.CanUndo {
		t.Fatalf("after replacing = %+v, %q", draft.Shots[0], draft.Message)
	}
	// Move: a locked shot stays.
	if _, err := edit(DraftOp{Type: "lock", Shot: 2}); err != nil {
		t.Fatal(err)
	}
	_, err = edit(DraftOp{Type: "move", Shot: 1, To: 2})
	refusedWith(t, err, "锁定的镜头不动")
	if _, err := edit(DraftOp{Type: "unlock", Shot: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := edit(DraftOp{Type: "move", Shot: 1, To: 2}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beats(draft), []string{"c2/1", "c1/3", "c2/2", "c3/2"}) {
		t.Fatalf("after moving = %v", beats(draft))
	}
	// Delete: shot 2 (c1/3, 4–8 s) could go to c2/1 before it or c2/2
	// after it, both long enough; c2/2 looks like it.
	if _, err := edit(DraftOp{Type: "delete", Shot: 2}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beats(draft), []string{"c2/1", "c2/2", "c3/2"}) || !slices.Equal(draft.Shots[1].Slots, []int{2, 3}) ||
		!draft.Shots[1].Extended || draft.Message != "已删除，由后一镜延长补上" {
		t.Fatalf("after deleting = %v %v %q", beats(draft), draft.Shots[1].Slots, draft.Message)
	}
	// Shot 2 (c2/2, 4–11 s) now: both sides cover it and look like it
	// alike, so the one before takes it.
	if _, err := edit(DraftOp{Type: "delete", Shot: 2}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beats(draft), []string{"c2/1", "c3/2"}) || !slices.Equal(draft.Shots[0].Slots, []int{1, 2, 3}) {
		t.Fatalf("after deleting again = %v %v", beats(draft), draft.Shots[0].Slots)
	}
	// Neither c3/2 nor c2/1 covers the whole film: shot 1 cannot go; and
	// the last shot never can.
	_, err = edit(DraftOp{Type: "delete", Shot: 1})
	refusedWith(t, err, "都不够长")
	if _, err := edit(DraftOp{Type: "reset"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beats(draft), []string{"c1/1", "c2/1", "c2/2", "c3/2"}) {
		t.Fatalf("after resetting = %v", beats(draft))
	}
	// Revert puts back the version's beat.
	if _, err := edit(DraftOp{Type: "replace", Shot: 4, Beat: "c3/1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := edit(DraftOp{Type: "revert", Shot: 4}); err != nil || beats(draft)[3] != "c3/2" {
		t.Fatalf("after reverting = %v, %v", beats(draft), err)
	}
	// A shot that took the slots of the one before it is reverted to its
	// own beat, not to the deleted shot's: c2/1 takes shot 1's slot, is
	// replaced, and comes back.
	if _, err := edit(DraftOp{Type: "delete", Shot: 1}); err != nil || !slices.Equal(beats(draft), []string{"c2/1", "c2/2", "c3/2"}) ||
		draft.Shots[0].Changed {
		t.Fatalf("after deleting the first shot = %v %+v, %v", beats(draft), draft.Shots[0], err)
	}
	if _, err := edit(DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); err != nil || !draft.Shots[0].Changed {
		t.Fatalf("after replacing the longer shot = %+v, %v", draft.Shots[0], err)
	}
	if _, err := edit(DraftOp{Type: "revert", Shot: 1}); err != nil || beats(draft)[0] != "c2/1" || draft.Shots[0].Changed {
		t.Fatalf("after reverting the longer shot = %v %+v, %v", beats(draft), draft.Shots[0], err)
	}
}

func TestTheLastShotCannotBeDeleted(t *testing.T) {
	src := &editSource{}
	state := draftState{Slots: []draftSlot{{Start: 0, End: 4}}, Shots: []draftShot{{Beat: "c1/1"}}}
	_, _, err := (&Store{}).deleteShot(t.Context(), src, state, 1)
	refusedWith(t, err, "至少要留一个镜头")
}

func TestTwoWindowsEditingAtOnce(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "lock", Shot: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("a step on an old revision = %v", err)
	}
	now, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if now.Revision != 1 || !now.Shots[0].Locked || now.Shots[0].Beat != "c1/1" {
		t.Fatalf("the draft after the conflict = revision %d %+v", now.Revision, now.Shots[0])
	}
}

func TestUndoGoesBackAHundredStepsAtMost(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	step := func(op DraftOp) {
		t.Helper()
		if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, op); err != nil {
			t.Fatal(err)
		}
	}
	// Undo after undo goes on back, not forward again.
	step(DraftOp{Type: "replace", Shot: 1, Beat: "c1/2"})
	step(DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"})
	step(DraftOp{Type: "undo"})
	if beats(draft)[0] != "c1/2" || draft.Message != "已撤销" {
		t.Fatalf("one undo = %v", beats(draft))
	}
	step(DraftOp{Type: "undo"})
	if beats(draft)[0] != "c1/1" || draft.CanUndo {
		t.Fatalf("two undos = %v, can undo %v", beats(draft), draft.CanUndo)
	}
	_, err = env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "undo"})
	refusedWith(t, err, "没有可以撤销")
	for n := range 101 {
		if n%2 == 0 {
			step(DraftOp{Type: "lock", Shot: 2})
		} else {
			step(DraftOp{Type: "unlock", Shot: 2})
		}
	}
	// The step and the hundred before it.
	if kept := env.store.Count(t, "SELECT COUNT(*) FROM draft_revisions WHERE draft_id = ?", draft.ID); kept != keptRevisions+1 {
		t.Fatalf("%d revisions kept", kept)
	}
	undone := 0
	for draft.CanUndo {
		step(DraftOp{Type: "undo"})
		undone++
	}
	if undone != keptRevisions {
		t.Fatalf("undid %d steps", undone)
	}
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "undo"}); err == nil {
		t.Fatal("undid a step older than the last hundred")
	}
}

func TestAShuffleWithTheSameSeedPicksTheSame(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	// Every window of both videos is found, the later ones better.
	for i := range 20 {
		env.search.hits = append(env.search.hits, photos.WindowMatch{AssetID: videoA, Window: i, Score: float64(i)})
	}
	for i := range 6 {
		env.search.hits = append(env.search.hits, photos.WindowMatch{AssetID: videoB, Window: i, Score: float64(i) + 0.5})
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "lock", Shot: 4})
	if err != nil {
		t.Fatal(err)
	}
	seed := int64(7)
	shuffled := map[int][]string{}
	for round := range 2 {
		next, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "shuffle", Seed: &seed})
		if err != nil {
			t.Fatal(err)
		}
		shuffled[round] = beats(next)
		if !strings.HasSuffix(next.Message, "锁住的 1 个没动") {
			t.Fatalf("message %q", next.Message)
		}
		if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, next.Revision, DraftOp{Type: "undo"}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(shuffled[0], shuffled[1]) {
		t.Fatalf("the same seed shuffled %v and %v", shuffled[0], shuffled[1])
	}
	got := shuffled[0]
	if got[3] != "c3/2" {
		t.Fatalf("the locked shot moved: %v", got)
	}
	seen := map[string]bool{}
	for i, beat := range got {
		if seen[beat] {
			t.Fatalf("beat %s twice: %v", beat, got)
		}
		seen[beat] = true
		if i < 3 && (cardOf(beat) == "c3" || cardOf(beat) == "c4") {
			t.Fatalf("a 16:9 shot became 竖: %v", got)
		}
		if i > 0 && cardOf(beat) == cardOf(got[i-1]) {
			t.Fatalf("two shots of one card in a row: %v", got)
		}
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM draft_revisions WHERE draft_id = ? AND op = 'shuffle' AND seed = 7", draft.ID); n != 2 {
		t.Fatalf("%d shuffles kept their seed", n)
	}
	// The texts searched for are the unlocked pictures' descriptions.
	if !slices.Equal(env.search.texts[0], []string{"海边散步", "海浪"}) {
		t.Fatalf("searched for %v", env.search.texts[0])
	}
	env.search.err = ErrSearchModel
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "shuffle"}); !errors.Is(err, ErrSearchModel) {
		t.Fatalf("a shuffle with another model = %v", err)
	}
}

func TestCandidatesForAShot(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	list, err := env.store.DraftCandidates(ctx, editOwner, draft.ID, CandidateQuery{Shot: 1})
	if err != nil {
		t.Fatal(err)
	}
	names := func(candidates []Candidate) []string {
		var out []string
		for _, c := range candidates {
			out = append(out, c.Beat)
		}
		return out
	}
	if !slices.Equal(names(list.SameCard), []string{"c1/2", "c1/3"}) || !slices.Equal(names(list.Planned), []string{"c2/1"}) ||
		!slices.Equal(names(list.Results), []string{"c2/2", "c3/1", "c3/2", "c4/1"}) || list.Query != "海边散步" {
		t.Fatalf("candidates = %v %v %v %q", names(list.SameCard), names(list.Planned), names(list.Results), list.Query)
	}
	if list.Planned[0].UsedBy != 2 || list.Results[3].Fits || !list.Results[1].Portrait || list.SameCard[0].TakenAt == nil {
		t.Fatalf("candidates = %+v", list)
	}
	if !slices.Equal(list.Scenes, []SceneCount{{"山路", 2}, {"海边", 2}}) {
		t.Fatalf("scenes = %v", list.Scenes)
	}
	filtered, err := env.store.DraftCandidates(ctx, editOwner, draft.ID, CandidateQuery{Shot: 1, Scene: "山路", Landscape: true})
	if err != nil || len(filtered.Results) != 0 {
		t.Fatalf("landscape 山路 = %v, %v", names(filtered.Results), err)
	}
	env.search.hits = []photos.WindowMatch{{AssetID: videoB, Window: 0, Score: 0.9}, {AssetID: videoA, Window: 15, Score: 0.4}}
	found, err := env.store.DraftCandidates(ctx, editOwner, draft.ID, CandidateQuery{Shot: 1, Q: "山"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(found.Results), []string{"c3/1", "c2/2"}) || *found.Results[0].Score != 0.9 {
		t.Fatalf("found = %v", names(found.Results))
	}
	env.store.search = nil
	if _, err := env.store.DraftCandidates(ctx, editOwner, draft.ID, CandidateQuery{Shot: 1, Q: "山"}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("searching without the photo library's search = %v", err)
	}
}

func TestConfirmingAVersionDeletesNoFilm(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	detail, err := env.store.ConfirmVersion(ctx, editOwner, "version:1")
	if err != nil || detail.ChosenVersionID != "version:1" || detail.ChosenFilmAssetID != "asset:film1" {
		t.Fatalf("confirmed = %q %q, %v", detail.ChosenVersionID, detail.ChosenFilmAssetID, err)
	}
	if detail, err = env.store.ConfirmVersion(ctx, editOwner, "version:2"); err != nil || detail.ChosenVersionID != "version:2" {
		t.Fatalf("confirmed again = %q, %v", detail.ChosenVersionID, err)
	}
	if len(detail.Versions) != 2 || env.store.Count(t, "SELECT COUNT(*) FROM versions") != 2 {
		t.Fatalf("versions after confirming = %+v", detail.Versions)
	}
	page, err := env.store.Projects(ctx, editOwner, "")
	if err != nil || page.Items[0].ChosenFilmAssetID != "asset:film2" {
		t.Fatalf("the list = %+v, %v", page.Items, err)
	}
}

func readList(t *testing.T, list []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(list, []byte("\xEF\xBB\xBF")) {
		t.Fatal("the shot list does not start with a byte order mark")
	}
	rows, err := csv.NewReader(bytes.NewReader(list[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rows[0], shotListHeader) {
		t.Fatalf("header %v", rows[0])
	}
	return rows[1:]
}

func TestTheShotListOpensInASpreadsheet(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	list, err := env.store.VersionShots(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	rows := readList(t, list)
	if len(rows) != 4 || !slices.Equal(rows[0], []string{"1", "1", "0.00", "4.00", "海边", videoA, "2026-08-23 13:38:50", "0.70", "4.70", "1", "否", ""}) {
		t.Fatalf("the version's rows = %q", rows)
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "delete", Shot: 2}); err != nil {
		t.Fatal(err)
	}
	if list, err = env.store.DraftShots(ctx, editOwner, draft.ID); err != nil {
		t.Fatal(err)
	}
	rows = readList(t, list)
	if len(rows) != 3 || rows[0][1] != "1–2" || rows[0][11] != "第 2 槽的镜头已删除，由这一镜延长补上" || rows[2][10] != "否" {
		t.Fatalf("the draft's rows = %q", rows)
	}
}

func TestAThumbnailIsAFrameOfTheProjectsVideo(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	jpeg, err := env.store.Thumbnail(ctx, editOwner, "project:1", videoA, 1500)
	if err != nil || !bytes.HasPrefix(jpeg, []byte("\xff\xd8")) {
		t.Fatalf("Thumbnail() = %q, %v", jpeg, err)
	}
	asked := env.frames.asked[0]
	if !slices.Equal(asked.Times, []float64{1.5}) || asked.LongEdge != thumbnailEdge || asked.Format != ffmpegworker.FormatJPEG {
		t.Fatalf("asked for %+v", asked)
	}
	if _, err := env.store.Thumbnail(ctx, editOwner, "project:1", "asset:film1", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a video not in the project = %v", err)
	}
	// A video deleted or in the trash has no frame: not found, rather than
	// a video that cannot be added.
	env.grants.trashed = map[string]bool{videoA: true}
	if _, err := env.store.Thumbnail(ctx, editOwner, "project:1", videoA, 1500); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrAssetsRefused) {
		t.Fatalf("a frame of a video in the trash = %v", err)
	}
}

// Once the photo library analysed a version's videos again with another
// model, the version can only be played or recut: its draft does not open,
// also when the draft was made before.
func TestAVersionAnalysedAgainWithAnotherModelIsNotEdited(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	if _, err := env.store.VersionDraft(ctx, editOwner, env.version); err != nil {
		t.Fatal(err)
	}
	env.grants.identity = "another-model|win=2s1f"
	if _, err := env.store.VersionDraft(ctx, editOwner, env.version); !errors.Is(err, ErrAnalysisChanged) {
		t.Fatalf("the draft of a version analysed again = %v", err)
	}
	env.grants.identity = ""
	if _, err := env.store.VersionDraft(ctx, editOwner, env.version); err != nil {
		t.Fatalf("the draft with the windows it was cut with = %v", err)
	}
}

// The fill's rows and a beat's likeness read the same vectors: for each
// window the analysis lists, its dimensions as little-endian float32.
func TestWindowVectorsAreReadAsTheLibraryKeepsThem(t *testing.T) {
	analysis := photos.GrantAnalysis{Dimensions: 2, List: windowsOf(3)}
	var raw []byte
	for _, value := range []float32{1, -2, 0.5, 4, 3e-9, float32(math.Inf(1))} {
		raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(value))
	}
	windows, vectors, err := windowVectors(analysis, raw, func(start, end float64) bool { return start < 6 && end > 2 })
	if err != nil || len(windows) != 2 || windows[0].Index != 1 || windows[1].Index != 2 ||
		!slices.Equal(vectors, []float32{0.5, 4, 3e-9, float32(math.Inf(1))}) {
		t.Fatalf("windowVectors() = %v, %v, %v", windows, vectors, err)
	}
	if _, vectors, err := windowVectors(analysis, raw, func(float64, float64) bool { return false }); err != nil || len(vectors) != 0 {
		t.Fatalf("no window kept = %v, %v", vectors, err)
	}
	for name, wrong := range map[string]photos.GrantAnalysis{
		"a vector short":         {Dimensions: 2, List: windowsOf(4)},
		"another dimension":      {Dimensions: 3, List: windowsOf(3)},
		"no dimension":           {Dimensions: 0, List: windowsOf(3)},
		"more than a vector has": {Dimensions: photos.MaxWindowDimensions + 1, List: windowsOf(3)},
	} {
		if _, _, err := windowVectors(wrong, raw, func(float64, float64) bool { return true }); err == nil {
			t.Errorf("%s: read as vectors", name)
		}
	}
}

// A beat is written "card/n" and read back as it was written, wherever it
// is made or taken apart.
func TestABeatIsReadAsItIsWritten(t *testing.T) {
	for _, c := range []struct {
		card string
		n    int
	}{{"v1-3", 1}, {"c2", 12}} {
		id := beatID(c.card, c.n)
		if card, n, ok := splitBeat(id); !ok || card != c.card || n != c.n || cardOf(id) != c.card {
			t.Errorf("%s read as %q %d %v, card %q", id, card, n, ok, cardOf(id))
		}
	}
	if beatID("v1-3", 1) != "v1-3/1" {
		t.Errorf("beatID() = %q", beatID("v1-3", 1))
	}
	for _, text := range []string{"", "c1", "c1/", "c1/0", "c1/-1", "c1/01", "c1/+1", "c1/x", "c1/1 "} {
		if card, n, ok := splitBeat(text); ok {
			t.Errorf("%q read as beat %d of %q", text, n, card)
		}
	}
}

// A step of a 方案 has one name, whether Go or SQL writes it: the versions'
// query finds a version's fill step by the name planStep gave it.
func TestPlanStepsAreNamedAlikeInSQL(t *testing.T) {
	env := newEditEnv(t)
	for plan := range 4 {
		var name string
		if err := env.store.db.QueryRowContext(t.Context(), "SELECT "+planStepSQL("fill", "p")+" FROM (SELECT ? AS p)", plan).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != planStep("fill", plan) {
			t.Errorf("方案 %d: SQL names its fill %q, Go %q", plan, name, planStep("fill", plan))
		}
	}
}

// Deleting a shot whose neighbours both cover its slots asks which looks
// more like it. An analysis comes with every window's vectors of its video,
// so each video is read once for the step, not once a beat compared.
func TestDeletingAShotReadsEachVideosAnalysisOnce(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	// Shot 2 (c2/1) lies between c1/1 and c2/2, all three of video A.
	env.grants.analysed = nil
	next, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "delete", Shot: 2})
	if err != nil || len(next.Shots) != 3 {
		t.Fatalf("deleting shot 2 = %v, %v", beats(next), err)
	}
	if !slices.Equal(env.grants.analysed, []string{videoA}) {
		t.Fatalf("the analyses read to delete a shot = %v", env.grants.analysed)
	}
}

// A shot whose video, or a neighbour's, the photo library no longer has is
// not deleted, and the member is told why: not that a video cannot be added
// to a project.
func TestDeletingAShotBesideAVideoGoneIsRefusedWithWhy(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	// Shot 2 (c2/1) lies between c1/1 and c2/2, all three of video A.
	env.grants.gone = map[string]bool{videoA: true}
	_, err = env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "delete", Shot: 2})
	refusedWith(t, err, "已从相册删除或放进回收站")
	if errors.Is(err, ErrAssetsRefused) {
		t.Fatalf("deleting beside a video gone = %v, a video that cannot be added", err)
	}
}

// A shot whose picture the plan described with nothing to search for stays
// as it is in a shuffle, and the others are shuffled: the plan wrote it,
// not the member. With nothing left to search for, the shuffle is refused.
func TestAShuffleLeavesAShotItCannotSearchFor(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	for i := range 20 {
		env.search.hits = append(env.search.hits, photos.WindowMatch{AssetID: videoA, Window: i, Score: float64(i)})
	}
	env.store.Exec(t, `UPDATE steps SET result_json = replace(result_json, '"query":"海浪"', '"query":"  "') WHERE job_id = 'job:1' AND name = 'pick'`)
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	before := beats(draft)
	seed := int64(7)
	next, err := env.store.EditDraft(ctx, editOwner, draft.ID, draft.Revision, DraftOp{Type: "shuffle", Seed: &seed})
	if err != nil {
		t.Fatalf("a shuffle with a picture of no description = %v", err)
	}
	// Shot 3 (c2/2) is the picture described with nothing.
	if got := beats(next); got[2] != before[2] {
		t.Fatalf("the shot with nothing to search for moved: %v, was %v", got, before)
	}
	if !strings.Contains(next.Message, "1 个在规划里没有可以搜索的画面描述，保持原样") {
		t.Fatalf("message %q", next.Message)
	}
	for _, texts := range env.search.texts {
		if slices.ContainsFunc(texts, func(text string) bool { return strings.TrimSpace(text) == "" }) {
			t.Fatalf("searched for %q", texts)
		}
	}

	// Every unlocked shot without a description to search for: refused.
	env.store.Exec(t, `UPDATE steps SET result_json = replace(replace(result_json, '"query":"海边散步"', '"query":""'), '"query":"山路"', '"query":""') WHERE job_id = 'job:1' AND name = 'pick'`)
	_, err = env.store.EditDraft(ctx, editOwner, draft.ID, next.Revision, DraftOp{Type: "shuffle", Seed: &seed})
	refusedWith(t, err, "都没有可以搜索的画面描述")
}
