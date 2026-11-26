package app

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// Planning by the member's own VNAS (docs/architecture/montage-planning-session.md):
// a job that comes to planning waits for VNAS and leaves a marker,
// /run/a-nas-videocut/plans/<account>; root's planner claims a round here,
// holds it as one conversation with the member's planning instance, and
// reports that it started and how it ended, or that it never started and
// why (ai/assistant/videocut-planner.py). The model reads the
// briefs and hands the two steps back through the videocut tools (briefs.go);
// each answer is checked there and the first good one kept for the input key
// of the step it answers. The job's own step then takes it into its plan
// (steps.go), so an answer for inputs the job no longer has is never used.
// One round plans every 方案 of the job, one after another: a later 方案's
// steps have input keys of their own (planKey), and their answers wait there
// until the job comes to them.

// PlanTemplate is the version of the prompt template the planner fills
// (ai/anas_videocut/prompts/); a claim answers it, and a planner that has
// another refuses the round.
const PlanTemplate = "plan-v2"

// RecutTemplate is the template a recut's round fills: plan-v2's, starting
// from the version's plan and changing only what the member's sentence asks
// (#596).
const RecutTemplate = "plan-v3"

// templateOf is the template a job with these settings is planned with.
func templateOf(settings domain.JobSettings) string {
	if settings.Recut != nil {
		return RecutTemplate
	}
	return PlanTemplate
}

const (
	// planGrantLife is how long a planning grant lets rounds start.
	planGrantLife = 7 * 24 * time.Hour
	// claimTimeout: a round claimed and not started by then is abandoned.
	claimTimeout = 5 * time.Minute
	// planTimeout: a round whose model called no tool for this long after
	// its last call is over; what it did not hand back is unknown. Counted
	// from the last call (roundFor), not the first: one round plans both
	// 方案, eight calls or more, which took longer than 30 minutes from the
	// first. handoverTimeout still bounds the round as a whole.
	planTimeout = 20 * time.Minute
	// handoverTimeout: a round started is over by then, whatever its model
	// does. The planner gives a conversation an hour at most
	// (videocut-planner.py's TURN); past that nothing of it is left to call.
	handoverTimeout = 65 * time.Minute
	// notTakenAfter: a job that wanted a round this long without one says so.
	notTakenAfter = 10 * time.Minute
	// maxPlanRejections: this many answers refused in a row end the round,
	// which counts as one tried (maxPlanRounds).
	maxPlanRejections = 3
	// maxPlanRounds: a job's planning is tried this many rounds for the same
	// inputs; a round counts once it reached the planning and did not finish
	// it. After the last the job fails, says why, and the member plans it
	// again with one click (Replan; docs/decisions/2026-11-25-montage-planning-retries.md).
	maxPlanRounds = 3
	// After a round VNAS could not be reached for, the next waits 2, 4, 8
	// minutes, at most unreachableWaitMost; after one tried, errorWait
	// (docs/architecture/montage-planning-session.md, 重试).
	unreachableWaitFirst = 2 * time.Minute
	unreachableWaitMost  = 15 * time.Minute
	errorWait            = time.Minute
)

// Why a round stopped short of the model's planning (rounds.failure): what
// it needed could not be reached, which is tried again a few minutes later
// and never counts, or the planning itself failed, which counts as a round
// tried. The planner says which; a round that timed out before it started
// was not reached.
const (
	failureUnreachable = "unreachable"
	failureError       = "error"
)

// RoundComplete is how a round ends that ran to the model's last answer: the
// planner's result for a conversation that completed.
const RoundComplete = "complete"

// Round states.
const (
	RoundClaimed   = "claimed"
	RoundStarted   = "started"
	RoundEnded     = "ended"
	RoundAbandoned = "abandoned"
)

// The details a job waiting for VNAS shows (docs/specs/montage-center.md):
// what it waits for and, when it waits to try again, when that is.
const (
	DetailVNASOff   = "VNAS 没有开启"
	DetailGrantOld  = "规划授权已过期，请点“重新规划”"
	DetailNotTaken  = "VNAS 还没有接手"
	DetailPlanning  = "VNAS 正在规划"
	detailSeparator = ": "
)

// DetailUnreachable is the detail of a job that tries again in minutes after
// a round VNAS could not be reached for.
func DetailUnreachable(minutes int) string {
	return fmt.Sprintf("VNAS 暂时连不上，%d 分钟后再试", minutes)
}

