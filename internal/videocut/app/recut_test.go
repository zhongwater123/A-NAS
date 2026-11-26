package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Recutting a version from a sentence (#602).

// PlanBrief briefs as the compute Worker does: a recut's with plan-v3.
func (c *editCompute) PlanBrief(_ context.Context, request videocutcompute.PlanBriefRequest) (videocutcompute.PlanBrief, error) {
	c.briefed = append(c.briefed, request)
	template := "plan-v2"
	if request.Change != "" && !c.stale {
		template = "plan-v3"
	}
	return videocutcompute.PlanBrief{Data: json.RawMessage(`{}`), Schema: json.RawMessage(`{}`), Template: template}, nil
}

func TestARecutPlansAgainFromTheVersionsPlan(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	for _, change := range []string{"", "  ", strings.Repeat("长", MaxAskRunes+1)} {
		if _, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:0", change); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("recutting with %q = %v", change, err)
		}
	}
	if _, _, err := env.store.RecutVersion(ctx, editOther, env.version, "recut:0", "结尾留长一点"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member recutting = %v", err)
	}
	job, created, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点")
	if err != nil || !created || job.Kind != KindRecut || !slices.Equal(stepNamesOf(job), env.store.stepNames(KindGenerate, 1)) {
		t.Fatalf("RecutVersion() = %+v, %v, %v", job, created, err)
	}
	if again, created, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点"); err != nil || created || again.ID != job.ID {
		t.Fatalf("the same request again = %v, %v, %v", again.ID, created, err)
	}
	if _, _, err := env.store.RecutVersion(ctx, editOwner, "version:2", "recut:1", "结尾留长一点"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("the key of another recut = %v", err)
	}
	if _, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:2", "开头短一点"); !errors.Is(err, domain.ErrJobActive) {
		t.Fatalf("recutting while a job runs = %v", err)
	}
	// The recut takes the version's videos and starts from its plan.
	if n := env.store.Count(t, "SELECT COUNT(*) FROM job_assets WHERE job_id = ?", job.ID); n != 2 {
		t.Fatalf("the recut has %d videos", n)
	}
	var settingsJSON string
	if err := env.store.db.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", job.ID).Scan(&settingsJSON); err != nil {
		t.Fatal(err)
	}
	var settings domain.JobSettings
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil || settings.Recut == nil || settings.Recut.Change != "结尾留长一点" {
		t.Fatalf("the recut's settings = %s, %v", settingsJSON, err)
	}
	var base struct {
		Outline pickedDoc `json:"outline"`
		Shots   []struct {
			Pictures []plannedPicture `json:"pictures"`
		} `json:"shots"`
	}
	if err := json.Unmarshal(settings.Recut.Base, &base); err != nil || base.Outline.Design != "从海边走到山里" || len(base.Shots) != 2 ||
		!slices.Equal(base.Shots[0].Pictures[0].Beats, []string{"c1/1", "c2/1", "c1/3"}) {
		t.Fatalf("the base = %s, %v", settings.Recut.Base, err)
	}
	if templateOf(settings) != RecutTemplate || templateOf(domain.JobSettings{}) != PlanTemplate {
		t.Fatal("a recut is not planned with plan-v3")
	}
}

