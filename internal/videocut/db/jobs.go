package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of jobs (docs/architecture/montage-center.md, 持久任务).

const (
	// FinishedStates are the states a job ends in, as SQL.
	FinishedStates = "'done', 'failed', 'cancelled'"
	// ActiveStateQuery holds for a job not finished.
	ActiveStateQuery = "state NOT IN (" + FinishedStates + ")"
)

// --- planning (docs/architecture/montage-planning-session.md) ---

// WantsRound is what makes a job one a round may be claimed for: it came to
// planning, waits there for VNAS (its wait reason waitVNAS), and asked for
// a round. A job put back in the queue, by replanning or by taking videos
// out, wants none until it comes to planning again (app's resume). It
// reads the jobs as j.
func WantsRound(waitVNAS string) string {
	return "j.plan_wanted = 1 AND j.state = 'waiting' AND j.wait_reason = '" + waitVNAS + "' AND j.cancel_requested = 0"
}

// KeepsMarker is what keeps a member's marker: a job that wants a round, or
// one that wanted it and is looking again right now (a waiting job runs for
// an instant each time it looks, and leaves its marker as it goes back to
// waiting). Without the second, a sync in that instant would take the
// marker of a job that still waits. waitVNAS is the wait reason of a job
// waiting for VNAS; it reads the jobs as j.
func KeepsMarker(waitVNAS string) string {
	return "j.plan_wanted = 1 AND j.cancel_requested = 0 AND (j.state = 'running' OR (j.state = 'waiting' AND j.wait_reason = '" + waitVNAS + "'))"
}

// ResetPlanRejections forgets jobID's answers refused in a row.
func (tx *Tx) ResetPlanRejections(ctx context.Context, jobID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0 WHERE id = ?", jobID)
	return err
}

// CountPlanRejection counts one more answer of jobID refused in a row, and
// says how many there are now.
func (tx *Tx) CountPlanRejection(ctx context.Context, jobID string) (int, error) {
	var rejections int
	err := tx.QueryRowContext(ctx, "UPDATE jobs SET plan_rejections = plan_rejections + 1 WHERE id = ? RETURNING plan_rejections",
		jobID).Scan(&rejections)
	return rejections, err
}

// JobPlanning is how jobID's planning stands: its owner, its epoch, and
// whether it wants a round.
func (d *DB) JobPlanning(ctx context.Context, jobID string) (owner string, epoch int, wanted bool, err error) {
	err = d.QueryRowContext(ctx, "SELECT owner_user_id, plan_epoch, plan_wanted FROM jobs WHERE id = ?", jobID).
		Scan(&owner, &epoch, &wanted)
	return owner, epoch, wanted, err
}

// UnwantRound records that jobID wants no round.
func (d *DB) UnwantRound(ctx context.Context, jobID string) error {
	return unwantRound(ctx, d, jobID)
}

// UnwantRound is DB.UnwantRound inside the transaction.
func (tx *Tx) UnwantRound(ctx context.Context, jobID string) error {
	return unwantRound(ctx, tx, jobID)
}

func unwantRound(ctx context.Context, q queryer, jobID string) error {
	_, err := q.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", jobID)
	return err
}

// WantRound records that jobID wants a round, since at unless it already
// did, and says since when as kept ("" when that is unknown).
func (d *DB) WantRound(ctx context.Context, jobID string, at time.Time) (string, error) {
	var since string
	err := d.QueryRowContext(ctx, `UPDATE jobs SET plan_wanted_at = CASE WHEN plan_wanted = 1 AND plan_wanted_at IS NOT NULL THEN plan_wanted_at ELSE ? END, plan_wanted = 1
		WHERE id = ? RETURNING IFNULL(plan_wanted_at, '')`, sqlitedb.FormatTime(at), jobID).Scan(&since)
	return since, err
}

// MarkedUsernames are the usernames, registered since registeredSince, of
// the members with a job that keeps their marker (KeepsMarker, with
// waitVNAS).
func (d *DB) MarkedUsernames(ctx context.Context, waitVNAS string, registeredSince time.Time) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT DISTINCT g.username FROM jobs j JOIN plan_grants g ON g.owner_user_id = j.owner_user_id
		WHERE `+KeepsMarker(waitVNAS)+` AND g.registered_at >= ?`,
		sqlitedb.FormatTime(registeredSince))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wanted := map[string]bool{}
	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return nil, err
		}
		wanted[username] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return wanted, nil
}

// ClaimableJob is owner's earliest job a round may be claimed for
// (WantsRound, with waitVNAS) and none is in progress for: its id, epoch,
// first step's input key and settings, encoded; sql.ErrNoRows when there
// is none.
func (tx *Tx) ClaimableJob(ctx context.Context, owner, waitVNAS string) (jobID string, epoch int, pickKey, settings string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT j.id, j.plan_epoch, p.input_key, j.settings_json FROM jobs j JOIN steps p ON p.job_id = j.id AND p.name = 'pick'
			WHERE j.owner_user_id = ? AND `+WantsRound(waitVNAS)+`
			AND NOT EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.state IN ('claimed', 'started'))
			ORDER BY j.created_at, j.id LIMIT 1`, owner).Scan(&jobID, &epoch, &pickKey, &settings)
	return jobID, epoch, pickKey, settings, err
}

// NewPlanEpoch starts jobID's planning over in a new epoch, its refusals
// forgotten.
func (tx *Tx) NewPlanEpoch(ctx context.Context, jobID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0, plan_epoch = plan_epoch + 1 WHERE id = ?", jobID)
	return err
}

// CancelRequested reports whether jobID is being cancelled.
func (tx *Tx) CancelRequested(ctx context.Context, jobID string) (bool, error) {
	var cancelled bool
	err := tx.QueryRowContext(ctx, "SELECT cancel_requested FROM jobs WHERE id = ?", jobID).Scan(&cancelled)
	return cancelled, err
}

// DueLateRoundJobs makes the waiting jobs of the rounds out of time by the
// bounds claimed, started and called (LateRounds) due at at.
func (tx *Tx) DueLateRoundJobs(ctx context.Context, at, claimed, started, called time.Time) error {
	bounds := []any{sqlitedb.FormatTime(claimed), sqlitedb.FormatTime(started), sqlitedb.FormatTime(called)}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET not_before = ? WHERE state = 'waiting' AND id IN (SELECT job_id FROM rounds WHERE `+LateRounds+")",
		append([]any{sqlitedb.FormatTime(at)}, bounds...)...)
	return err
}

// MakeWaitingJobDue makes jobID due at at, when it waits.
func (tx *Tx) MakeWaitingJobDue(ctx context.Context, jobID string, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE jobs SET not_before = ? WHERE id = ? AND state = 'waiting'", sqlitedb.FormatTime(at), jobID)
	return err
}