// DetailFailed is the detail of a job that plans again in minutes after a
// round that did not finish its planning.
func DetailFailed(minutes int) string {
	return fmt.Sprintf("规划没有完成，%d 分钟后自动重来", minutes)
}

// planDetail is the detail an ErrPlanPending carries.
func planDetail(err error) string {
	_, detail, _ := strings.Cut(err.Error(), detailSeparator)
	return detail
}

func pending(detail string) error {
	if detail == "" {
		return domain.ErrPlanPending
	}
	return fmt.Errorf("%w%s%s", domain.ErrPlanPending, detailSeparator, detail)
}

// RegisterPlanGrant records that owner, signed in as username, just did
// something that may need planning with a planning grant registered for
// them (规划授权). The grant itself is the File Broker's; this only says
// whether to leave markers.
func (s *Store) RegisterPlanGrant(ctx context.Context, owner, username string) error {
	if !domain.AccountName.MatchString(username) {
		return domain.ErrInvalid
	}
	return s.db.RegisterPlanGrant(ctx, owner, username, s.now())
}

// planGrant is owner's last registration, if any.
func (s *Store) planGrant(ctx context.Context, q queryer, owner string) (string, time.Time, bool, error) {
	username, at, err := q.PlanGrant(ctx, owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	registered, err := time.Parse(time.RFC3339Nano, at)
	return username, registered, err == nil, err
}

// VNASPlanner is the planner that waits for the member's VNAS.
func (s *Store) VNASPlanner() Planner { return vnasPlanner{s} }

type vnasPlanner struct{ s *Store }

func (p vnasPlanner) Pick(ctx context.Context, input domain.PlanInput) (json.RawMessage, error) {
	return p.s.planned(ctx, input, StepOutline)
}

func (p vnasPlanner) Shots(ctx context.Context, input domain.PlanInput) (json.RawMessage, error) {
	return p.s.planned(ctx, input, StepShots)
}

// planned is the answer VNAS handed back for the step's input key, or why
// there is none yet. It runs as the job's step, where waiting for the
// compute Worker is ordinary: the step's brief is built here, before a round
// can ask for it, and the second step's answer is turned back into the
// outline's pictures here, so that no tool call waits behind the Worker's
// other work for either.
func (s *Store) planned(ctx context.Context, input domain.PlanInput, step string) (json.RawMessage, error) {
	if input.StepKey == "" || input.PickKey == "" {
		return nil, errors.New("the planning step has no input key")
	}
	briefKey := input.StepKey
	if step == StepOutline {
		// The outline's brief is the same for every 方案: kept under the
		// first's key, it is built once.
		briefKey = input.PickKey
	}
	brief, err := s.briefOf(ctx, input.JobID, step, briefKey, func() (domain.PlanInput, error) { return input, nil })
	if err != nil {
		return nil, err
	}
	answer, taken, err := s.planAnswer(ctx, input.JobID, step, input.StepKey)
	if err != nil {
		return nil, err
	}
	if !taken {
		return nil, s.awaitPlan(ctx, input.JobID, input.PickKey)
	}
	if step == StepOutline {
		return answer, nil
	}
	turned, err := s.jobCompute.ShotsAnswer(ctx, answer, input.Picked, brief.Pictures)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{"answer": turned, "slots": brief.Slots})
}