func TestARecutOfAnEditedVersionLeavesTheHandEditsOut(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, 0, domain.DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); err != nil {
		t.Fatal(err)
	}
	saving, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1")
	if err != nil {
		t.Fatal(err)
	}
	env.runJobs(t)
	edited, err := env.store.Job(ctx, editOwner, saving.ID)
	if err != nil || edited.Version == nil {
		t.Fatalf("the edit = %+v, %v", edited, err)
	}
	job, _, err := env.store.RecutVersion(ctx, editOwner, edited.Version.ID, "recut:1", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	var base string
	if err := env.store.db.QueryRowContext(ctx, "SELECT json_extract(settings_json, '$.recut.base') FROM jobs WHERE id = ?", job.ID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	// The plan's first picture still leads with c1/1, not the c1/3 put there by hand.
	if !strings.Contains(base, `"beats":["c1/1","c2/1","c1/3"]`) {
		t.Fatalf("the base of a recut of an edit = %s", base)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM jobs WHERE id = ? AND json_extract(settings_json, '$.edit') IS NULL", job.ID); n != 1 {
		t.Fatal("the recut kept the edit's pins")
	}
}

func TestOnlyARecutTakesARound(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	env.store.planMarkers = t.TempDir()
	if err := env.store.RegisterPlanGrant(ctx, editOwner, "alice"); err != nil {
		t.Fatal(err)
	}
	// Editing and saving never want a round.
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, 0, domain.DraftOp{Type: "lock", Shot: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1"); err != nil {
		t.Fatal(err)
	}
	env.runJobs(t)
	if n := env.store.Count(t, "SELECT COUNT(*) FROM jobs WHERE plan_wanted = 1") + env.store.Count(t, "SELECT COUNT(*) FROM rounds"); n != 0 {
		t.Fatal("editing wanted a planning round")
	}
	if _, err := env.store.ClaimRound(ctx, editOwner); !errors.Is(err, domain.ErrNoPlanWanted) {
		t.Fatalf("a claim after editing = %v", err)
	}
	// A recut that came to planning is claimed with its own template.
	job, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	env.store.Exec(t, "UPDATE jobs SET state = 'waiting', wait_reason = ?, plan_wanted = 1 WHERE id = ?", WaitVNAS, job.ID)
	env.store.Exec(t, "INSERT INTO steps(job_id, name, input_key, state, started_at) VALUES(?, 'pick', 'key', 'running', '2026-11-20T00:00:00Z')", job.ID)
	if err := env.store.SyncMarkers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.store.planMarkers, "alice")); err != nil {
		t.Fatalf("the recut's marker: %v", err)
	}
	claim, err := env.store.ClaimRound(ctx, editOwner)
	if err != nil || claim.JobID != job.ID || claim.Template != RecutTemplate {
		t.Fatalf("the recut's claim = %+v, %v", claim, err)
	}
}

func TestARecutsBriefCarriesItsSentenceAndBase(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	compute := &editCompute{}
	env.store.jobCompute = compute
	job, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	env.store.Exec(t, `INSERT INTO steps(job_id, name, input_key, state, result_json, started_at)
		SELECT ?, name, input_key, state, result_json, started_at FROM steps WHERE job_id = 'job:1' AND name IN ('catalog', 'song')`, job.ID)
	input := func() (domain.PlanInput, error) { return env.store.storedInput(ctx, job.ID) }
	if _, err := env.store.briefOf(ctx, job.ID, StepOutline, "key", input); err != nil {
		t.Fatal(err)
	}
	asked := compute.briefed[0]
	// The cards are numbered as when the version was cut: the base names them so.
	if asked.Change != "结尾留长一点" || !strings.Contains(string(asked.Base), "从海边走到山里") || asked.Picked != nil ||
		!strings.Contains(string(asked.Base), `"cards":["c1","c2"]`) || !strings.Contains(string(asked.Base), `"beats":["c3/2","c3/1"]`) {
		t.Fatalf("the recut's brief request = %+v", asked)
	}
	// A Worker that briefs another template is refused.
	compute.stale = true
	if _, err := env.store.briefOf(ctx, job.ID, StepOutline, "other", input); err == nil || !strings.Contains(err.Error(), "plan-v3") {
		t.Fatalf("a brief of plan-v2 for a recut = %v", err)
	}
}

// The base is the plan as it was answered, with how each part and picture
// links to the one before: the template asks to keep them where the
// sentence does not touch (#612 review).
func TestARecutsBaseKeepsThePlansLinks(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	env.store.Exec(t, `UPDATE steps SET result_json = ? WHERE job_id = 'job:1' AND name = 'pick'`,
		`{"design":"从海边走到山里","opening":"海","ending":"山","unmet":"直升机","parts":[
			{"name":"海边","summary":"走","link":"从清晨的海面进入","scenes":[{"query":"海边散步","cards":["c1","c2"]}]},
			{"name":"山里","summary":"爬","link":"顺着海浪声转到山路","scenes":[{"query":"海浪","cards":["c2"]},{"query":"山路","cards":["c3","c4"]}]}]}`)
	env.store.Exec(t, `UPDATE steps SET result_json = ? WHERE job_id = 'job:1' AND name = 'shots'`,
		`{"answer":{"parts":[{"name":"海边","pictures":[{"beats":["c1/1","c2/1","c1/3"],"shows":"海","link":"开场"}]},
			{"name":"山里","pictures":[{"beats":["c2/2"],"shows":"浪","link":"同一片海"},{"beats":["c3/2","c3/1"],"shows":"路","link":"脚步接脚步"}]}]}}`)
	job, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:links", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	var base string
	if err := env.store.db.QueryRowContext(ctx, "SELECT json_extract(settings_json, '$.recut.base') FROM jobs WHERE id = ?", job.ID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"顺着海浪声转到山路", "从清晨的海面进入", "脚步接脚步", "同一片海", `"shows":"路"`} {
		if !strings.Contains(base, kept) {
			t.Errorf("the base lacks %s: %s", kept, base)
		}
	}
	if strings.Contains(base, "unmet") || strings.Contains(base, "直升机") {
		t.Errorf("the base carries what the earlier request did not find: %s", base)
	}
}

