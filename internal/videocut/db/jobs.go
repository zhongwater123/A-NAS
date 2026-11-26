package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of jobs (docs/architecture/videocut-center.md, 持久任务).

const (
	// finishedStates are the states a job ends in, as SQL.
	finishedStates = "'done', 'failed', 'cancelled'"
	// activeStateQuery holds for a job not finished.
	activeStateQuery = "state NOT IN (" + finishedStates + ")"
	// dueStates holds for a job that may run once due.
	dueStates = "state IN ('queued', 'waiting') AND cancel_requested = 0"
)

// --- planning (docs/architecture/videocut-planning-session.md) ---

// wantsRound is what makes a job one a round may be claimed for: it came to
// planning, waits there for VNAS (its wait reason waitVNAS), and asked for
// a round. A job put back in the queue, by replanning or by taking videos
// out, wants none until it comes to planning again (app's resume). It
// reads the jobs as j.
func wantsRound(waitVNAS string) string {
	return "j.plan_wanted = 1 AND j.state = 'waiting' AND j.wait_reason = '" + waitVNAS + "' AND j.cancel_requested = 0"
}

// keepsMarker is what keeps a member's marker: a job that wants a round, or
// one that wanted it and is looking again right now (a waiting job runs for
// an instant each time it looks, and leaves its marker as it goes back to
// waiting). Without the second, a sync in that instant would take the
// marker of a job that still waits. waitVNAS is the wait reason of a job
// waiting for VNAS; it reads the jobs as j.
func keepsMarker(waitVNAS string) string {
	return "j.plan_wanted = 1 AND j.cancel_requested = 0 AND (j.state = 'running' OR (j.state = 'waiting' AND j.wait_reason = '" + waitVNAS + "'))"
}

// ResetPlanRejections forgets jobID's answers refused in a row.
func (tx *Tx) ResetPlanRejections(ctx context.Context, jobID string) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0 WHERE id = ?", jobID)
	return err
}

// CountPlanRejection counts one more answer of jobID refused in a row, and
// says how many there are now.
func (tx *Tx) CountPlanRejection(ctx context.Context, jobID string) (int, error) {
	var rejections int
	err := tx.tx.QueryRowContext(ctx, "UPDATE jobs SET plan_rejections = plan_rejections + 1 WHERE id = ? RETURNING plan_rejections",
		jobID).Scan(&rejections)
	return rejections, err
}

// JobPlanning is how jobID's planning stands: its owner, its epoch, and
// whether it wants a round.
func (d *DB) JobPlanning(ctx context.Context, jobID string) (owner string, epoch int, wanted bool, err error) {
	err = d.db.QueryRowContext(ctx, "SELECT owner_user_id, plan_epoch, plan_wanted FROM jobs WHERE id = ?", jobID).
		Scan(&owner, &epoch, &wanted)
	return owner, epoch, wanted, err
}

// UnwantRound records that jobID wants no round.
func (c *Conn) UnwantRound(ctx context.Context, jobID string) error {
	_, err := c.q.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", jobID)
	return err
}

// WantRound records that jobID wants a round, since at unless it already
// did, and says since when as kept ("" when that is unknown).
func (d *DB) WantRound(ctx context.Context, jobID string, at time.Time) (string, error) {
	var since string
	err := d.db.QueryRowContext(ctx, `UPDATE jobs SET plan_wanted_at = CASE WHEN plan_wanted = 1 AND plan_wanted_at IS NOT NULL THEN plan_wanted_at ELSE ? END, plan_wanted = 1
		WHERE id = ? RETURNING IFNULL(plan_wanted_at, '')`, sqlitedb.FormatTime(at), jobID).Scan(&since)
	return since, err
}