// planAnswer is the answer taken for a step's input key.
func (s *Store) planAnswer(ctx context.Context, jobID, step, key string) (json.RawMessage, bool, error) {
	answer, err := s.db.PlanAnswer(ctx, jobID, step, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return json.RawMessage(answer), err == nil, err
}

// shotsKey is the second step's input key: the first step's and the outline
// taken (steps.go). The tools work it out as the job's step does, so that an
// answer is kept under the key the step will look for.
func shotsKey(pickKey string, picked []byte) string {
	return digest(pickKey, digest(string(picked)), promptVersion)
}

// pastRound is one of the job's rounds for its epoch and inputs.
type pastRound struct {
	state, failure, outcome string
	ended                   time.Time
}

// pastRounds are the job's latest rounds for its epoch and inputs, the latest first.
func (s *Store) pastRounds(ctx context.Context, jobID string, epoch int, pickKey string) ([]pastRound, error) {
	kept, err := s.db.PastRounds(ctx, jobID, epoch, pickKey)
	if err != nil {
		return nil, err
	}
	var rounds []pastRound
	for _, k := range kept {
		round := pastRound{state: k.State, failure: k.Failure, outcome: k.Outcome}
		if at := scanTime(k.Ended); at != nil {
			round.ended = *at
		}
		rounds = append(rounds, round)
	}
	return rounds, nil
}

// unreachableWait is how long a job waits after its nth round in a row that
// could not reach VNAS: 2, 4, 8 minutes, then 15.
func unreachableWait(n int) time.Duration {
	return min(unreachableWaitFirst<<min(max(n, 1)-1, 4), unreachableWaitMost)
}

// tried reports whether a round reached the planning and left the job still
// waiting for it: it ended without what the job waits for (whatever stopped
// it, as the model may have been called), or the planning failed before its
// prompt went in. A round VNAS was not reached for was not tried.
func (r pastRound) tried() bool { return r.state == RoundEnded || r.failure == failureError }

// retryWait is how long the job still waits before VNAS is asked again
// after rounds that did not plan it, the latest first, and what it waits
// for: errorWait after a round tried, 2, 4, 8 then 15 minutes after rounds
// in a row that could not reach VNAS. Only rounds since the member last
// registered a planning grant, and since the VideoCut service started, make
// it wait: doing something that may need planning, or installing another
// version, tries again at once.
func (s *Store) retryWait(rounds []pastRound, registered time.Time) (time.Duration, string) {
	since := registered
	if s.opened.After(since) {
		since = s.opened
	}
	latest := rounds[0]
	if latest.ended.Before(since) {
		return 0, ""
	}
	wait, detail := errorWait, DetailFailed
	if !latest.tried() {
		unreached := 0
		for _, round := range rounds {
			if round.tried() || round.ended.Before(since) {
				break
			}
			unreached++
		}
		wait, detail = unreachableWait(unreached), DetailUnreachable
	}
	left := latest.ended.Add(wait).Sub(s.now())
	if left <= 0 {
		return 0, ""
	}
	return left, detail(int((left + time.Minute - 1) / time.Minute))
}

// planFailed: the planning was tried maxPlanRounds rounds and none finished
// it; said is how the last of them ended, as the planner or the round's end
// reported it. Its text is the job's error, which the page shows.
type planFailed struct {
	rounds int
	said   string
}

func (p *planFailed) Error() string {
	return fmt.Sprintf("规划试了 %d 轮都没有完成：%s", p.rounds, cmp.Or(p.said, "没有交回规划"))
}
func (p *planFailed) Unwrap() error { return domain.ErrPlanFailed }

// awaitPlan says why the job's step has no plan yet: a round runs, VNAS has
// not taken it, or it waits to plan again; or, once the planning was tried
// maxPlanRounds rounds for these inputs and none finished it, that it failed.
// A job that may have a round wants one and leaves its owner's marker; one
// that may not wants none. pickKey is the first step's input key, which a
// round plans for.
func (s *Store) awaitPlan(ctx context.Context, jobID, pickKey string) error {
	owner, epoch, wanted, err := s.db.JobPlanning(ctx, jobID)
	if err != nil {
		return err
	}
	// A round that plans for other inputs, or an earlier epoch, is over:
	// what it hands back is not kept, and it does not hold up a new one.
	if err := s.db.EndOtherRounds(ctx, jobID, epoch, pickKey, s.now(), "the job's inputs changed"); err != nil {
		return err
	}
	unwanted := func(why error) error {
		if !wanted {
			return why
		}
		if err := s.db.UnwantRound(ctx, jobID); err != nil {
			return err
		}
		s.syncMarkers(ctx)
		return why
	}
	rounds, err := s.pastRounds(ctx, jobID, epoch, pickKey)
	if err != nil {
		return err
	}
	username, registered, granted, err := s.planGrant(ctx, s.db, owner)
	if err != nil {
		return err
	}
	if len(rounds) > 0 {
		if latest := rounds[0]; latest.state == RoundClaimed || latest.state == RoundStarted {
			return unwanted(pending(DetailPlanning))
		}
		// Only the member can change what a temporary fault or the model's
		// mistakes did not: those are planned again on their own, a round at
		// a time, and after the last the job fails rather than waiting for
		// the member (2026-11-25; it no longer stops at the first round that
		// may have called the model, #514).
		tried, said := 0, ""
		for _, round := range rounds {
			if round.tried() {
				tried++
				if said == "" {
					said = round.outcome
				}
			}
		}
		if tried >= maxPlanRounds {
			return unwanted(&planFailed{rounds: tried, said: said})
		}
		if wait, detail := s.retryWait(rounds, registered); wait > 0 {
			return unwanted(pending(detail))
		}
	}
	switch {
	case !granted:
		return unwanted(pending(DetailVNASOff))
	case s.now().Sub(registered) > planGrantLife:
		return unwanted(pending(DetailGrantOld))
	}
	since, err := s.db.WantRound(ctx, jobID, s.now())
	if err != nil {
		return err
	}
	if err := s.leaveMarker(username); err != nil {
		s.logger.WarnContext(ctx, "leaving the planning marker failed", "job", jobID, "error", err)
	}
	if at := scanTime(sql.NullString{String: since, Valid: since != ""}); at != nil && s.now().Sub(*at) >= notTakenAfter {
		return pending(DetailNotTaken)
	}
	return pending("")
}

// --- markers ---

func (s *Store) leaveMarker(username string) error {
	if s.markers == nil {
		return nil
	}
	return s.markers.Leave(username)
}

func (s *Store) hasMarker(username string) bool {
	return s.markers != nil && s.markers.Has(username)
}

// SyncMarkers makes the markers say which members have a job wanting a
// round: at start, as the run directory is empty after a restart, and
// whenever a job stops wanting one.
func (s *Store) SyncMarkers(ctx context.Context) error {
	if s.markers == nil {
		return nil
	}
	wanted, err := s.db.MarkedUsernames(ctx, WaitVNAS, s.now().Add(-planGrantLife))
	if err != nil {
		return err
	}
	return s.markers.Keep(wanted)
}

// syncMarkers is SyncMarkers for where a failure only delays: the planner
// would claim a round that finds nothing, which syncs them again.
func (s *Store) syncMarkers(ctx context.Context) {
	if err := s.SyncMarkers(ctx); err != nil {
		s.logger.WarnContext(ctx, "syncing the planning markers failed", "error", err)
	}
}

// --- rounds ---

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ClaimRound starts a round for owner's earliest job that wants one, while
// the member's marker is there. The round plans for the job's epoch and the
// first step's input key as they are now. A claim that finds no such job
// leaves no marker behind: the planner would claim for it again and again.
func (s *Store) ClaimRound(ctx context.Context, owner string) (domain.Claim, error) {
	var claim domain.Claim
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		username, _, granted, err := s.planGrant(ctx, tx, owner)
		if err != nil {
			return err
		}
		if !granted || !s.hasMarker(username) {
			return domain.ErrNoPlanWanted
		}
		var epoch int
		var pickKey, settings string
		claim.JobID, epoch, pickKey, settings, err = tx.ClaimableJob(ctx, owner, WaitVNAS)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNoPlanWanted
		}
		if err != nil {
			return err
		}
		excerpt, err := tx.DoneExcerpt(ctx, claim.JobID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		claim.TargetSeconds = domain.ExcerptSeconds(excerpt.String)
		var kept domain.JobSettings
		if json.Unmarshal([]byte(settings), &kept) == nil {
			claim.Project = kept.ProjectName
		}
		claim.RoundID, claim.AttemptToken, claim.Template = newID("round"), newToken(), templateOf(kept)
		if err := tx.AddRound(ctx, claim.RoundID, claim.JobID, owner, claim.AttemptToken, s.now(), epoch, claim.Template, pickKey); err != nil {
			return err
		}
		return tx.UnwantRound(ctx, claim.JobID)
	})
	// Whether a round was claimed or none was wanted, the markers follow the
	// jobs: the member's is gone unless another of their jobs wants a round.
	if err == nil || errors.Is(err, domain.ErrNoPlanWanted) {
		s.syncMarkers(ctx)
	}
	if err != nil {
		return domain.Claim{}, err
	}
	s.logger.InfoContext(ctx, "planning round claimed", "round", claim.RoundID, "job", claim.JobID)
	return claim, nil
}

