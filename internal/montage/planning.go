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
// briefs and hands the two steps back through the montage tools; each
// answer is checked here and the first good one kept.

// PlanTemplate is the version of the prompt template the round script
// fills (ai/anas_montage/prompts/); a claim answers it, and a round script
// that has another refuses the round.
const PlanTemplate = "plan-v1"

const (
	// planGrantLife is how long a planning grant lets rounds start.
	planGrantLife = 7 * 24 * time.Hour
	// claimTimeout: a round claimed and not started by then is abandoned.
	claimTimeout = 5 * time.Minute
	// roundTimeout: a round started and not ended by then is over, and
	// what it did not hand back is unknown.
	roundTimeout = 30 * time.Minute
	// notTakenAfter: a job waiting this long without a round says so.
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
func (s *Store) planGrant(ctx context.Context, owner string) (string, time.Time, bool, error) {
	var username, at string
	err := s.db.QueryRowContext(ctx, "SELECT username, registered_at FROM plan_grants WHERE owner_user_id = ?", owner).Scan(&username, &at)
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
	return nil, p.s.awaitPlan(ctx, input.JobID, "pick")
}

func (p vnasPlanner) Shots(ctx context.Context, input PlanInput) (json.RawMessage, error) {
	return nil, p.s.awaitPlan(ctx, input.JobID, "shots")
}

// lastRound is the job's latest round in its current epoch.
type lastRound struct {
	id, state     string
	claimed       time.Time
	started       bool
	abandonedRuns int
}

func (s *Store) latestRound(ctx context.Context, jobID string, epoch int) (lastRound, bool, error) {
	var round lastRound
	var claimed string
	var started sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, state, claimed_at, started_at FROM rounds WHERE job_id = ? AND epoch = ?
		ORDER BY claimed_at DESC, id DESC LIMIT 1`, jobID, epoch).Scan(&round.id, &round.state, &claimed, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return lastRound{}, false, nil
	}
	if err != nil {
		return lastRound{}, false, err
	}
	at := scanTime(sql.NullString{String: claimed, Valid: true})
	if at != nil {
		round.claimed = *at
	}
	round.started = started.Valid
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rounds WHERE job_id = ? AND epoch = ? AND state = 'abandoned'",
		jobID, epoch).Scan(&round.abandonedRuns); err != nil {
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

// awaitPlan answers whether the job's step can be planned now: it cannot
// while a round runs or before VNAS takes it, and it needs the member when
// a round ended without the step or the answers kept failing their checks.
// A job that may have a round leaves its owner's marker.
func (s *Store) awaitPlan(ctx context.Context, jobID, step string) error {
	var owner string
	var rejections, epoch int
	if err := s.db.QueryRowContext(ctx, "SELECT owner_user_id, plan_rejections, plan_epoch FROM jobs WHERE id = ?", jobID).
		Scan(&owner, &rejections, &epoch); err != nil {
		return err
	}
	if rejections >= maxPlanRejections {
		return ErrPlanInvalid
	}
	round, found, err := s.latestRound(ctx, jobID, epoch)
	if err != nil {
		return err
	}
	if found {
		switch round.state {
		case RoundClaimed, RoundStarted:
			return pending("")
		case RoundEnded:
			// The model may have been called: never started again on its own (#514).
			return fmt.Errorf("%w: the round ended without the %s step", ErrPlanUnknown, step)
		case RoundAbandoned:
			if wait := planBackoff(round.abandonedRuns); s.now().Before(round.claimed.Add(wait)) {
				return pending(DetailRetrying)
			}
		}
	}
	username, registered, granted, err := s.planGrant(ctx, owner)
	if err != nil {
		return err
	}
	switch {
	case !granted:
		return pending(DetailVNASOff)
	case s.now().Sub(registered) > planGrantLife:
		return pending(DetailGrantOld)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 1 WHERE id = ?", jobID); err != nil {
		return err
	}
	if err := s.leaveMarker(username); err != nil {
		s.logger.WarnContext(ctx, "leaving the planning marker failed", "job", jobID, "error", err)
	}
	if waited := s.waitingSince(ctx, jobID); !waited.IsZero() && s.now().Sub(waited) >= notTakenAfter {
		return pending(DetailNotTaken)
	}
	return pending("")
}

// waitingSince is when the job last came to wait for VNAS: its last
// round's claim, or when the step it waits on started.
func (s *Store) waitingSince(ctx context.Context, jobID string) time.Time {
	var at sql.NullString
	_ = s.db.QueryRowContext(ctx, `SELECT MAX(t) FROM (
		SELECT MAX(claimed_at) AS t FROM rounds WHERE job_id = ?
		UNION ALL SELECT MAX(started_at) FROM steps WHERE job_id = ? AND name IN ('pick', 'shots'))`, jobID, jobID).Scan(&at)
	if t := scanTime(at); t != nil {
		return *t
	}
	return time.Time{}
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

func (s *Store) takeMarker(username string) bool {
	if s.planMarkers == "" || !accountName.MatchString(username) {
		return false
	}
	return os.Remove(s.markerPath(username)) == nil
}

func (s *Store) hasMarker(username string) bool {
	if s.planMarkers == "" || !accountName.MatchString(username) {
		return false
	}
	_, err := os.Stat(s.markerPath(username))
	return err == nil
}

// SyncMarkers makes the markers say which members have a job wanting a
// round: at start, as the run directory is empty after a restart, and
// whenever a job stops wanting one.
func (s *Store) SyncMarkers(ctx context.Context) error {
	if s.planMarkers == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT g.username FROM jobs j JOIN plan_grants g ON g.owner_user_id = j.owner_user_id
		WHERE j.plan_wanted = 1 AND j.state IN ('queued', 'running', 'waiting') AND g.registered_at >= ?`,
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
// the waker's marker is there, and takes the marker.
func (s *Store) ClaimRound(ctx context.Context, owner string) (Claim, error) {
	username, _, granted, err := s.planGrant(ctx, owner)
	if err != nil {
		return Claim{}, err
	}
	if !granted || !s.hasMarker(username) {
		return Claim{}, ErrNoPlanWanted
	}
	var claim Claim
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var epoch int
		err := tx.QueryRowContext(ctx, `SELECT j.id, j.plan_epoch FROM jobs j WHERE j.owner_user_id = ? AND j.plan_wanted = 1
			AND j.state IN ('queued', 'waiting') AND j.cancel_requested = 0
			AND NOT EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.state IN ('claimed', 'started'))
			ORDER BY j.created_at, j.id LIMIT 1`, owner).Scan(&claim.JobID, &epoch)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO rounds(id, job_id, owner_user_id, attempt_token, state, claimed_at, epoch, template)
			VALUES(?, ?, ?, ?, 'claimed', ?, ?, ?)`, claim.RoundID, claim.JobID, owner, claim.AttemptToken, now, epoch, PlanTemplate); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", claim.JobID)
		return err
	})
	if err != nil {
		return Claim{}, err
	}
	s.takeMarker(username)
	if err := s.SyncMarkers(ctx); err != nil {
		s.logger.WarnContext(ctx, "syncing the planning markers failed", "error", err)
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
	claimed                        time.Time
}

func (s *Store) round(ctx context.Context, q queryer, owner, roundID string) (round, error) {
	var r round
	var claimed string
	err := q.QueryRowContext(ctx, "SELECT id, job_id, owner_user_id, attempt_token, state, claimed_at FROM rounds WHERE id = ? AND owner_user_id = ?",
		roundID, owner).Scan(&r.id, &r.jobID, &r.owner, &r.token, &r.state, &claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return round{}, ErrNotFound
	}
	if err != nil {
		return round{}, err
	}
	if at := scanTime(sql.NullString{String: claimed, Valid: true}); at != nil {
		r.claimed = *at
	}
	return r, nil
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
// in five minutes (abandoned, no model called), started and not ended in
// thirty (over: what they did not hand back is unknown).
func (s *Store) SweepRounds(ctx context.Context) error {
	now := s.now()
	at := sqlitedb.FormatTime(now)
	if err := s.roundsDueNow(ctx, s.db, `(state = 'claimed' AND claimed_at <= ?) OR (state = 'started' AND started_at <= ?)`,
		sqlitedb.FormatTime(now.Add(-claimTimeout)), sqlitedb.FormatTime(now.Add(-roundTimeout))); err != nil {
		return err
	}
	claimed, err := s.db.ExecContext(ctx, `UPDATE rounds SET state = 'abandoned', ended_at = ?, outcome = 'not started in time'
		WHERE state = 'claimed' AND claimed_at <= ?`, at, sqlitedb.FormatTime(now.Add(-claimTimeout)))
	if err != nil {
		return err
	}
	started, err := s.db.ExecContext(ctx, `UPDATE rounds SET state = 'ended', ended_at = ?, outcome = 'not ended in time'
		WHERE state = 'started' AND started_at <= ?`, at, sqlitedb.FormatTime(now.Add(-roundTimeout)))
	if err != nil {
		return err
	}
	a, _ := claimed.RowsAffected()
	b, _ := started.RowsAffected()
	if a+b > 0 {
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

// roundsDueNow makes the jobs of rounds that just moved due at once.
func (s *Store) roundsDueNow(ctx context.Context, q queryer, condition string, args ...any) error {
	_, err := q.ExecContext(ctx, `UPDATE jobs SET not_before = ? WHERE state = 'waiting' AND id IN (SELECT job_id FROM rounds WHERE `+condition+")",
		append([]any{sqlitedb.FormatTime(s.now())}, args...)...)
	return err
}