// MarkedUsernames are the usernames, registered since registeredSince, of
// the members with a job that keeps their marker (keepsMarker, with
// waitVNAS).
func (d *DB) MarkedUsernames(ctx context.Context, waitVNAS string, registeredSince time.Time) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT g.username FROM jobs j JOIN plan_grants g ON g.owner_user_id = j.owner_user_id
		WHERE `+keepsMarker(waitVNAS)+` AND g.registered_at >= ?`,
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
// (wantsRound, with waitVNAS) and none is in progress for: its id, epoch,
// first step's input key and settings, encoded; sql.ErrNoRows when there
// is none.
func (tx *Tx) ClaimableJob(ctx context.Context, owner, waitVNAS string) (jobID string, epoch int, pickKey, settings string, err error) {
	err = tx.tx.QueryRowContext(ctx, `SELECT j.id, j.plan_epoch, p.input_key, j.settings_json FROM jobs j JOIN steps p ON p.job_id = j.id AND p.name = 'pick'
			WHERE j.owner_user_id = ? AND `+wantsRound(waitVNAS)+`
			AND NOT EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.state IN ('claimed', 'started'))
			ORDER BY j.created_at, j.id LIMIT 1`, owner).Scan(&jobID, &epoch, &pickKey, &settings)
	return jobID, epoch, pickKey, settings, err
}

// NewPlanEpoch starts jobID's planning over in a new epoch, its refusals
// forgotten.
func (tx *Tx) NewPlanEpoch(ctx context.Context, jobID string) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0, plan_epoch = plan_epoch + 1 WHERE id = ?", jobID)
	return err
}

// CancelRequested reports whether jobID is being cancelled.
func (c *Conn) CancelRequested(ctx context.Context, jobID string) (bool, error) {
	var cancelled bool
	err := c.q.QueryRowContext(ctx, "SELECT cancel_requested FROM jobs WHERE id = ?", jobID).Scan(&cancelled)
	return cancelled, err
}

// DueLateRoundJobs makes the waiting jobs of the rounds out of time by the
// bounds claimed, started and called (lateRounds) due at at.
func (tx *Tx) DueLateRoundJobs(ctx context.Context, at, claimed, started, called time.Time) error {
	bounds := []any{sqlitedb.FormatTime(claimed), sqlitedb.FormatTime(started), sqlitedb.FormatTime(called)}
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET not_before = ? WHERE state = 'waiting' AND id IN (SELECT job_id FROM rounds WHERE `+lateRounds+")",
		append([]any{sqlitedb.FormatTime(at)}, bounds...)...)
	return err
}

// MakeWaitingJobDue makes jobID due at at, when it waits.
func (tx *Tx) MakeWaitingJobDue(ctx context.Context, jobID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE jobs SET not_before = ? WHERE id = ? AND state = 'waiting'", sqlitedb.FormatTime(at), jobID)
	return err
}

// --- running (docs/architecture/videocut-center.md, 持久任务) ---

// CancelRequestedJobs cancels, at at, the jobs being cancelled that have
// not ended.
func (tx *Tx) CancelRequestedJobs(ctx context.Context, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'cancelled', lease_until = NULL, finished_at = ?, updated_at = ?
			WHERE cancel_requested = 1 AND `+activeStateQuery, now, now)
	return err
}

// RequeueRunningJobs puts the jobs running back in the queue, due at at.
func (tx *Tx) RequeueRunningJobs(ctx context.Context, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued', lease_until = NULL, not_before = ?, updated_at = ?
			WHERE state = 'running'`, now, now)
	return err
}

// JobActive reports whether jobID is a job that has not ended.
func (d *DB) JobActive(ctx context.Context, jobID string) (bool, error) {
	var active bool
	err := d.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = ? AND "+activeStateQuery+")", jobID).Scan(&active)
	return active, err
}

// NextNotBefore is when the first queued or waiting job is due, as kept
// (not valid when none is).
func (d *DB) NextNotBefore(ctx context.Context) (sql.NullString, error) {
	var next sql.NullString
	err := d.db.QueryRowContext(ctx, "SELECT MIN(not_before) FROM jobs WHERE "+dueStates).Scan(&next)
	return next, err
}

// DueJob is a job due at now as the runner takes it, its settings still
// encoded.
type DueJob struct {
	ID, Owner, ProjectID, Kind, Settings string
	Plans                                int
}

// DueJob is the job due first at now; sql.ErrNoRows when none is.
func (tx *Tx) DueJob(ctx context.Context, now time.Time) (DueJob, error) {
	var job DueJob
	err := tx.tx.QueryRowContext(ctx, `SELECT id, owner_user_id, project_id, kind, settings_json, plans FROM jobs
			WHERE `+dueStates+` AND not_before <= ?
			ORDER BY not_before, created_at LIMIT 1`, sqlitedb.FormatTime(now)).
		Scan(&job.ID, &job.Owner, &job.ProjectID, &job.Kind, &job.Settings, &job.Plans)
	return job, err
}

// FailInvalidJob fails jobID at at, as invalid, saying message.
func (tx *Tx) FailInvalidJob(ctx context.Context, jobID, message string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'failed', error_class = 'invalid', error = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		message, sqlitedb.FormatTime(at), sqlitedb.FormatTime(at), jobID)
	return err
}

