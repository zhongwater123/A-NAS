package montage_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

// Planning by the member's VNAS (docs/specs/montage-center.md, 规划（VNAS）):
// the job waits, leaves a marker, a round is claimed, started and ended,
// and the two answers come back through the tools.

func newPlanEnv(t *testing.T) *jobEnv {
	t.Helper()
	env := newJobEnv(t)
	env.markers = filepath.Join(t.TempDir(), "plans")
	if err := os.Mkdir(env.markers, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.open(t)
	return env
}

func (e *jobEnv) marked(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(e.markers, "alice"))
	return err == nil
}

func (e *jobEnv) grant(t *testing.T) {
	t.Helper()
	if err := e.store.RegisterPlanGrant(context.Background(), "user:alice", "alice"); err != nil {
		t.Fatal(err)
	}
}

// waitForPlan runs the job until it waits for VNAS.
func (e *jobEnv) waitForPlan(t *testing.T, jobID string) montage.Job {
	t.Helper()
	job := e.settle(t, jobID)
	if job.State != montage.JobWaiting || job.WaitReason != montage.WaitVNAS {
		t.Fatalf("job = %+v", job)
	}
	return job
}

func outline(cards ...string) json.RawMessage {
	answer, _ := json.Marshal(map[string]any{"parts": []any{map[string]any{"name": "开场", "cards": cards}}})
	return answer
}

func TestWithoutAGrantTheJobWaitsAndNoMarkerIsLeft(t *testing.T) {
	env := newPlanEnv(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	if job.Error != montage.DetailVNASOff || env.marked(t) {
		t.Fatalf("job = %+v, marked %v", job, env.marked(t))
	}
	// A grant older than seven days leaves none either.
	env.grant(t)
	env.clock.advance(8 * 24 * time.Hour)
	if job = env.settle(t, job.ID); job.Error != montage.DetailGrantOld || env.marked(t) {
		t.Fatalf("job = %+v, marked %v", job, env.marked(t))
	}
}

func TestARoundPlansBothStepsAndTheFilmIsMade(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	if !env.marked(t) {
		t.Fatal("no marker for the waker")
	}
	// Bob has no job wanting a round.
	if _, err := env.store.ClaimRound(ctx, "user:bob"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("Bob's claim = %v", err)
	}
	claim, err := env.store.ClaimRound(ctx, "user:alice")
	if err != nil || claim.JobID != job.ID || claim.Template != montage.PlanTemplate || claim.TargetSeconds != 4 || len(claim.AttemptToken) != 32 {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if env.marked(t) {
		t.Fatal("the claim left its marker")
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("a second claim = %v", err)
	}
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "job-in-hermes"); err != nil {
		t.Fatal(err)
	}
	// Once started, the round is not abandoned.
	if err := env.store.AbandonRound(ctx, "user:alice", claim.RoundID, "late"); !errors.Is(err, montage.ErrRoundOver) {
		t.Fatalf("abandoning a started round = %v", err)
	}

	// Bob's VNAS cannot reach Alice's job, even with her token.
	if _, err := env.store.Brief(ctx, "user:bob", job.ID, claim.AttemptToken, montage.StepOutline); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("Bob's brief = %v", err)
	}
	if _, err := env.store.Brief(ctx, "user:alice", job.ID, "0123", montage.StepOutline); !errors.Is(err, montage.ErrNotFound) {
		t.Fatalf("another token = %v", err)
	}
	brief, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline)
	if err != nil || brief == nil {
		t.Fatalf("brief = %v, %v", brief, err)
	}
	// The shots wait for the outline.
	if early, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots); err != nil ||
		early.(*montage.ToolAnswer).Status != montage.ToolOutlineFirst {
		t.Fatalf("shots brief before the outline = %+v, %v", early, err)
	}
	// A card outside the catalog is refused, with reasons.
	refused, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v9-9"))
	if err != nil || refused.Status != montage.ToolRejected || len(refused.Reasons) == 0 {
		t.Fatalf("an outline with another card = %+v, %v", refused, err)
	}
	taken, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-0", "v2-0"))
	if err != nil || taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v, %v", taken, err)
	}
	// The same token again: taken already, not replaced.
	again, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-1"))
	if err != nil || again.Status != montage.ToolAccepted || again.Message != "已收下" {
		t.Fatalf("the outline again = %+v, %v", again, err)
	}
	// The job planned the first step and waits for the second.
	env.waitForPlan(t, job.ID)
	if shots, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots, outline("v1-0")); err != nil ||
		shots.Status != montage.ToolAccepted {
		t.Fatalf("shots = %+v, %v", shots, err)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || done.Version == nil {
		t.Fatalf("job = %+v", done)
	}
	// Answers after the end are told the round is over.
	if late, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-0")); err != nil ||
		late.Status != montage.ToolAccepted && late.Status != montage.ToolRoundOver {
		t.Fatalf("a late answer = %+v, %v", late, err)
	}
	if stale, err := env.store.StaleRounds(ctx, "user:alice"); err != nil || len(stale) != 0 {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
}

func TestAnAbandonedRoundWaitsAndTriesAgain(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, err := env.store.ClaimRound(ctx, "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.AbandonRound(ctx, "user:alice", claim.RoundID, "no model key on the device"); err != nil {
		t.Fatal(err)
	}
	// Abandoned, it is no longer started.
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "late"); !errors.Is(err, montage.ErrRoundOver) {
		t.Fatalf("starting an abandoned round = %v", err)
	}
	// Within the backoff no marker comes back.
	if job = env.waitForPlan(t, job.ID); env.marked(t) || job.Error != montage.DetailRetrying {
		t.Fatalf("job = %+v, marked %v", job, env.marked(t))
	}
	env.clock.advance(time.Minute + time.Second)
	if env.waitForPlan(t, job.ID); !env.marked(t) {
		t.Fatal("no marker after the backoff")
	}
}

