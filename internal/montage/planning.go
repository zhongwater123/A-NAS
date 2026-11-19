package montage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Planning by the member's own VNAS (docs/architecture/montage-center.md,
// 规划): a job that comes to planning waits for VNAS and leaves a marker,
// /run/a-nas-montage/plans/<account>; the waker starts the member's cron
// unit, whose round script claims a round here, hands the round's prompt
// to Hermes, and reports that it started and ended. The model reads the
// briefs and hands the two steps back through the montage tools (briefs.go);
// each answer is checked there and the first good one kept for the input key
// of the step it answers. The job's own step then takes it into its plan
// (steps.go), so an answer for inputs the job no longer has is never used.

// PlanTemplate is the version of the prompt template the round script
// fills (ai/anas_montage/prompts/); a claim answers it, and a round script
// that has another refuses the round.
const PlanTemplate = "plan-v2"

const (
	// planGrantLife is how long a planning grant lets rounds start.
	planGrantLife = 7 * 24 * time.Hour
	// claimTimeout: a round claimed and not started by then is abandoned.
	claimTimeout = 5 * time.Minute
	// planTimeout: a round whose model began to plan, which its first call
	// of a tool shows, and that did not end by then is over; what it did
	// not hand back is unknown.
	planTimeout = 30 * time.Minute
	// handoverTimeout: a round handed to Hermes whose model never called a
	// tool is over by then. Hermes may run the member's own due jobs first,
	// in the same round of the unit, which lasts an hour at most
	// (TimeoutStartSec=1h, cron_round.py's ROUND); past that nothing of it
	// is left to call.
	handoverTimeout = 65 * time.Minute
	// notTakenAfter: a job that wanted a round this long without one says so.
	notTakenAfter = 10 * time.Minute
	// maxPlanRejections: this many answers refused in a row and the
	// member is asked.
	maxPlanRejections = 3
	// planBackoffMost bounds the wait after an abandoned round.
	planBackoffMost = 30 * time.Minute
)

// Round states.
const (
	RoundClaimed   = "claimed"
	RoundStarted   = "started"
	RoundEnded     = "ended"
	RoundAbandoned = "abandoned"
)

// Planning errors the round script and the tools are told.
var (
	// ErrNoPlanWanted: no job of the member waits for a round.
	ErrNoPlanWanted = errors.New("no job waits for planning")
	// ErrRoundOver: the round was abandoned, ended or timed out.
	ErrRoundOver = errors.New("this round is over")
	// ErrJobCancelled: the job was cancelled; the round should stop.
	ErrJobCancelled = errors.New("the job was cancelled")
)

// The details a job waiting for VNAS shows (docs/specs/montage-center.md).
const (
	DetailVNASOff   = "VNAS 没有开启"
	DetailGrantOld  = "规划授权已过期，请点“重新规划”"
	DetailNotTaken  = "VNAS 还没有接手"
	DetailRetrying  = "VNAS 这一轮没有开始，稍后再试"
	detailSeparator = ": "
)

// planDetail is the detail an ErrPlanPending carries.
func planDetail(err error) string {
	_, detail, _ := strings.Cut(err.Error(), detailSeparator)
	return detail
}

func pending(detail string) error {
	if detail == "" {
		return ErrPlanPending
	}
	return fmt.Errorf("%w%s%s", ErrPlanPending, detailSeparator, detail)
}

