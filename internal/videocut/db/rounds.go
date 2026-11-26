package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of rounds: the planning rounds the member's VNAS
// claims, starts and ends (docs/architecture/videocut-planning-session.md).

// endRound ends rounds: one only claimed never reached the model and is
// abandoned, one started is over. Its arguments are the time and the
// outcome; the rounds it ends follow.
const endRound = `UPDATE rounds SET ended_at = ?, outcome = ?, state = CASE WHEN state = 'claimed' THEN 'abandoned' ELSE 'ended' END`

// lateRounds picks the rounds out of time: claimed by the first bound and
// not started, or started by the second or last called by the third.
const lateRounds = `(state = 'claimed' AND claimed_at <= ?) OR (state = 'started' AND (started_at <= ? OR called_at <= ?))`

// RoundStanding is a round as a tool call finds it, with its job: the
// round's id, state, epoch and the first step's input key it plans for;
// the job's epoch, whether it is being cancelled, how many 方案 it makes,
// and the first step's input key it has now (not valid when none).
type RoundStanding struct {
	ID, State string
	Epoch     int
	InputKey  string
	JobEpoch  int
	Cancelled bool
	Plans     int
	PickKey   sql.NullString
}

// RoundStanding is owner's round for jobID with token, as it stands now;
// ErrNotFound when there is none.
func (c *conn) RoundStanding(ctx context.Context, owner, jobID, token string) (RoundStanding, error) {
	var r RoundStanding
	err := queryRow(ctx, c.q, `SELECT r.id, r.state, r.epoch, r.input_key, j.plan_epoch, j.cancel_requested, j.plans,
		(SELECT input_key FROM steps WHERE job_id = j.id AND name = 'pick')
		FROM rounds r JOIN jobs j ON j.id = r.job_id WHERE r.job_id = ? AND r.owner_user_id = ? AND r.attempt_token = ?`,
		jobID, owner, token).Scan(&r.ID, &r.State, &r.Epoch, &r.InputKey, &r.JobEpoch, &r.Cancelled, &r.Plans, &r.PickKey)
	return r, err
}

// MarkRoundCalled records that the model called a videocut tool in round id
// at at.
func (d *DB) MarkRoundCalled(ctx context.Context, id string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, "UPDATE rounds SET called_at = ? WHERE id = ?", sqlitedb.FormatTime(at), id)
	return err
}

// EndRoundByID ends round id at at with outcome (endRound).
func (tx *Tx) EndRoundByID(ctx context.Context, id string, at time.Time, outcome string) error {
	_, err := tx.tx.ExecContext(ctx, endRound+" WHERE id = ?", sqlitedb.FormatTime(at), outcome, id)
	return err
}

// PastRound is one of a job's rounds as kept: its state, failure and
// outcome, and when it ended (not valid while it has not).
type PastRound struct {
	State, Failure, Outcome string
	Ended                   sql.NullString
}

// PastRounds are jobID's latest 20 rounds for epoch and pickKey, the latest
// claimed first.
func (d *DB) PastRounds(ctx context.Context, jobID string, epoch int, pickKey string) ([]PastRound, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT state, failure, outcome, ended_at FROM rounds WHERE job_id = ? AND epoch = ? AND input_key = ?
		ORDER BY claimed_at DESC, id DESC LIMIT 20`, jobID, epoch, pickKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rounds []PastRound
	for rows.Next() {
		var round PastRound
		if err := rows.Scan(&round.State, &round.Failure, &round.Outcome, &round.Ended); err != nil {
			return nil, err
		}
		rounds = append(rounds, round)
	}
	return rounds, rows.Err()
}

// EndOtherRounds ends, at at with outcome, jobID's rounds in progress that
// plan for another epoch than epoch or another input key than pickKey
// (endRound).
func (d *DB) EndOtherRounds(ctx context.Context, jobID string, epoch int, pickKey string, at time.Time, outcome string) error {
	_, err := d.db.ExecContext(ctx, endRound+` WHERE job_id = ? AND state IN ('claimed', 'started') AND (epoch <> ? OR input_key <> ?)`,
		sqlitedb.FormatTime(at), outcome, jobID, epoch, pickKey)
	return err
}

// AddRound keeps round id, claimed by owner at at with token for jobID's
// epoch, its template and the first step's input key pickKey.
func (tx *Tx) AddRound(ctx context.Context, id, jobID, owner, token string, at time.Time, epoch int, template, pickKey string) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO rounds(id, job_id, owner_user_id, attempt_token, state, claimed_at, epoch, template, input_key)
			VALUES(?, ?, ?, ?, 'claimed', ?, ?, ?, ?)`, id, jobID, owner, token, sqlitedb.FormatTime(at), epoch, template, pickKey)
	return err
}