func (s *Store) round(ctx context.Context, tx *db.Tx, owner, roundID string) (db.RoundRow, error) {
	r, err := tx.Round(ctx, owner, roundID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.RoundRow{}, domain.ErrNotFound
	}
	return r, err
}

// newPlanEpoch starts the job's planning over: the rounds before it no
// longer say the plan is unknown, a round in progress is over, and the
// refusals so far are forgotten.
func (s *Store) newPlanEpoch(ctx context.Context, tx *db.Tx, jobID, why string) error {
	if err := tx.NewPlanEpoch(ctx, jobID); err != nil {
		return err
	}
	return tx.EndRounds(ctx, jobID, s.now(), why)
}

// StartRound records that the round was handed to the member's planning
// instance as the conversation runRef, before its prompt goes in; a round
// abandoned or over is refused, and the planner hands nothing to the model.
func (s *Store) StartRound(ctx context.Context, owner, roundID, runRef string) error {
	if len(runRef) > 128 {
		return domain.ErrInvalid
	}
	return s.db.Update(ctx, func(tx *db.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		cancelled, err := tx.CancelRequested(ctx, r.JobID)
		if err != nil {
			return err
		}
		if r.State != RoundClaimed || cancelled {
			return domain.ErrRoundOver
		}
		return tx.StartRound(ctx, roundID, runRef, s.now())
	})
}