// accountName is what the waker accepts as an account's name: the marker
// is a file named by it.
var accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// RegisterPlanGrant records that owner, signed in as username, just did
// something that may need planning with a planning grant registered for
// them (规划授权). The grant itself is the File Broker's; this only says
// whether to leave markers.
func (s *Store) RegisterPlanGrant(ctx context.Context, owner, username string) error {
	if !accountName.MatchString(username) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plan_grants(owner_user_id, username, registered_at) VALUES(?, ?, ?)
		ON CONFLICT(owner_user_id) DO UPDATE SET username = excluded.username, registered_at = excluded.registered_at`,
		owner, username, sqlitedb.FormatTime(s.now()))
	return err
}

// planGrant is owner's last registration, if any.
func (s *Store) planGrant(ctx context.Context, q queryer, owner string) (string, time.Time, bool, error) {
	var username, at string
	err := q.QueryRowContext(ctx, "SELECT username, registered_at FROM plan_grants WHERE owner_user_id = ?", owner).Scan(&username, &at)
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

func (p vnasPlanner) Pick(ctx context.Context, input PlanInput) (json.RawMessage, error) {
	return p.s.planned(ctx, input, StepOutline)
}

func (p vnasPlanner) Shots(ctx context.Context, input PlanInput) (json.RawMessage, error) {
	return p.s.planned(ctx, input, StepShots)
}

// planned is the answer VNAS handed back for the step's input key, or why
// there is none yet. It runs as the job's step, where waiting for the
// compute Worker is ordinary: the step's brief is built here, before a round
// can ask for it, and the second step's answer is turned back into the
// outline's pictures here, so that no tool call waits behind the Worker's
// other work for either.
func (s *Store) planned(ctx context.Context, input PlanInput, step string) (json.RawMessage, error) {
	if input.StepKey == "" || input.PickKey == "" {
		return nil, errors.New("the planning step has no input key")
	}
	brief, err := s.briefOf(ctx, input.JobID, step, input.StepKey, func() (PlanInput, error) { return input, nil })
	if err != nil {
		return nil, err
	}
	answer, taken, err := s.planAnswer(ctx, s.db, input.JobID, step, input.StepKey)
	if err != nil {
		return nil, err
	}
	if !taken {
		return nil, s.awaitPlan(ctx, input.JobID, step, input.PickKey)
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
func (s *Store) planAnswer(ctx context.Context, q queryer, jobID, step, key string) (json.RawMessage, bool, error) {
	var answer string
	err := q.QueryRowContext(ctx, "SELECT answer_json FROM plan_answers WHERE job_id = ? AND step = ? AND input_key = ?",
		jobID, step, key).Scan(&answer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return json.RawMessage(answer), err == nil, err
}

// currentPick is, in SQL, the first step of the 方案 that job j is at: the
// latest there is, as each 方案's steps start once the one before is saved,
// and a changed input removes the steps after it.
const currentPick = `(SELECT name FROM steps WHERE job_id = j.id AND (name = 'pick' OR name LIKE 'pick-%') ORDER BY length(name) DESC, name DESC LIMIT 1)`

// shotsKey is the second step's input key: the first step's and the outline
// taken (steps.go). The tools work it out as the job's step does, so that an
// answer is kept under the key the step will look for.
func shotsKey(pickKey string, picked []byte) string {
	return digest(pickKey, digest(string(picked)), promptVersion)
}

// lastRound is the job's latest round for its epoch and inputs.
type lastRound struct {
	id, state     string
	ended         time.Time
	abandonedRuns int
}

func (s *Store) latestRound(ctx context.Context, jobID string, epoch int, pickKey string) (lastRound, bool, error) {
	var round lastRound
	var ended sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, state, ended_at FROM rounds WHERE job_id = ? AND epoch = ? AND input_key = ?
		ORDER BY claimed_at DESC, id DESC LIMIT 1`, jobID, epoch, pickKey).Scan(&round.id, &round.state, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return lastRound{}, false, nil
	}
	if err != nil {
		return lastRound{}, false, err
	}
	if at := scanTime(ended); at != nil {
		round.ended = *at
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rounds WHERE job_id = ? AND epoch = ? AND input_key = ? AND state = 'abandoned'",
		jobID, epoch, pickKey).Scan(&round.abandonedRuns); err != nil {
		return lastRound{}, false, err
	}
	return round, true, nil
}

// planBackoff is how long after its nth abandoned round a job waits before
// it asks again: 1, 2, 4… minutes, at most 30.
func planBackoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	delay := time.Minute << min(n-1, 5)
	return min(delay, planBackoffMost)
}