// The recut's base names the version's cards by their numbers then; its
// brief names each by the number of the card now in the same place, and
// leaves out those no longer there (a video taken out, or analysed again and
// cut anew), whose scenes and pictures the round picks again.
func TestARecutsBriefNamesTheCardsAsTheyAreNumberedNow(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	compute := &editCompute{}
	env.store.jobCompute = compute
	job, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	// Video A was taken out, and video B's second card cut anew: its first
	// card (c3) is n1 now.
	now := map[string]videocutcompute.CatalogCard{
		"n1": card("n1", videoB, 0, 10, []float64{1, 5}, "山路", "近景", "特写"),
		"n2": card("n2", videoB, 10, 12, []float64{11}, "山路", "全景"),
	}
	catalog, _ := json.Marshal(catalogResult{Cards: now, Catalog: []string{"n1", "n2"}})
	env.store.Exec(t, `INSERT INTO steps(job_id, name, input_key, state, result_json, started_at) VALUES(?, 'catalog', 'key', 'done', ?, '2026-11-20T09:00:00Z')`,
		job.ID, string(catalog))
	env.store.Exec(t, `INSERT INTO steps(job_id, name, input_key, state, result_json, started_at)
		SELECT ?, name, input_key, state, result_json, started_at FROM steps WHERE job_id = 'job:1' AND name = 'song'`, job.ID)
	if _, err := env.store.briefOf(ctx, job.ID, StepOutline, "key", func() (domain.PlanInput, error) { return env.store.storedInput(ctx, job.ID) }); err != nil {
		t.Fatal(err)
	}
	base := string(compute.briefed[0].Base)
	if strings.Count(base, `"cards"`) != 1 || !strings.Contains(base, `"cards":["n1"]`) ||
		strings.Count(base, `"beats"`) != 1 || !strings.Contains(base, `"beats":["n1/2","n1/1"]`) {
		t.Fatalf("the base of a recut whose cards are numbered anew = %s", base)
	}
	for _, query := range []string{"海边散步", "海浪", "山路"} {
		if !strings.Contains(base, query) {
			t.Fatalf("the base lost what a scene asks for (%s): %s", query, base)
		}
	}
}

// Remove takes videos out of the grant, as dropping them from a job asks.
func (g *editGrants) Remove(context.Context, string, []string) error { return nil }

// A recut goes on without a video gone since its version was made, as a
// generate job does; only an edit does not (#612 review).
func TestARecutGoesOnWithoutAMissingVideo(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	job, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:missing", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	env.store.Exec(t, `UPDATE jobs SET state = ?, wait_reason = ?, missing_json = ? WHERE id = ?`,
		JobNeedsInput, NeedMissing, `["`+videoB+`"]`, job.ID)
	dropped, err := env.store.DropAssets(ctx, editOwner, job.ID, []string{videoB})
	if err != nil || dropped.State == JobNeedsInput {
		t.Fatalf("DropAssets() on a recut = %+v, %v", dropped, err)
	}
	if n := env.store.Count(t, "SELECT COUNT(*) FROM job_assets WHERE job_id = ?", job.ID); n != 1 {
		t.Fatalf("the recut keeps %d videos", n)
	}
}

// A request key names one request: generating with the key of a recut or of
// a save is another request of another kind (ErrConflict), not that job.
func TestGeneratingWithTheKeyOfAnotherKindOfJobIsAConflict(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	recut, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "key:1", "结尾留长一点")
	if err != nil {
		t.Fatal(err)
	}
	if job, _, err := env.store.Generate(ctx, editOwner, "project:1", "key:1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("generating with a recut's key = %+v, %v", job, err)
	}
	if _, err := env.store.Cancel(ctx, editOwner, recut.ID); err != nil {
		t.Fatal(err)
	}
	draft, err := env.store.VersionDraft(ctx, editOwner, env.version)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "key:2"); err != nil {
		t.Fatal(err)
	}
	if job, _, err := env.store.Generate(ctx, editOwner, "project:1", "key:2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("generating with a save's key = %+v, %v", job, err)
	}
	// The other way round was already so.
	if _, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "key:2", "结尾留长一点"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("recutting with a save's key = %v", err)
	}
	if _, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "key:1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("saving with a recut's key = %v", err)
	}
}