// RunJob sets jobID running at at, its lease until leaseUntil.
func (tx *Tx) RunJob(ctx context.Context, jobID string, leaseUntil, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'running', lease_until = ?, heartbeat_at = ?, updated_at = ? WHERE id = ?`,
		sqlitedb.FormatTime(leaseUntil), sqlitedb.FormatTime(at), sqlitedb.FormatTime(at), jobID)
	return err
}

// RenewLease renews jobID's lease until leaseUntil, at at, and says
// whether the job is being cancelled and whether it is saving;
// sql.ErrNoRows when the job is gone.
func (d *DB) RenewLease(ctx context.Context, jobID string, leaseUntil, at time.Time) (requested, saving bool, err error) {
	err = d.db.QueryRowContext(ctx, `UPDATE jobs SET lease_until = ?, heartbeat_at = ? WHERE id = ? RETURNING cancel_requested, save_started`,
		sqlitedb.FormatTime(leaseUntil), sqlitedb.FormatTime(at), jobID).Scan(&requested, &saving)
	return requested, saving, err
}

// RunningJob is how jobID stands while it runs: its attempts, whether it
// is being cancelled and whether it is saving; sql.ErrNoRows when it is
// not running.
func (tx *Tx) RunningJob(ctx context.Context, jobID string) (attempts int, cancelled, saving bool, err error) {
	err = tx.tx.QueryRowContext(ctx, "SELECT attempts, cancel_requested, save_started FROM jobs WHERE id = ? AND state = 'running'", jobID).Scan(&attempts, &cancelled, &saving)
	return attempts, cancelled, saving, err
}

// JobSettlement is how a job's step left it, as settling records it. The
// times are as kept (sqlitedb.FormatTime); Missing and FinishedAt are nil
// for NULL.
type JobSettlement struct {
	State, WaitReason string
	Attempts          int
	ErrorClass, Error string
	Missing           any
	SaveStarted       bool
	NotBefore         string
	FinishedAt        any
	UpdatedAt         string
}

// SettleJob records how jobID's step left it, its lease released.
func (tx *Tx) SettleJob(ctx context.Context, jobID string, s JobSettlement) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = ?, wait_reason = ?, attempts = ?, error_class = ?, error = ?,
			missing_json = ?, save_started = ?, lease_until = NULL, not_before = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		s.State, s.WaitReason, s.Attempts, s.ErrorClass, s.Error, s.Missing, s.SaveStarted, s.NotBefore, s.FinishedAt, s.UpdatedAt, jobID)
	return err
}

// SaveStarted reports whether jobID started saving a film and has not
// heard back.
func (d *DB) SaveStarted(ctx context.Context, jobID string) (bool, error) {
	var saving bool
	err := d.db.QueryRowContext(ctx, "SELECT save_started FROM jobs WHERE id = ?", jobID).Scan(&saving)
	return saving, err
}

// SaveReplay is what replaying jobID's interrupted save reads: its
// project's grant, what its render step kept, and the input key of its
// save step.
func (d *DB) SaveReplay(ctx context.Context, jobID, renderStep, saveStep string) (grantID, rendered, key string, err error) {
	err = d.db.QueryRowContext(ctx, `SELECT p.grant_id, r.result_json, w.input_key FROM jobs j
			JOIN projects p ON p.id=j.project_id JOIN steps r ON r.job_id=j.id AND r.name=?
			JOIN steps w ON w.job_id=j.id AND w.name=? WHERE j.id=?`, renderStep, saveStep, jobID).Scan(&grantID, &rendered, &key)
	return grantID, rendered, key, err
}

// JobGrant is the grant of jobID's project (not valid when none);
// sql.ErrNoRows when there is no such job.
func (d *DB) JobGrant(ctx context.Context, jobID string) (sql.NullString, error) {
	var grantID sql.NullString
	err := d.db.QueryRowContext(ctx, `SELECT p.grant_id FROM jobs j JOIN projects p ON p.id = j.project_id WHERE j.id = ?`, jobID).
		Scan(&grantID)
	return grantID, err
}

// SetJobStep records that jobID is at step name, at at.
func (tx *Tx) SetJobStep(ctx context.Context, jobID, name string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE jobs SET step = ?, wait_reason = '', updated_at = ? WHERE id = ?", name, sqlitedb.FormatTime(at), jobID)
	return err
}

// ResetJobAttempts starts jobID's attempts from none, at at.
func (tx *Tx) ResetJobAttempts(ctx context.Context, jobID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE jobs SET attempts = 0, error_class = '', error = '', updated_at = ? WHERE id = ?", sqlitedb.FormatTime(at), jobID)
	return err
}

// StartSave raises jobID's save fence while it runs and is not being
// cancelled; false when it did not.
func (d *DB) StartSave(ctx context.Context, jobID string) (bool, error) {
	started, err := d.db.ExecContext(ctx, "UPDATE jobs SET save_started = 1 WHERE id = ? AND cancel_requested = 0 AND state = 'running'", jobID)
	if err != nil {
		return false, err
	}
	n, err := started.RowsAffected()
	return n > 0, err
}

// ReleaseSaveFence lowers jobID's save fence at at, its attempts from
// none: the job goes on with its next 方案.
func (tx *Tx) ReleaseSaveFence(ctx context.Context, jobID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET save_started = 0, attempts = 0, error_class = '', error = '', updated_at = ? WHERE id = ?`, sqlitedb.FormatTime(at), jobID)
	return err
}