// awaitPlan says why the job's step has no plan yet: a round runs, VNAS has
// not taken it, or it needs the member, when a round ended without the step
// or the answers kept failing their checks. A job that may have a round
// wants one and leaves its owner's marker; one that may not wants none.
// pickKey is the first step's input key, which a round plans for.
func (s *Store) awaitPlan(ctx context.Context, jobID, step, pickKey string) error {
	var owner string
	var rejections, epoch int
	var wanted bool
	if err := s.db.QueryRowContext(ctx, "SELECT owner_user_id, plan_rejections, plan_epoch, plan_wanted FROM jobs WHERE id = ?", jobID).
		Scan(&owner, &rejections, &epoch, &wanted); err != nil {
		return err
	}
	// A round that plans for other inputs, or an earlier epoch, is over:
	// what it hands back is not kept, and it does not hold up a new one.
	if _, err := s.db.ExecContext(ctx, endRound+` WHERE job_id = ? AND state IN ('claimed', 'started') AND (epoch <> ? OR input_key <> ?)`,
		sqlitedb.FormatTime(s.now()), "the job's inputs changed", jobID, epoch, pickKey); err != nil {
		return err
	}
	unwanted := func(why error) error {
		if !wanted {
			return why
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", jobID); err != nil {
			return err
		}
		s.syncMarkers(ctx)
		return why
	}
	if rejections >= maxPlanRejections {
		return unwanted(ErrPlanInvalid)
	}
	round, found, err := s.latestRound(ctx, jobID, epoch, pickKey)
	if err != nil {
		return err
	}
	if found {
		switch round.state {
		case RoundClaimed, RoundStarted:
			return unwanted(pending(""))
		case RoundEnded:
			// The model may have been called: never started again on its own (#514).
			return unwanted(fmt.Errorf("%w: the round ended without the %s step", ErrPlanUnknown, step))
		case RoundAbandoned:
			// From when the round was abandoned: one that timed out was
			// abandoned five minutes after its claim.
			if wait := planBackoff(round.abandonedRuns); s.now().Before(round.ended.Add(wait)) {
				return unwanted(pending(DetailRetrying))
			}
		}
	}
	username, registered, granted, err := s.planGrant(ctx, s.db, owner)
	if err != nil {
		return err
	}
	switch {
	case !granted:
		return unwanted(pending(DetailVNASOff))
	case s.now().Sub(registered) > planGrantLife:
		return unwanted(pending(DetailGrantOld))
	}
	var since string
	if err := s.db.QueryRowContext(ctx, `UPDATE jobs SET plan_wanted_at = CASE WHEN plan_wanted = 1 AND plan_wanted_at IS NOT NULL THEN plan_wanted_at ELSE ? END, plan_wanted = 1
		WHERE id = ? RETURNING IFNULL(plan_wanted_at, '')`, sqlitedb.FormatTime(s.now()), jobID).Scan(&since); err != nil {
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

func (s *Store) markerPath(username string) string { return filepath.Join(s.planMarkers, username) }

func (s *Store) leaveMarker(username string) error {
	if s.planMarkers == "" || !accountName.MatchString(username) {
		return nil
	}
	file, err := os.OpenFile(s.markerPath(username), os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *Store) hasMarker(username string) bool {
	if s.planMarkers == "" || !accountName.MatchString(username) {
		return false
	}
	_, err := os.Stat(s.markerPath(username))
	return err == nil
}

// wantsRound is what makes a job one a round may be claimed for: it came to
// planning, waits there for VNAS, and asked for a round. A job put back in
// the queue, by replanning or by taking videos out, wants none until it
// comes to planning again
// (resume). It reads the jobs as j.
const wantsRound = "j.plan_wanted = 1 AND j.state = 'waiting' AND j.wait_reason = '" + WaitVNAS + "' AND j.cancel_requested = 0"

// keepsMarker is what keeps a member's marker: a job that wants a round, or
// one that wanted it and is looking again right now (a waiting job runs for
// an instant each time it looks, and leaves its marker as it goes back to
// waiting). Without the second, a sync in that instant would take the marker
// of a job that still waits.
const keepsMarker = "j.plan_wanted = 1 AND j.cancel_requested = 0 AND (j.state = 'running' OR (j.state = 'waiting' AND j.wait_reason = '" + WaitVNAS + "'))"

// SyncMarkers makes the markers say which members have a job wanting a
// round: at start, as the run directory is empty after a restart, and
// whenever a job stops wanting one.
func (s *Store) SyncMarkers(ctx context.Context) error {
	if s.planMarkers == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT g.username FROM jobs j JOIN plan_grants g ON g.owner_user_id = j.owner_user_id
		WHERE `+keepsMarker+` AND g.registered_at >= ?`,
		sqlitedb.FormatTime(s.now().Add(-planGrantLife)))
	if err != nil {
		return err
	}
	defer rows.Close()
	wanted := map[string]bool{}
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return err
		}
		wanted[username] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.planMarkers)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			_ = os.Remove(filepath.Join(s.planMarkers, entry.Name()))
		}
	}
	for username := range wanted {
		if err := s.leaveMarker(username); err != nil {
			return err
		}
	}
	return nil
}

// syncMarkers is SyncMarkers for where a failure only delays: the waker
// would start a round that claims nothing, which syncs them again.
func (s *Store) syncMarkers(ctx context.Context) {
	if err := s.SyncMarkers(ctx); err != nil {
		s.logger.WarnContext(ctx, "syncing the planning markers failed", "error", err)
	}
}

// --- rounds ---

// Claim is what a claimed round tells the round script.
type Claim struct {
	RoundID       string `json:"roundId"`
	JobID         string `json:"jobId"`
	AttemptToken  string `json:"attemptToken"`
	Template      string `json:"template"`
	TargetSeconds int    `json:"targetSeconds"`
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ClaimRound starts a round for owner's earliest job that wants one, while
// the waker's marker is there. The round plans for the job's epoch and the
// first step's input key as they are now. A claim that finds no such job
// leaves no marker behind: the waker would start the member's unit for it
// again and again.
func (s *Store) ClaimRound(ctx context.Context, owner string) (Claim, error) {
	var claim Claim
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		username, _, granted, err := s.planGrant(ctx, tx, owner)
		if err != nil {
			return err
		}
		if !granted || !s.hasMarker(username) {
			return ErrNoPlanWanted
		}
		var epoch int
		var pickKey string
		err = tx.QueryRowContext(ctx, `SELECT j.id, j.plan_epoch, p.input_key FROM jobs j JOIN steps p ON p.job_id = j.id AND p.name = `+currentPick+`
			WHERE j.owner_user_id = ? AND `+wantsRound+`
			AND NOT EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.state IN ('claimed', 'started'))
			ORDER BY j.created_at, j.id LIMIT 1`, owner).Scan(&claim.JobID, &epoch, &pickKey)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoPlanWanted
		}
		if err != nil {
			return err
		}
		var excerpt sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT result_json FROM steps WHERE job_id = ? AND name = 'song' AND state = 'done'",
			claim.JobID).Scan(&excerpt); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		claim.TargetSeconds = excerptSeconds(excerpt.String)
		claim.RoundID, claim.AttemptToken, claim.Template = newID("round"), newToken(), PlanTemplate
		now := sqlitedb.FormatTime(s.now())
		if _, err := tx.ExecContext(ctx, `INSERT INTO rounds(id, job_id, owner_user_id, attempt_token, state, claimed_at, epoch, template, input_key)
			VALUES(?, ?, ?, ?, 'claimed', ?, ?, ?, ?)`, claim.RoundID, claim.JobID, owner, claim.AttemptToken, now, epoch, PlanTemplate, pickKey); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", claim.JobID)
		return err
	})
	// Whether a round was claimed or none was wanted, the markers follow the
	// jobs: the member's is gone unless another of their jobs wants a round.
	if err == nil || errors.Is(err, ErrNoPlanWanted) {
		s.syncMarkers(ctx)
	}
	if err != nil {
		return Claim{}, err
	}
	s.logger.InfoContext(ctx, "planning round claimed", "round", claim.RoundID, "job", claim.JobID)
	return claim, nil
}

// excerptSeconds is the target length of the film in whole seconds, from
// the song step's stored excerpt.
func excerptSeconds(stored string) int {
	var excerpt Excerpt
	if stored == "" || json.Unmarshal([]byte(stored), &excerpt) != nil || excerpt.Frames <= 0 {
		return 0
	}
	return (excerpt.Frames + FilmRate/2) / FilmRate
}

// round is a round as the operations on it read it.
type round struct {
	id, jobID, owner, token, state string
}

func (s *Store) round(ctx context.Context, q queryer, owner, roundID string) (round, error) {
	var r round
	err := q.QueryRowContext(ctx, "SELECT id, job_id, owner_user_id, attempt_token, state FROM rounds WHERE id = ? AND owner_user_id = ?",
		roundID, owner).Scan(&r.id, &r.jobID, &r.owner, &r.token, &r.state)
	if errors.Is(err, sql.ErrNoRows) {
		return round{}, ErrNotFound
	}
	return r, err
}

// endRound ends rounds: one only claimed never reached the model and is
// abandoned, one started is over. Its arguments are the time and the
// outcome; the rounds it ends follow.
const endRound = `UPDATE rounds SET ended_at = ?, outcome = ?, state = CASE WHEN state = 'claimed' THEN 'abandoned' ELSE 'ended' END`

// endRounds ends the job's rounds in progress, whatever they plan for.
// Their Hermes jobs are the next round's to remove (StaleRounds).
func endRounds(ctx context.Context, q queryer, jobID string, at time.Time, outcome string) error {
	_, err := q.ExecContext(ctx, endRound+" WHERE job_id = ? AND state IN ('claimed', 'started')", sqlitedb.FormatTime(at), outcome, jobID)
	return err
}

// newPlanEpoch starts the job's planning over: the rounds before it no
// longer say the plan is unknown, a round in progress is over, and the
// refusals so far are forgotten.
func (s *Store) newPlanEpoch(ctx context.Context, tx *sql.Tx, jobID, why string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0, plan_epoch = plan_epoch + 1 WHERE id = ?", jobID); err != nil {
		return err
	}
	return endRounds(ctx, tx, jobID, s.now(), why)
}

// StartRound records that the round's job was handed to Hermes, as runRef;
// a round abandoned or over is refused, and the script removes the job.
func (s *Store) StartRound(ctx context.Context, owner, roundID, runRef string) error {
	if len(runRef) > 128 {
		return ErrInvalid
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		var cancelled bool
		if err := tx.QueryRowContext(ctx, "SELECT cancel_requested FROM jobs WHERE id = ?", r.jobID).Scan(&cancelled); err != nil {
			return err
		}
		if r.state != RoundClaimed || cancelled {
			return ErrRoundOver
		}
		_, err = tx.ExecContext(ctx, "UPDATE rounds SET state = 'started', run_ref = ?, started_at = ? WHERE id = ?",
			runRef, sqlitedb.FormatTime(s.now()), roundID)
		return err
	})
}

// AbandonRound records that the round stopped before Hermes had it: no
// model was called, and the job waits for another round after a backoff.
func (s *Store) AbandonRound(ctx context.Context, owner, roundID, reason string) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		if r.state != RoundClaimed {
			return ErrRoundOver
		}
		now := sqlitedb.FormatTime(s.now())
		if _, err := tx.ExecContext(ctx, "UPDATE rounds SET state = 'abandoned', ended_at = ?, reported_at = ?, outcome = ? WHERE id = ?",
			now, now, truncate(reason, 500), roundID); err != nil {
			return err
		}
		return s.dueNow(ctx, tx, r.jobID)
	})
	if err == nil {
		s.wakeJobs()
	}
	return err
}

// EndRound records how the round's run ended in Hermes. A round reported
// before it started is abandoned.
func (s *Store) EndRound(ctx context.Context, owner, roundID, outcome string) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.round(ctx, tx, owner, roundID)
		if err != nil {
			return err
		}
		now := sqlitedb.FormatTime(s.now())
		switch r.state {
		case RoundClaimed:
			_, err = tx.ExecContext(ctx, "UPDATE rounds SET state = 'abandoned', ended_at = ?, reported_at = ?, outcome = ? WHERE id = ?",
				now, now, truncate(outcome, 500), roundID)
		case RoundStarted:
			_, err = tx.ExecContext(ctx, "UPDATE rounds SET state = 'ended', ended_at = ?, reported_at = ?, outcome = ? WHERE id = ?",
				now, now, truncate(outcome, 500), roundID)
		default:
			_, err = tx.ExecContext(ctx, "UPDATE rounds SET reported_at = COALESCE(reported_at, ?) WHERE id = ?", now, roundID)
		}
		if err != nil {
			return err
		}
		return s.dueNow(ctx, tx, r.jobID)
	})
	if err == nil {
		s.wakeJobs()
	}
	return err
}

// StaleRound is a round whose run may have left a job and a session in
// Hermes: claimed by an earlier run of the unit, never reported.
type StaleRound struct {
	RoundID string `json:"roundId"`
	RunRef  string `json:"runRef,omitempty"`
}

// StaleRounds lists owner's rounds no round script reported the end of.
func (s *Store) StaleRounds(ctx context.Context, owner string) ([]StaleRound, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, run_ref FROM rounds WHERE owner_user_id = ? AND reported_at IS NULL ORDER BY claimed_at", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stale := []StaleRound{}
	for rows.Next() {
		var r StaleRound
		if err := rows.Scan(&r.RoundID, &r.RunRef); err != nil {
			return nil, err
		}
		stale = append(stale, r)
	}
	return stale, rows.Err()
}

// CleanedRounds records that the round script removed these rounds' jobs
// and sessions: a round still claimed is abandoned, one started is over.
func (s *Store) CleanedRounds(ctx context.Context, owner string, roundIDs []string) error {
	for _, id := range roundIDs {
		if err := s.EndRound(ctx, owner, id, "cleaned up by a later round"); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// SweepRounds ends the rounds that ran out of time: claimed and not started
// in five minutes (abandoned, no model called); started and not ended thirty
// minutes after the model's first call, or with no call at all once the
// unit's own round is over (what they did not hand back is unknown).
func (s *Store) SweepRounds(ctx context.Context) error {
	now := s.now()
	late := `(state = 'claimed' AND claimed_at <= ?) OR (state = 'started' AND (started_at <= ? OR called_at <= ?))`
	bounds := []any{sqlitedb.FormatTime(now.Add(-claimTimeout)), sqlitedb.FormatTime(now.Add(-handoverTimeout)), sqlitedb.FormatTime(now.Add(-planTimeout))}
	var swept int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET not_before = ? WHERE state = 'waiting' AND id IN (SELECT job_id FROM rounds WHERE `+late+")",
			append([]any{sqlitedb.FormatTime(now)}, bounds...)...); err != nil {
			return err
		}
		ended, err := tx.ExecContext(ctx, endRound+" WHERE "+late, append([]any{sqlitedb.FormatTime(now), "out of time"}, bounds...)...)
		if err != nil {
			return err
		}
		swept, _ = ended.RowsAffected()
		return nil
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
func (s *Store) dueNow(ctx context.Context, q queryer, jobID string) error {
	_, err := q.ExecContext(ctx, "UPDATE jobs SET not_before = ? WHERE id = ? AND state = 'waiting'", sqlitedb.FormatTime(s.now()), jobID)
	return err
}
