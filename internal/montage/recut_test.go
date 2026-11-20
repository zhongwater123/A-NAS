package montage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Recutting a version from a sentence (#602).

// PlanBrief briefs as the compute Worker does: a recut's with plan-v3.
func (c *editCompute) PlanBrief(_ context.Context, request PlanBriefRequest) (PlanBrief, error) {
	c.briefed = append(c.briefed, request)
	template := "plan-v2"
	if request.Change != "" && !c.stale {
		template = "plan-v3"
	}
	return PlanBrief{Data: json.RawMessage(`{}`), Schema: json.RawMessage(`{}`), Template: template}, nil
}

func TestARecutPlansAgainFromTheVersionsPlan(t *testing.T) {
	env := newEditEnv(t)
	ctx := t.Context()
	for _, change := range []string{"", "  ", strings.Repeat("长", MaxAskRunes+1)} {
		if _, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:0", change); !errors.Is(err, ErrInvalid) {
			t.Fatalf("recutting with %q = %v", change, err)
		}
	}
	if _, _, err := env.store.RecutVersion(ctx, editOther, env.version, "recut:0", "结尾留长一点"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member recutting = %v", err)
	}
	job, created, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点")
	if err != nil || !created || job.Kind != KindRecut || !slices.Equal(stepNamesOf(job), env.store.stepNames(KindGenerate, 1)) {
		t.Fatalf("RecutVersion() = %+v, %v, %v", job, created, err)
	}
	if again, created, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:1", "结尾留长一点"); err != nil || created || again.ID != job.ID {
		t.Fatalf("the same request again = %v, %v, %v", again.ID, created, err)
	}
	if _, _, err := env.store.RecutVersion(ctx, editOwner, "version:2", "recut:1", "结尾留长一点"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the key of another recut = %v", err)
	}
	if _, _, err := env.store.RecutVersion(ctx, editOwner, env.version, "recut:2", "开头短一点"); !errors.Is(err, ErrJobActive) {
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
	var settings JobSettings
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
	if templateOf(settings) != RecutTemplate || templateOf(JobSettings{}) != PlanTemplate {
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
	if draft, err = env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "replace", Shot: 1, Beat: "c1/3"}); err != nil {
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
	if _, err := env.store.EditDraft(ctx, editOwner, draft.ID, 0, DraftOp{Type: "lock", Shot: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.SaveDraft(ctx, editOwner, draft.ID, "save:1"); err != nil {
		t.Fatal(err)
	}
	env.runJobs(t)
	if n := env.store.Count(t, "SELECT COUNT(*) FROM jobs WHERE plan_wanted = 1") + env.store.Count(t, "SELECT COUNT(*) FROM rounds"); n != 0 {
		t.Fatal("editing wanted a planning round")
	}
	if _, err := env.store.ClaimRound(ctx, editOwner); !errors.Is(err, ErrNoPlanWanted) {
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
	input := func() (PlanInput, error) { return env.store.storedInput(ctx, job.ID) }
	if _, err := env.store.briefOf(ctx, job.ID, StepOutline, "key", input); err != nil {
		t.Fatal(err)
	}
	asked := compute.briefed[0]
	if asked.Change != "结尾留长一点" || !strings.Contains(string(asked.Base), "从海边走到山里") || asked.Picked != nil {
		t.Fatalf("the recut's brief request = %+v", asked)
	}
	// A Worker that briefs another template is refused.
	compute.stale = true
	if _, err := env.store.briefOf(ctx, job.ID, StepOutline, "other", input); err == nil || !strings.Contains(err.Error(), "plan-v3") {
		t.Fatalf("a brief of plan-v2 for a recut = %v", err)
	}
}