// FinishJob records jobID done at at, its save fence lowered.
func (tx *Tx) FinishJob(ctx context.Context, jobID string, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'done', save_started = 0, missing_json = NULL, attempts = 0, error_class = '', error = '', wait_reason = '',
			lease_until = NULL, finished_at = ?, updated_at = ? WHERE id = ?`, now, now, jobID)
	return err
}

// JobDraft is the draft an edit job saves and the draft's revision.
func (d *DB) JobDraft(ctx context.Context, jobID string) (draftID string, revision int, err error) {
	err = d.db.QueryRowContext(ctx, "SELECT draft_id, draft_revision FROM jobs WHERE id = ?", jobID).Scan(&draftID, &revision)
	return draftID, revision, err
}

// --- the member's requests and reads ---

// KeyedJob is owner's job started with requestKey: its id, its kind and
// what column holds for it; sql.ErrNoRows when there is none.
func (c *Conn) KeyedJob(ctx context.Context, owner, requestKey, column string) (id, kind string, subject sql.NullString, err error) {
	err = c.q.QueryRowContext(ctx, "SELECT id, kind, "+column+" FROM jobs WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).
		Scan(&id, &kind, &subject)
	return id, kind, subject, err
}

// ProjectJobActive reports whether a job of projectID has not ended.
func (tx *Tx) ProjectJobActive(ctx context.Context, projectID string) (bool, error) {
	var active bool
	err := tx.tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE project_id = ? AND "+activeStateQuery+")", projectID).Scan(&active)
	return active, err
}

// NewJob is a job to queue, its settings encoded: an edit names the
// version it came from, its draft and the draft's revision; a recut the
// version.
type NewJob struct {
	ID, Owner, ProjectID, RequestKey, Settings string
	Plans                                      int
	Kind                                       string
	SourceVersion, Draft                       sql.NullString
	DraftRevision                              sql.NullInt64
}

// AddJob queues job, made and due at at.
func (tx *Tx) AddJob(ctx context.Context, job NewJob, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, plans, state, not_before,
			created_at, updated_at, kind, source_version_id, draft_id, draft_revision)
		VALUES(?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Owner, job.ProjectID, job.RequestKey, job.Settings, job.Plans, now, now, now, job.Kind, job.SourceVersion, job.Draft, job.DraftRevision)
	return err
}

// JobSettings are jobID's settings as it was started with them, encoded.
func (c *Conn) JobSettings(ctx context.Context, jobID string) (string, error) {
	var encoded string
	err := c.q.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", jobID).Scan(&encoded)
	return encoded, err
}

// JobRow is a job as its page reads it; the times as kept.
type JobRow struct {
	ID, ProjectID, Kind, State string
	Step, WaitReason, Error    sql.NullString
	Created                    string
	Finished                   sql.NullString
	Plans                      int
}

// Job is owner's job jobID; sql.ErrNoRows when owner has none.
func (d *DB) Job(ctx context.Context, owner, jobID string) (JobRow, error) {
	var job JobRow
	err := d.db.QueryRowContext(ctx, `SELECT id, project_id, kind, state, step, wait_reason, error, created_at, finished_at, plans
		FROM jobs WHERE id = ? AND owner_user_id = ?`, jobID, owner).
		Scan(&job.ID, &job.ProjectID, &job.Kind, &job.State, &job.Step, &job.WaitReason, &job.Error, &job.Created, &job.Finished, &job.Plans)
	return job, err
}

// JobMissing is the videos jobID found missing, encoded (not valid when
// none).
func (d *DB) JobMissing(ctx context.Context, jobID string) (sql.NullString, error) {
	var raw sql.NullString
	err := d.db.QueryRowContext(ctx, "SELECT missing_json FROM jobs WHERE id = ?", jobID).Scan(&raw)
	return raw, err
}

// RequestCancel asks, at at, that owner's jobID stop: a job not running
// is cancelled at once. It says the job's state after; sql.ErrNoRows when
// owner has no such job in progress that is not saving.
func (d *DB) RequestCancel(ctx context.Context, owner, jobID string, at time.Time) (string, error) {
	now := sqlitedb.FormatTime(at)
	var state string
	err := d.db.QueryRowContext(ctx, `UPDATE jobs SET cancel_requested = 1, updated_at = ?,
		state = CASE WHEN state = 'running' THEN state ELSE 'cancelled' END,
		finished_at = CASE WHEN state = 'running' THEN finished_at ELSE ? END
		WHERE id = ? AND owner_user_id = ? AND save_started = 0 AND `+activeStateQuery+" RETURNING state", now, now, jobID, owner).Scan(&state)
	return state, err
}

// Requeue puts owner's jobID back in the queue, due at notBefore and
// changed at updated, when it is in one of states and not being
// cancelled; false when it is not (or that is unknown).
func (tx *Tx) Requeue(ctx context.Context, owner, jobID string, notBefore, updated time.Time, states ...string) (bool, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
	args := []any{sqlitedb.FormatTime(notBefore), sqlitedb.FormatTime(updated), jobID, owner}
	for _, state := range states {
		args = append(args, state)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued', attempts = 0, not_before = ?, updated_at = ?, finished_at = NULL,
		wait_reason = '', error_class = '', error = '', missing_json = NULL, plan_wanted = 0 WHERE id = ? AND owner_user_id = ? AND cancel_requested = 0 AND state IN (`+marks+")", args...)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}