func TestARoundThatEndsWithoutAnAnswerNeedsTheMember(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, _ := env.store.ClaimRound(ctx, "user:alice")
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "job"); err != nil {
		t.Fatal(err)
	}
	// The unit was killed: thirty minutes later the round is over.
	env.clock.advance(31 * time.Minute)
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown {
		t.Fatalf("job = %+v", needs)
	}
	// Not started again on its own; the member replans.
	if env.marked(t) {
		t.Fatal("a marker for a round that may have called the model")
	}
	if stale, err := env.store.StaleRounds(ctx, "user:alice"); err != nil || len(stale) != 1 || stale[0].RunRef != "job" {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
	if err := env.store.CleanedRounds(ctx, "user:alice", []string{claim.RoundID}); err != nil {
		t.Fatal(err)
	}
	if stale, _ := env.store.StaleRounds(ctx, "user:alice"); len(stale) != 0 {
		t.Fatalf("stale after cleaning = %+v", stale)
	}
	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if env.waitForPlan(t, job.ID); !env.marked(t) {
		t.Fatal("no marker after replanning")
	}
}

func TestThreeRefusedAnswersNeedTheMember(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, _ := env.store.ClaimRound(ctx, "user:alice")
	_ = env.store.StartRound(ctx, "user:alice", claim.RoundID, "job")
	for range 3 {
		answer, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, json.RawMessage(`{"parts":[]}`))
		if err != nil || answer.Status != montage.ToolRejected {
			t.Fatalf("answer = %+v, %v", answer, err)
		}
	}
	if over, _ := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-0")); over.Status != montage.ToolRoundOver {
		t.Fatalf("a fourth answer = %+v", over)
	}
	if needs := env.settle(t, job.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedPlanInvalid {
		t.Fatalf("job = %+v", needs)
	}
}