// RoundRow is a round as the operations on it read it.
type RoundRow struct {
	ID, JobID, Owner, Token, State string
}

// Round is owner's round roundID; ErrNotFound when owner has none.
func (tx *Tx) Round(ctx context.Context, owner, roundID string) (RoundRow, error) {
	var r RoundRow
	err := queryRow(ctx, tx.tx, "SELECT id, job_id, owner_user_id, attempt_token, state FROM rounds WHERE id = ? AND owner_user_id = ?",
		roundID, owner).Scan(&r.ID, &r.JobID, &r.Owner, &r.Token, &r.State)
	return r, err
}

// EndRounds ends, at at with outcome, jobID's rounds in progress, whatever
// they plan for (endRound). Their conversations stay in the member's VNAS
// history; what they hand back from now on is not kept.
func (c *conn) EndRounds(ctx context.Context, jobID string, at time.Time, outcome string) error {
	_, err := c.q.ExecContext(ctx, endRound+" WHERE job_id = ? AND state IN ('claimed', 'started')", sqlitedb.FormatTime(at), outcome, jobID)
	return err
}

// StartRound records round id started at at as the conversation runRef.
func (tx *Tx) StartRound(ctx context.Context, id, runRef string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE rounds SET state = 'started', run_ref = ?, started_at = ? WHERE id = ?",
		runRef, sqlitedb.FormatTime(at), id)
	return err
}

// AbandonRound records round id abandoned, ended and reported at at, with
// outcome and failure.
func (tx *Tx) AbandonRound(ctx context.Context, id string, at time.Time, outcome, failure string) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, "UPDATE rounds SET state = 'abandoned', ended_at = ?, reported_at = ?, outcome = ?, failure = ? WHERE id = ?",
		now, now, outcome, failure, id)
	return err
}

// EndStartedRound records round id ended and reported at at, with outcome;
// its failure is failure when the model called no tool in it, else none.
func (tx *Tx) EndStartedRound(ctx context.Context, id string, at time.Time, outcome, failure string) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `UPDATE rounds SET state = 'ended', ended_at = ?, reported_at = ?, outcome = ?,
				failure = CASE WHEN called_at IS NULL THEN ? ELSE '' END WHERE id = ?`,
		now, now, outcome, failure, id)
	return err
}

// MarkRoundReported records round id reported at at, unless it was before.
func (tx *Tx) MarkRoundReported(ctx context.Context, id string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE rounds SET reported_at = COALESCE(reported_at, ?) WHERE id = ?", sqlitedb.FormatTime(at), id)
	return err
}

// EndLateRounds ends, at at with outcome, the rounds out of time by the
// bounds claimed, started and called (lateRounds; endRound), and says how
// many it ended (0 when that is unknown).
func (tx *Tx) EndLateRounds(ctx context.Context, at time.Time, outcome string, claimed, started, called time.Time) (int64, error) {
	bounds := []any{sqlitedb.FormatTime(claimed), sqlitedb.FormatTime(started), sqlitedb.FormatTime(called)}
	ended, err := tx.tx.ExecContext(ctx, endRound+" WHERE "+lateRounds, append([]any{sqlitedb.FormatTime(at), outcome}, bounds...)...)
	if err != nil {
		return 0, err
	}
	swept, _ := ended.RowsAffected()
	return swept, nil
}