// LatestJob is projectID's job made last; sql.ErrNoRows when it has none.
func (c *Conn) LatestJob(ctx context.Context, projectID string) (string, error) {
	var latest string
	err := c.q.QueryRowContext(ctx, "SELECT id FROM jobs WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", projectID).Scan(&latest)
	return latest, err
}

// NoticeRow is one of a member's jobs as the notices read it: its
// project, state, wait reason and error, when it last changed (as kept),
// its first 方案's film ("" when none) and whether a round of its epoch is
// in progress.
type NoticeRow struct {
	JobID, ProjectID, Project, State, Reason, Detail, At, FilmID string
	Planning                                                     bool
}

// Notices are owner's jobs that wait for VNAS (their wait reason
// waitVNAS), need the member, or were done or failed since since, the
// latest changed first.
func (d *DB) Notices(ctx context.Context, owner, waitVNAS string, since time.Time) ([]NoticeRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT j.id, j.project_id, p.name, j.state, j.wait_reason, j.error, j.updated_at, IFNULL(v.film_asset_id, ''),
		EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.epoch = j.plan_epoch AND r.state IN ('claimed', 'started'))
		FROM jobs j JOIN projects p ON p.id = j.project_id LEFT JOIN versions v ON v.job_id = j.id AND v.plan = 1
		WHERE j.owner_user_id = ? AND ((j.state = 'waiting' AND j.wait_reason = ?) OR j.state = 'needs_input'
		  OR (j.state IN ('done', 'failed') AND j.finished_at >= ?)) ORDER BY j.updated_at DESC`, owner, waitVNAS, sqlitedb.FormatTime(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notices []NoticeRow
	for rows.Next() {
		var n NoticeRow
		if err := rows.Scan(&n.JobID, &n.ProjectID, &n.Project, &n.State, &n.Reason, &n.Detail, &n.At, &n.FilmID, &n.Planning); err != nil {
			return nil, err
		}
		notices = append(notices, n)
	}
	return notices, rows.Err()
}

// ProjectJobIDs are the jobs of projectID.
func (tx *Tx) ProjectJobIDs(ctx context.Context, projectID string) ([]string, error) {
	return jobIDs(ctx, tx.tx, "SELECT id FROM jobs WHERE project_id = ?", projectID)
}

// OwnerJobIDs are the jobs of owner.
func (tx *Tx) OwnerJobIDs(ctx context.Context, owner string) ([]string, error) {
	return jobIDs(ctx, tx.tx, "SELECT id FROM jobs WHERE owner_user_id = ?", owner)
}

func jobIDs(ctx context.Context, q queryer, query, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