func TestAnswersAfterCancellingAreToldSo(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, _ := env.store.ClaimRound(ctx, "user:alice")
	_ = env.store.StartRound(ctx, "user:alice", claim.RoundID, "job")
	if _, err := env.store.Cancel(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	answer, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-0"))
	if err != nil || answer.Status != montage.ToolCancelled || answer.Message != "任务已取消" {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
	// The round's Hermes job is the next round's to remove.
	if stale, _ := env.store.StaleRounds(ctx, "user:alice"); len(stale) != 1 {
		t.Fatalf("stale = %+v", stale)
	}
}

func TestARoundNotStartedInFiveMinutesIsAbandoned(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, _ := env.store.ClaimRound(ctx, "user:alice")
	env.clock.advance(6 * time.Minute)
	if err := env.store.SweepRounds(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "late"); !errors.Is(err, montage.ErrRoundOver) {
		t.Fatalf("starting it late = %v", err)
	}
	// No model was called: the job waits for another round.
	if job = env.waitForPlan(t, job.ID); job.State != montage.JobWaiting {
		t.Fatalf("job = %+v", job)
	}
}

func TestMarkersComeBackAfterARestart(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	env.waitForPlan(t, env.generate(t, "generate:1").ID)
	if err := os.Remove(filepath.Join(env.markers, "alice")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.markers, "bob"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecoverJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if !env.marked(t) {
		t.Fatal("the marker did not come back")
	}
	if _, err := os.Stat(filepath.Join(env.markers, "bob")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a marker for nobody's job stayed: %v", err)
	}
}

func TestARoundPlansBothPlansTheSecondAfterTheFirst(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.plans = 2
	env.open(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, err := env.store.ClaimRound(ctx, "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "job-in-hermes"); err != nil {
		t.Fatal(err)
	}
	submit := func(step string, answer json.RawMessage) montage.ToolAnswer {
		t.Helper()
		got, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, step, answer)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := submit(montage.StepOutline, outline("v1-0", "v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("first outline = %+v", got)
	}
	env.waitForPlan(t, job.ID)
	// The first 方案's shots taken, the model is told to go on with the second.
	if got := submit(montage.StepShots, outline("v1-0")); got.Status != montage.ToolNextPlan {
		t.Fatalf("first shots = %+v", got)
	}
	// The second outline's brief carries the first, to differ from.
	brief, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Plan    int               `json:"plan"`
		Earlier []json.RawMessage `json:"earlierOutlines"`
	}
	if err := json.Unmarshal(brief.(map[string]json.RawMessage)["data"], &data); err != nil || data.Plan != 2 || len(data.Earlier) != 1 {
		t.Fatalf("second brief = %s, %v", brief.(map[string]json.RawMessage)["data"], err)
	}
	if got := submit(montage.StepOutline, outline("v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second outline = %+v", got)
	}
	if got := submit(montage.StepShots, outline("v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second shots = %+v", got)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || len(done.Versions) != 2 {
		t.Fatalf("job = %+v", done)
	}
}

func TestARoundEndedAfterTheFirstPlanLeavesTheSecondToAnother(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.plans = 2
	env.open(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, err := env.store.ClaimRound(ctx, "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "job-in-hermes"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, outline("v1-0")); err != nil {
		t.Fatal(err)
	}
	env.waitForPlan(t, job.ID)
	if _, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots, outline("v1-0")); err != nil {
		t.Fatal(err)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	// The first 方案 is saved; the second waits for a new round, not the member.
	waiting := env.waitForPlan(t, job.ID)
	if waiting.State != montage.JobWaiting || len(waiting.Versions) != 1 || !env.marked(t) {
		t.Fatalf("job = %+v, marked %v", waiting, env.marked(t))
	}
	if next, err := env.store.ClaimRound(ctx, "user:alice"); err != nil || next.JobID != job.ID {
		t.Fatalf("next claim = %+v, %v", next, err)
	}
}
