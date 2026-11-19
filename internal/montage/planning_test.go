package montage_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
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

// planning starts a job with a grant, runs it until it waits for VNAS, and
// claims and starts a round for it.
func (e *jobEnv) planning(t *testing.T) (montage.Job, montage.Claim) {
	t.Helper()
	e.grant(t)
	job := e.waitForPlan(t, e.generate(t, "generate:1").ID)
	return job, e.round(t)
}

// round claims and starts a round for Alice's job that wants one.
func (e *jobEnv) round(t *testing.T) montage.Claim {
	t.Helper()
	claim, err := e.store.ClaimRound(context.Background(), "user:alice")
	if err != nil {
		t.Fatalf("ClaimRound() = %v", err)
	}
	if err := e.store.StartRound(context.Background(), "user:alice", claim.RoundID, "job-in-hermes"); err != nil {
		t.Fatalf("StartRound() = %v", err)
	}
	return claim
}

func (e *jobEnv) submit(t *testing.T, claim montage.Claim, step string, answer json.RawMessage) montage.ToolAnswer {
	t.Helper()
	said, err := e.store.Submit(context.Background(), "user:alice", claim.JobID, claim.AttemptToken, step, answer)
	if err != nil {
		t.Fatalf("Submit(%s) = %v", step, err)
	}
	return said
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
	// With one the marker is left; once it is older than seven days the job
	// wants no round and the marker goes.
	env.grant(t)
	env.clock.advance(2 * time.Minute)
	if job = env.settle(t, job.ID); !env.marked(t) {
		t.Fatalf("no marker with a grant: %+v", job)
	}
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
	if refused := env.submit(t, claim, montage.StepOutline, outline("v9-9")); refused.Status != montage.ToolRejected || len(refused.Reasons) == 0 {
		t.Fatalf("an outline with another card = %+v", refused)
	}
	// As a model writes it: spaced, with characters JSON may escape. The
	// second step's key is worked out from the outline as it is kept.
	written := json.RawMessage(`{ "parts": [ {"name": "开场 <海> & 船", "cards": ["v1-0", "v2-0"]} ] }`)
	if taken := env.submit(t, claim, montage.StepOutline, written); taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v", taken)
	}
	// The same token again: taken already, not replaced.
	if again := env.submit(t, claim, montage.StepOutline, outline("v1-1")); again.Status != montage.ToolAccepted || again.Message != "已收下" {
		t.Fatalf("the outline again = %+v", again)
	}
	// The model reads the second step's brief before the job came to it, and
	// the job then plans the first step and waits for the second.
	if shots, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots); err != nil || shots == nil {
		t.Fatalf("shots brief = %v, %v", shots, err)
	}
	env.waitForPlan(t, job.ID)
	if shots := env.submit(t, claim, montage.StepShots, outline("v1-0")); shots.Status != montage.ToolAccepted {
		t.Fatalf("shots = %+v", shots)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || done.Version == nil {
		t.Fatalf("job = %+v", done)
	}
	// The second step's answer was turned back with the outline taken, the
	// first one, and each step's brief was built once, however often it was
	// read and answered.
	var kept struct {
		Parts []struct {
			Name  string   `json:"name"`
			Cards []string `json:"cards"`
		} `json:"parts"`
	}
	if len(env.compute.turned) != 1 || json.Unmarshal([]byte(env.compute.turned[0]), &kept) != nil ||
		len(kept.Parts) != 1 || kept.Parts[0].Name != "开场 <海> & 船" || len(kept.Parts[0].Cards) != 2 {
		t.Fatalf("turned back with %q", env.compute.turned)
	}
	if env.compute.briefs["pick"] != 1 || env.compute.briefs["shots"] != 1 {
		t.Fatalf("briefs built = %v", env.compute.briefs)
	}
	// Answers after the end are told the round is over.
	if late := env.submit(t, claim, montage.StepOutline, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("a late answer = %+v", late)
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

// A round that was claimed and never started is abandoned five minutes
// later; the backoff counts from then, not from its claim, which is already
// longer ago than the first backoffs.
func TestARoundNotStartedInFiveMinutesIsAbandonedAndBacksOffFromThen(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	claim, _ := env.store.ClaimRound(ctx, "user:alice")
	env.clock.advance(5*time.Minute + time.Second)
	if err := env.store.SweepRounds(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "late"); !errors.Is(err, montage.ErrRoundOver) {
		t.Fatalf("starting it late = %v", err)
	}
	// No model was called: the job waits for another round, after the backoff.
	if job = env.waitForPlan(t, job.ID); job.Error != montage.DetailRetrying || env.marked(t) {
		t.Fatalf("right after the round was abandoned: job = %+v, marked %v", job, env.marked(t))
	}
	env.clock.advance(time.Minute + time.Second)
	if env.waitForPlan(t, job.ID); !env.marked(t) {
		t.Fatal("no marker after the backoff")
	}
}

func TestARoundThatEndsWithoutAnAnswerNeedsTheMember(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	// The model began to plan and the unit was killed: thirty minutes after
	// its first call the round is over.
	if _, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline); err != nil {
		t.Fatal(err)
	}
	env.clock.advance(29 * time.Minute)
	if still := env.settle(t, job.ID); still.State != montage.JobWaiting {
		t.Fatalf("before the round's time is up: job = %+v", still)
	}
	env.clock.advance(2 * time.Minute)
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown {
		t.Fatalf("job = %+v", needs)
	}
	// Not started again on its own; the member replans.
	if env.marked(t) {
		t.Fatal("a marker for a round that may have called the model")
	}
	if late := env.submit(t, claim, montage.StepOutline, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("an answer after the round's time = %+v", late)
	}
	if stale, err := env.store.StaleRounds(ctx, "user:alice"); err != nil || len(stale) != 1 || stale[0].RunRef != "job-in-hermes" {
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

// Hermes may run the member's own due jobs before the planning job, in the
// same round of the unit: a round handed over whose model has not called yet
// is not over after thirty minutes, only once the unit's hour has passed.
func TestARoundWhoseModelHasNotStartedYetIsGivenTheUnitsHour(t *testing.T) {
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	env.clock.advance(40 * time.Minute)
	if waiting := env.settle(t, job.ID); waiting.State != montage.JobWaiting || waiting.WaitReason != montage.WaitVNAS {
		t.Fatalf("after 40 minutes without a call: job = %+v", waiting)
	}
	// The planning job starts now, and its answers are taken.
	if taken := env.submit(t, claim, montage.StepOutline, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("an outline 40 minutes after the hand-over = %+v", taken)
	}
	// Another job's round, never called: over after the unit's hour.
	if _, err := env.store.Cancel(context.Background(), "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	second := env.waitForPlan(t, env.generate(t, "generate:2").ID)
	env.round(t)
	env.clock.advance(66 * time.Minute)
	if needs := env.settle(t, second.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown {
		t.Fatalf("after the unit's hour without a call: job = %+v", needs)
	}
}

func TestThreeRefusedAnswersNeedTheMember(t *testing.T) {
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	for range 3 {
		if answer := env.submit(t, claim, montage.StepOutline, json.RawMessage(`{"parts":[]}`)); answer.Status != montage.ToolRejected {
			t.Fatalf("answer = %+v", answer)
		}
	}
	if over := env.submit(t, claim, montage.StepOutline, outline("v1-0")); over.Status != montage.ToolRoundOver {
		t.Fatalf("a fourth answer = %+v", over)
	}
	if needs := env.settle(t, job.ID); needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedPlanInvalid {
		t.Fatalf("job = %+v", needs)
	}
}

func TestAnswersAfterCancellingAreToldSo(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	if _, err := env.store.Cancel(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if answer := env.submit(t, claim, montage.StepOutline, outline("v1-0")); answer.Status != montage.ToolCancelled || answer.Message != "任务已取消" {
		t.Fatalf("answer = %+v", answer)
	}
	if kept := env.store.Count(t, "SELECT COUNT(*) FROM plan_answers WHERE job_id = ?", job.ID); kept != 0 {
		t.Fatalf("%d answers kept for a cancelled job", kept)
	}
	// The round's Hermes job is the next round's to remove.
	if stale, _ := env.store.StaleRounds(ctx, "user:alice"); len(stale) != 1 {
		t.Fatalf("stale = %+v", stale)
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

// A restart of the montage service while a round plans a job starts no other
// round for it (#514: a model that may have been called is not called again
// on the service's own account): the round goes on, and its answers are
// taken as before.
func TestARestartWhileARoundPlansStartsNoOther(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	if taken := env.submit(t, claim, montage.StepOutline, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v", taken)
	}
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.open(t)
	if err := env.store.RecoverJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if waiting := env.waitForPlan(t, job.ID); waiting.Step != "shots" || env.marked(t) {
		t.Fatalf("after the restart: job = %+v, marked %v", waiting, env.marked(t))
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("a second round after the restart = %v", err)
	}
	if taken := env.submit(t, claim, montage.StepShots, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("the round's shots after the restart = %+v", taken)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
}

// A marker with no job behind it would have the waker start the member's
// unit again and again: a claim that finds nothing to plan takes it, and so
// does deleting the project or the account of a job that wanted a round.
func TestAMarkerGoesWithTheJobItWasFor(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	if !env.marked(t) {
		t.Fatal("no marker for the waker")
	}
	if err := env.store.DeleteProject(ctx, "user:alice", env.project.ID); err != nil {
		t.Fatal(err)
	}
	if env.marked(t) {
		t.Fatal("the marker outlived its project")
	}
	// A marker left behind all the same, as by a sync that failed.
	if err := os.WriteFile(filepath.Join(env.markers, "alice"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) || env.marked(t) {
		t.Fatalf("a claim with nothing to plan = %v, marked %v (job %s)", err, env.marked(t), job.ID)
	}

	// The same for an account that is deleted, whose grant goes too.
	other := newPlanEnv(t)
	other.grant(t)
	other.waitForPlan(t, other.generate(t, "generate:1").ID)
	if err := other.store.RemoveAccount(ctx, "user:alice"); err != nil {
		t.Fatal(err)
	}
	if other.marked(t) || other.store.Count(t, "SELECT COUNT(*) FROM plan_grants") != 0 {
		t.Fatal("the marker or the grant outlived the account")
	}
	// The upkeep takes what is left of a grant that ran out.
	third := newPlanEnv(t)
	third.grant(t)
	third.waitForPlan(t, third.generate(t, "generate:1").ID)
	third.clock.advance(8 * 24 * time.Hour)
	third.store.Upkeep(ctx)
	if third.marked(t) {
		t.Fatal("the marker outlived the grant")
	}
}

// A job put back in the queue wants no round until it comes to planning
// again: claimed before that, the round would plan for a job that is not
// waiting for it, and the job would then find the round over.
func TestAJobBackInTheQueueIsNotClaimedUntilItWaitsAgain(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.grant(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	queued, err := env.store.Replan(ctx, "user:alice", job.ID)
	if err != nil || queued.State != montage.JobQueued {
		t.Fatalf("Replan() = %+v, %v", queued, err)
	}
	if env.marked(t) {
		t.Fatal("a marker for a job that is back in the queue")
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("claiming a queued job = %v", err)
	}
	// Even with a marker the waker still saw.
	if err := os.WriteFile(filepath.Join(env.markers, "alice"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("claiming a queued job by its old marker = %v", err)
	}
	env.waitForPlan(t, job.ID)
	claim := env.round(t)
	if taken := env.submit(t, claim, montage.StepOutline, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v", taken)
	}
	if waiting := env.waitForPlan(t, job.ID); waiting.Step != "shots" {
		t.Fatalf("job = %+v", waiting)
	}
}

// Planning again ends the round in progress: what it hands back afterwards
// is not kept, its refusals do not count against the new epoch, and it does
// not hold up the next round.
func TestPlanningAgainEndsTheRoundInProgress(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, old := env.planning(t)
	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if good := env.submit(t, old, montage.StepOutline, outline("v1-0")); good.Status != montage.ToolRoundOver {
		t.Fatalf("the old round's outline = %+v", good)
	}
	for range 4 {
		if bad := env.submit(t, old, montage.StepOutline, json.RawMessage(`{"parts":[]}`)); bad.Status != montage.ToolRoundOver {
			t.Fatalf("the old round's bad outline = %+v", bad)
		}
	}
	if brief, err := env.store.Brief(ctx, "user:alice", job.ID, old.AttemptToken, montage.StepOutline); err != nil ||
		brief.(*montage.ToolAnswer).Status != montage.ToolRoundOver {
		t.Fatalf("the old round's brief = %+v, %v", brief, err)
	}
	if waiting := env.waitForPlan(t, job.ID); waiting.Step != "pick" || !env.marked(t) {
		t.Fatalf("job = %+v, marked %v", waiting, env.marked(t))
	}
	// The next round is claimed at once, not when the old one runs out.
	next := env.round(t)
	if next.RoundID == old.RoundID {
		t.Fatal("the old round was claimed again")
	}
	if taken := env.submit(t, next, montage.StepOutline, outline("v2-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("the new round's outline = %+v", taken)
	}
	// The old round's Hermes job is still the next round's to remove.
	if stale, _ := env.store.StaleRounds(ctx, "user:alice"); len(stale) != 2 {
		t.Fatalf("stale = %+v", stale)
	}
}

// A round plans for the inputs the job had when it was claimed. When the
// song's analysis changes, the catalog staying the same, the first step has
// another input key: the old round's answers, which come back last, are
// dropped, and an outline taken before the change is not used for the new
// inputs either.
func TestAnswersForInputsTheJobNoLongerHasAreDropped(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	env.store.UseMusic(reanalysedSong{version: "before"})
	job, old := env.planning(t)
	if taken := env.submit(t, old, montage.StepOutline, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v", taken)
	}
	if waiting := env.waitForPlan(t, job.ID); waiting.Step != "shots" {
		t.Fatalf("job = %+v", waiting)
	}
	// The song is analysed again while the round still runs.
	env.store.UseMusic(reanalysedSong{version: "after"})
	env.clock.advance(2 * time.Minute)
	waiting := env.waitForPlan(t, job.ID)
	if waiting.Step != "pick" || !env.marked(t) {
		t.Fatalf("after the song changed: job = %+v, marked %v", waiting, env.marked(t))
	}
	// The old round hands its second step back last.
	if late := env.submit(t, old, montage.StepShots, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("the old round's shots = %+v", late)
	}
	if late := env.submit(t, old, montage.StepOutline, outline("v2-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("the old round's outline = %+v", late)
	}
	if kept := env.store.Count(t, "SELECT COUNT(*) FROM plan_answers WHERE job_id = ? AND step = 'shots'", job.ID); kept != 0 {
		t.Fatalf("%d late answers kept", kept)
	}
	// A new round plans both steps for the new inputs, and the film is made
	// from its answers.
	next := env.round(t)
	if early, err := env.store.Brief(ctx, "user:alice", job.ID, next.AttemptToken, montage.StepShots); err != nil ||
		early.(*montage.ToolAnswer).Status != montage.ToolOutlineFirst {
		t.Fatalf("the new round's shots brief before its outline = %+v, %v", early, err)
	}
	if taken := env.submit(t, next, montage.StepOutline, outline("v2-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("the new round's outline = %+v", taken)
	}
	env.waitForPlan(t, job.ID)
	if taken := env.submit(t, next, montage.StepShots, outline("v2-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("the new round's shots = %+v", taken)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("job = %+v", done)
	}
	if len(env.compute.turned) != 1 || env.compute.turned[0] != string(outline("v2-0")) {
		t.Fatalf("the film was planned from %q", env.compute.turned)
	}
}

// A round carries the first step's input key it was claimed for, and every
// call checks it against the key the job's step has now: once they differ,
// as when a step upstream started over, the round is told it is over, also
// before the job has looked at its rounds again.
func TestARoundForAnotherInputKeyIsOverAtOnce(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	env.store.Exec(t, "UPDATE steps SET input_key = 'the-key-of-other-inputs' WHERE job_id = ? AND name = 'pick'", job.ID)
	if late := env.submit(t, claim, montage.StepOutline, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("an outline for inputs the job no longer has = %+v", late)
	}
	if brief, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepOutline); err != nil ||
		brief.(*montage.ToolAnswer).Status != montage.ToolRoundOver {
		t.Fatalf("its brief = %+v, %v", brief, err)
	}
	// A step that is gone altogether, as while the job waits upstream again.
	env.store.Exec(t, "DELETE FROM steps WHERE job_id = ? AND name = 'pick'", job.ID)
	if late := env.submit(t, claim, montage.StepOutline, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("an outline for a step the job no longer has = %+v", late)
	}
	if kept := env.store.Count(t, "SELECT COUNT(*) FROM plan_answers WHERE job_id = ?", job.ID); kept != 0 {
		t.Fatalf("%d answers kept", kept)
	}
}

// Each step keeps the first good answer, also when two come at once, as from
// a tool call tried again or the member's own process on the socket.
func TestTwoAnswersAtOnceKeepOne(t *testing.T) {
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	answers := []json.RawMessage{outline("v1-0"), outline("v1-1"), outline("v2-0"), outline("v2-1")}
	var wg sync.WaitGroup
	said := make([]montage.ToolAnswer, len(answers))
	failed := make([]error, len(answers))
	for i, answer := range answers {
		wg.Go(func() {
			said[i], failed[i] = env.store.Submit(context.Background(), "user:alice", job.ID, claim.AttemptToken, montage.StepOutline, answer)
		})
	}
	wg.Wait()
	for i := range answers {
		if failed[i] != nil || said[i].Status != montage.ToolAccepted {
			t.Fatalf("answer %d = %+v, %v", i, said[i], failed[i])
		}
	}
	if kept := env.store.Count(t, "SELECT COUNT(*) FROM plan_answers WHERE job_id = ? AND step = 'pick'", job.ID); kept != 1 {
		t.Fatalf("%d outlines kept", kept)
	}
	// The job plans from the one kept, and a later answer does not replace it.
	env.waitForPlan(t, job.ID)
	if later := env.submit(t, claim, montage.StepOutline, json.RawMessage(`{"parts":[{"name":"后来","cards":["v1-0"]}]}`)); later.Status != montage.ToolAccepted {
		t.Fatalf("a later answer = %+v", later)
	}
	if plans := env.store.Count(t, `SELECT COUNT(*) FROM plans p JOIN plan_answers a ON a.job_id = p.job_id AND a.step = p.step
		AND a.answer_json = p.answer_json WHERE p.job_id = ? AND p.voided_at IS NULL AND p.answer_json NOT LIKE '%后来%'`, job.ID); plans != 1 {
		t.Fatalf("the job planned from %d of the answers kept", plans)
	}
}

// The desktop says a job is being planned while a round has it, though the
// job itself waits for VNAS all the while; before that it says why it waits.
func TestNoticesTellWaitingFromPlanning(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job := env.waitForPlan(t, env.generate(t, "generate:1").ID)
	only := func(kind, detail string) {
		t.Helper()
		notices, err := env.store.Notices(ctx, "user:alice")
		if err != nil || len(notices) != 1 || notices[0].Kind != kind || notices[0].Detail != detail || notices[0].JobID != job.ID {
			t.Fatalf("notices = %+v, %v; want %s %q", notices, err, kind, detail)
		}
	}
	only(montage.NoticeWaiting, montage.DetailVNASOff)
	env.grant(t)
	env.clock.advance(2 * time.Minute)
	env.waitForPlan(t, job.ID)
	only(montage.NoticeWaiting, "")
	claim, err := env.store.ClaimRound(ctx, "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	only(montage.NoticePlanning, "")
	if err := env.store.StartRound(ctx, "user:alice", claim.RoundID, "job"); err != nil {
		t.Fatal(err)
	}
	env.waitForPlan(t, job.ID)
	only(montage.NoticePlanning, "")
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	env.settle(t, job.ID)
	only(montage.NoticeNeeds, "")
	// Ten minutes without a round and the job says VNAS has not taken it.
	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	env.waitForPlan(t, job.ID)
	only(montage.NoticeWaiting, "")
	env.clock.advance(11 * time.Minute)
	env.waitForPlan(t, job.ID)
	only(montage.NoticeWaiting, montage.DetailNotTaken)
}

// The montage center notes a planning grant only when the Product Service
// says it registered one: the header reaches it from the request's context
// through the proxy, which drops whatever a browser sent under that name.
func TestAPlanningGrantIsNotedOnlyOnTheProductServicesWord(t *testing.T) {
	env := newPlanEnv(t)
	job := env.generate(t, "generate:1")
	api := serviceproxy.Local(montageapi.New(env.store, nil))
	replan := func(said string, browser bool) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, montageapi.PathPrefix+"/jobs/"+job.ID+"/replan", nil)
		ctx := accounts.WithUser(request.Context(), accounts.User{ID: "user:alice", Username: "alice"})
		if browser {
			request.Header.Set(montageapi.AssistantHeader, said)
		} else if said != "" {
			ctx = serviceproxy.WithInternalHeader(ctx, montageapi.AssistantHeader, said)
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request.WithContext(ctx))
		// The job is queued, which planning again does not apply to.
		if recorder.Code != http.StatusConflict {
			t.Fatalf("replan answered %d %s", recorder.Code, recorder.Body)
		}
	}
	noted := func() int {
		return env.store.Count(t, "SELECT COUNT(*) FROM plan_grants WHERE owner_user_id = 'user:alice' AND username = 'alice'")
	}
	replan("on", true)
	replan("", false)
	replan("off", false)
	if noted() != 0 {
		t.Fatal("a planning grant was noted without the Product Service's word")
	}
	replan("on", false)
	if noted() != 1 {
		t.Fatal("the planning grant the Product Service registered was not noted")
	}
}

// newTwoPlanEnv is newPlanEnv with jobs that make two 方案, as the product's do.
func newTwoPlanEnv(t *testing.T) *jobEnv {
	t.Helper()
	env := newPlanEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.plans = 2
	env.open(t)
	return env
}

// outlineBrief reads the outline brief of a round: which 方案 it names (none
// for the first) and how many earlier outlines it carries.
func (e *jobEnv) outlineBrief(t *testing.T, claim montage.Claim) (int, int) {
	t.Helper()
	brief, err := e.store.Brief(context.Background(), "user:alice", claim.JobID, claim.AttemptToken, montage.StepOutline)
	if err != nil {
		t.Fatalf("Brief() = %v", err)
	}
	var data struct {
		Plan    int               `json:"plan"`
		Earlier []json.RawMessage `json:"earlierOutlines"`
	}
	if err := json.Unmarshal(brief.(map[string]json.RawMessage)["data"], &data); err != nil {
		t.Fatalf("brief = %s, %v", brief.(map[string]json.RawMessage)["data"], err)
	}
	return data.Plan, len(data.Earlier)
}

// One round plans both 方案 (docs/architecture/montage-center.md, 一轮规划两个
// 方案): the first's shots taken, the model is told to go on with the next,
// whose outline brief carries the first outline to differ from. Each 方案's
// answers are kept under its own input keys, and the job takes them as it
// comes to their steps.
func TestARoundPlansBothPlansTheSecondAfterTheFirst(t *testing.T) {
	ctx := context.Background()
	env := newTwoPlanEnv(t)
	job, claim := env.planning(t)
	if plan, earlier := env.outlineBrief(t, claim); plan != 0 || earlier != 0 {
		t.Fatalf("the first outline's brief names 方案 %d with %d earlier outlines", plan, earlier)
	}
	if got := env.submit(t, claim, montage.StepOutline, outline("v1-0", "v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("first outline = %+v", got)
	}
	if got := env.submit(t, claim, montage.StepShots, outline("v1-0")); got.Status != montage.ToolNextPlan || got.Message == "" {
		t.Fatalf("first shots = %+v", got)
	}
	// The call tried again, as after a lost answer, is told the same; no
	// answer of the second 方案 is taken from it.
	if got := env.submit(t, claim, montage.StepShots, outline("v1-0")); got.Status != montage.ToolNextPlan {
		t.Fatalf("first shots again = %+v", got)
	}
	// The model goes on before the job came to the second 方案: the job is
	// still at the first's steps, or waits.
	if early, err := env.store.Brief(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots); err != nil ||
		early.(*montage.ToolAnswer).Status != montage.ToolOutlineFirst {
		t.Fatalf("second shots brief before its outline = %+v, %v", early, err)
	}
	if plan, earlier := env.outlineBrief(t, claim); plan != 2 || earlier != 1 {
		t.Fatalf("the second outline's brief names 方案 %d with %d earlier outlines", plan, earlier)
	}
	if got := env.submit(t, claim, montage.StepOutline, outline("v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second outline = %+v", got)
	}
	if got := env.submit(t, claim, montage.StepShots, outline("v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second shots = %+v", got)
	}
	// Past the last 方案 the tools keep to it: its shots again are taken already.
	if got := env.submit(t, claim, montage.StepShots, outline("v1-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second shots again = %+v", got)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || len(done.Versions) != 2 || done.Versions[0].Plan != 1 || done.Versions[1].Plan != 2 {
		t.Fatalf("job = %+v", done)
	}
	// Each 方案's shots were turned back with its own outline, and the outline's
	// brief, the same for both, was built once.
	if len(env.compute.turned) != 2 || env.compute.turned[0] == env.compute.turned[1] {
		t.Fatalf("turned back with %q", env.compute.turned)
	}
	if env.compute.briefs["pick"] != 1 || env.compute.briefs["shots"] != 2 {
		t.Fatalf("briefs built = %v", env.compute.briefs)
	}
	if rounds := env.store.Count(t, "SELECT COUNT(*) FROM rounds WHERE job_id = ?", job.ID); rounds != 1 {
		t.Fatalf("%d rounds planned the two 方案", rounds)
	}
}

// A round that handed the first 方案 back and ended before the second: the
// model may have been called, so no round is started on its own (#514). The
// first 方案's film is made and saved; for the second the member is asked, and
// the round she allows plans the second alone.
func TestARoundThatEndsAfterTheFirstPlanAsksTheMemberForTheSecond(t *testing.T) {
	ctx := context.Background()
	env := newTwoPlanEnv(t)
	job, claim := env.planning(t)
	env.submit(t, claim, montage.StepOutline, outline("v1-0", "v2-0"))
	if got := env.submit(t, claim, montage.StepShots, outline("v1-0")); got.Status != montage.ToolNextPlan {
		t.Fatalf("first shots = %+v", got)
	}
	if err := env.store.EndRound(ctx, "user:alice", claim.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown || needs.Step != "pick-2" ||
		len(needs.Versions) != 1 || needs.Versions[0].Plan != 1 {
		t.Fatalf("job = %+v", needs)
	}
	if env.marked(t) {
		t.Fatal("a marker for a round the member did not ask for")
	}
	if _, err := env.store.ClaimRound(ctx, "user:alice"); !errors.Is(err, montage.ErrNoPlanWanted) {
		t.Fatalf("a claim nobody asked for = %v", err)
	}
	if env.lib.saves != 1 {
		t.Fatalf("%d films saved before the second 方案 was planned", env.lib.saves)
	}

	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if env.waitForPlan(t, job.ID); !env.marked(t) {
		t.Fatal("no marker after the member planned again")
	}
	// The first 方案 was not made again, and the new round plans the second.
	if env.lib.saves != 1 {
		t.Fatalf("%d films saved after planning again", env.lib.saves)
	}
	second := env.round(t)
	if plan, earlier := env.outlineBrief(t, second); plan != 2 || earlier != 1 {
		t.Fatalf("the new round's outline brief names 方案 %d with %d earlier outlines", plan, earlier)
	}
	env.submit(t, second, montage.StepOutline, outline("v2-0"))
	if got := env.submit(t, second, montage.StepShots, outline("v2-0")); got.Status != montage.ToolAccepted {
		t.Fatalf("second shots = %+v", got)
	}
	if err := env.store.EndRound(ctx, "user:alice", second.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || len(done.Versions) != 2 || env.lib.saves != 2 {
		t.Fatalf("job = %+v, %d films saved", done, env.lib.saves)
	}
}

// A round that handed in only the first 方案's outline and ended needs the
// member as before: an outline alone is no 方案 handed back.
func TestARoundThatEndsWithOnlyAnOutlineNeedsTheMember(t *testing.T) {
	env := newTwoPlanEnv(t)
	job, claim := env.planning(t)
	env.submit(t, claim, montage.StepOutline, outline("v1-0"))
	if err := env.store.EndRound(context.Background(), "user:alice", claim.RoundID, "the unit was stopped"); err != nil {
		t.Fatal(err)
	}
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedVNASUnknown || needs.Step != "shots" || env.marked(t) {
		t.Fatalf("job = %+v, marked %v", needs, env.marked(t))
	}
}

// A video deleted for good after the first 方案 was saved: taking it out
// plans what is left again, in a new round, and the 方案 already saved stays
// as it is. It is not made again and not saved a second time, which the
// versions' UNIQUE (job_id, plan) would refuse after the photo library took
// the film; and its film's copy does not stay in the job's directory while
// the second 方案 is planned.
func TestDroppingAVideoAfterTheFirstPlanWasSavedKeepsItAndMakesTheRest(t *testing.T) {
	ctx := context.Background()
	env := newTwoPlanEnv(t)
	job, claim := env.planning(t)
	env.submit(t, claim, montage.StepOutline, outline("v1-0", "v2-0"))
	env.submit(t, claim, montage.StepShots, outline("v1-0"))
	// The first 方案 is made and saved while the round still plans the second.
	waiting := env.waitForPlan(t, job.ID)
	if len(waiting.Versions) != 1 || env.lib.saves != 1 || waiting.Step != "pick-2" {
		t.Fatalf("job = %+v, %d films saved", waiting, env.lib.saves)
	}
	if _, err := os.Stat(filepath.Join(env.root, "tmp", job.ID, "film.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the saved film's copy is still in the job's directory: %v", err)
	}
	first, segments := waiting.Versions[0], env.editing.segments

	env.lib.set(func() { delete(env.lib.videos, "asset:a2") })
	env.clock.advance(2 * time.Minute)
	needs := env.settle(t, job.ID)
	if needs.State != montage.JobNeedsInput || needs.WaitReason != montage.NeedMissing || len(needs.Missing) != 1 || needs.Missing[0] != "asset:a2" {
		t.Fatalf("job = %+v", needs)
	}
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, needs.Missing); err != nil {
		t.Fatal(err)
	}
	if late := env.submit(t, claim, montage.StepOutline, outline("v1-0")); late.Status != montage.ToolRoundOver {
		t.Fatalf("an answer of the round that planned the old videos = %+v", late)
	}
	again := env.waitForPlan(t, job.ID)
	if env.lib.saves != 1 || env.editing.segments != segments || len(again.Versions) != 1 || again.Versions[0] != first {
		t.Fatalf("the saved 方案 was touched: job = %+v, %d films saved, %d segments rendered (were %d)",
			again, env.lib.saves, env.editing.segments, segments)
	}
	for _, st := range again.Steps[3:8] {
		if st.State != "done" {
			t.Fatalf("the saved 方案's step %s is %s", st.Name, st.State)
		}
	}
	if again.Step != "pick-2" || !env.marked(t) {
		t.Fatalf("job = %+v, marked %v", again, env.marked(t))
	}
	// The new round plans the second 方案 alone, from the videos left; the
	// first's outline was for other cards and is not given along.
	second := env.round(t)
	if plan, earlier := env.outlineBrief(t, second); plan != 2 || earlier != 0 {
		t.Fatalf("the new round's outline brief names 方案 %d with %d earlier outlines", plan, earlier)
	}
	if refused := env.submit(t, second, montage.StepOutline, outline("v2-0")); refused.Status != montage.ToolRejected {
		t.Fatalf("an outline with a card of the video taken out = %+v", refused)
	}
	env.submit(t, second, montage.StepOutline, outline("v1-1"))
	if got := env.submit(t, second, montage.StepShots, outline("v1-1")); got.Status != montage.ToolAccepted {
		t.Fatalf("second shots = %+v", got)
	}
	if err := env.store.EndRound(ctx, "user:alice", second.RoundID, "ok"); err != nil {
		t.Fatal(err)
	}
	done := env.settle(t, job.ID)
	if done.State != montage.JobDone || len(done.Versions) != 2 || done.Versions[0] != first || done.Versions[1].Plan != 2 || env.lib.saves != 2 {
		t.Fatalf("job = %+v, %d films saved", done, env.lib.saves)
	}
	if env.jobFiles(job.ID) {
		t.Fatal("the job's files outlived it")
	}
}

// An answer that fails its checks after the member planned again: checking
// it waited for the compute Worker (the second step's brief was still being
// built), and meanwhile its round ended. The refusal is the old round's: the
// round is told it is over, and the new planning starts with none counted
// against it.
func TestARefusalThatCrossesPlanningAgainCountsForNothing(t *testing.T) {
	ctx := context.Background()
	env := newPlanEnv(t)
	job, claim := env.planning(t)
	if taken := env.submit(t, claim, montage.StepOutline, outline("v1-0")); taken.Status != montage.ToolAccepted {
		t.Fatalf("outline = %+v", taken)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.compute.mu.Lock()
	env.compute.onBrief = func(step string) {
		if step == montage.StepShots {
			once.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	env.compute.mu.Unlock()
	answered := make(chan montage.ToolAnswer, 1)
	failed := make(chan error, 1)
	go func() {
		said, err := env.store.Submit(ctx, "user:alice", job.ID, claim.AttemptToken, montage.StepShots, outline("v9-9"))
		if err != nil {
			failed <- err
			return
		}
		answered <- said
	}()
	select {
	case <-reached:
	case err := <-failed:
		t.Fatalf("Submit() = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the shots' brief was never built")
	}
	if _, err := env.store.Replan(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case said := <-answered:
		if said.Status != montage.ToolRoundOver {
			t.Fatalf("a refusal after planning again = %+v", said)
		}
	case err := <-failed:
		t.Fatalf("Submit() = %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the submission never answered")
	}
	if counted := env.store.Count(t, "SELECT plan_rejections FROM jobs WHERE id = ?", job.ID); counted != 0 {
		t.Fatalf("%d refusals counted against the new planning", counted)
	}
	// In the other order, as before: a refusal handed in after the member
	// planned again is told the round is over too, and counts for nothing.
	if late := env.submit(t, claim, montage.StepShots, outline("v9-9")); late.Status != montage.ToolRoundOver {
		t.Fatalf("a refusal of the round that is over = %+v", late)
	}
	if counted := env.store.Count(t, "SELECT plan_rejections FROM jobs WHERE id = ?", job.ID); counted != 0 {
		t.Fatalf("%d refusals counted after the round was over", counted)
	}
}