// AbandonRound records that the round stopped before its prompt reached the
// model, and why: failure says whether what it needed could not be reached
// (failureUnreachable, also for anything else the planner says) or the
// planning failed (failureError). The job waits for another round
// (retryWait).
func (s *Store) AbandonRound(ctx context.Context, owner, roundID, reason, failure string) error {
	if failure != failureError {
		failure = failureUnreachable
	}
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		if r.State != RoundClaimed {
			return domain.ErrRoundOver
		}
		if err := tx.AbandonRound(ctx, roundID, s.now(), truncate(reason, 500), failure); err != nil {
			return err
		}
		return s.dueNow(ctx, tx, r.JobID)
	})
	if err == nil {
		s.wakeJobs()
	}
	return err
}

// EndRound records how the round's conversation ended: result is
// RoundComplete for one that ran to the model's last answer, else what
// stopped it (an error, a timeout, the conversation cut off). One that
// stopped before the model called any videocut tool failed (failureError):
// whether the model had begun is unknown, but it planned nothing. Either
// way a round that left the job without its plan counts as one tried, and
// the job plans again on its own (awaitPlan). A round reported before it
// started is abandoned.
func (s *Store) EndRound(ctx context.Context, owner, roundID, outcome, result string) error {
	failure := ""
	if result != "" && result != RoundComplete {
		failure = failureError
	}
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		now := s.now()
		switch r.State {
		case RoundClaimed:
			err = tx.AbandonRound(ctx, roundID, now, truncate(outcome, 500), cmp.Or(failure, failureUnreachable))
		case RoundStarted:
			err = tx.EndStartedRound(ctx, roundID, now, truncate(outcome, 500), failure)
		default:
			err = tx.MarkRoundReported(ctx, roundID, now)
		}
		if err != nil {
			return err
		}
		return s.dueNow(ctx, tx, r.JobID)
	})
	if err == nil {
		s.wakeJobs()
	}
	return err
}

// SweepRounds ends the rounds that ran out of time: claimed and not started
// in five minutes (abandoned, no model called, as if VNAS was not reached);
// started and not ended twenty minutes after the model's last call, or
// whatever the model does once the planner's own limit is over (what they
// did not hand back is unknown).
func (s *Store) SweepRounds(ctx context.Context) error {
	now := s.now()
	claimed, started, called := now.Add(-claimTimeout), now.Add(-handoverTimeout), now.Add(-planTimeout)
	var swept int64
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		if err := tx.DueLateRoundJobs(ctx, now, claimed, started, called); err != nil {
			return err
		}
		var err error
		swept, err = tx.EndLateRounds(ctx, now, "out of time", claimed, started, called)
		return err
	})
	if err != nil {
		return err
	}
	if swept > 0 {
		s.wakeJobs()
	}
	return nil
}

// truncate keeps at most most bytes of text, whole characters only.
func truncate(text string, most int) string {
	if len(text) <= most {
		return text
	}
	cut := most
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// dueNow makes a job waiting for its plan due at once: the round it waited
// on moved.
func (s *Store) dueNow(ctx context.Context, tx *db.Tx, jobID string) error {
	return tx.MakeWaitingJobDue(ctx, jobID, s.now())
}
