package montage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// A job makes one film of a project (docs/architecture/montage-center.md,
// 持久任务): analysis, catalog, song, planning, filling and checking the
// timeline, rendering and saving, each step's result stored before the next
// so that a restart or a retry goes on from the first step not done. The
// job keeps a snapshot of the project's videos and settings when it starts:
// changing the project meanwhile only changes the next job.

// Job states.
const (
	JobQueued     = "queued"
	JobRunning    = "running"
	JobWaiting    = "waiting"
	JobNeedsInput = "needs_input"
	JobDone       = "done"
	JobFailed     = "failed"
	JobCancelled  = "cancelled"
)

// Why a job waits or needs the member.
const (
	WaitAnalysis     = "analysis"
	WaitTrashed      = "asset_trashed"
	WaitVNAS         = "vnas"
	NeedMissing      = "asset_missing"
	NeedAnalysis     = "analysis_failed"
	NeedNoFootage    = "no_usable_footage"
	NeedMusic        = "music_invalid"
	NeedVNASUnknown  = "vnas_unknown"
	NeedPlanInvalid  = "plan_invalid"
	NeedStorageFull  = "storage_full"
	jobLease         = 5 * time.Minute
	jobMaxAttempts   = 3
	jobRetryDelay    = time.Minute
	lifecycleCheck   = 10 * time.Minute
	finishedStates   = "'done', 'failed', 'cancelled'"
	activeStateQuery = "state NOT IN (" + finishedStates + ")"
)

var (
	// ErrJobActive: the project already has a job in progress.
	ErrJobActive = errors.New("the project already has a job in progress")
	// ErrJobState: the job is not in a state that allows this.
	ErrJobState = errors.New("the job cannot do this now")
	// ErrJobsUnavailable: this service was started without what jobs need.
	ErrJobsUnavailable = errors.New("the montage center cannot make films here")
)

// JobSettings is what a job started with.
type JobSettings struct {
	SongID        string  `json:"songId,omitempty"`
	SongStart     float64 `json:"songStart,omitempty"`
	Ask           string  `json:"ask"`
	Chronological bool    `json:"chronological"`
	ProjectName   string  `json:"projectName"`
}

// Step is where one step of a job stands.
type Step struct {
	Name     string     `json:"name"`
	State    string     `json:"state"`
	Started  *time.Time `json:"startedAt,omitempty"`
	Finished *time.Time `json:"finishedAt,omitempty"`
}

// Job is a job as the member sees it.
type Job struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"projectId"`
	State      string     `json:"state"`
	Step       string     `json:"step,omitempty"`
	WaitReason string     `json:"waitReason,omitempty"`
	Error      string     `json:"error,omitempty"`
	Steps      []Step     `json:"steps"`
	Missing    []string   `json:"missingAssetIds,omitempty"`
	Version    *Version   `json:"version,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Version is a film a job saved.
type Version struct {
	Number      int       `json:"number"`
	FilmAssetID string    `json:"filmAssetId"`
	CreatedAt   time.Time `json:"createdAt"`
	// Gone: the member deleted the film from the photo library.
	Gone bool `json:"gone,omitempty"`
}

// StepNames are a job's steps, in order.
var StepNames = []string{"analysis", "catalog", "song", "pick", "shots", "fill", "render", "save"}

func scanTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &at
}

// Generate starts a job for owner's project from its videos and settings
// now. The same requestKey again returns the same job; a project with a job
// in progress answers ErrJobActive.
func (s *Store) Generate(ctx context.Context, owner, projectID, requestKey string) (Job, bool, error) {
	if !printable(requestKey, MaxRequestKey) {
		return Job{}, false, ErrInvalid
	}
	if !s.runsJobs() {
		return Job{}, false, ErrJobsUnavailable
	}
	var jobID string
	created := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var priorProject string
		err := tx.QueryRowContext(ctx, "SELECT id, project_id FROM jobs WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).Scan(&jobID, &priorProject)
		if err == nil && priorProject != projectID {
			return ErrConflict
		}
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		row, err := s.project(ctx, tx, owner, projectID)
		if err != nil {
			return err
		}
		if row.state != "ready" {
			return ErrNotFound
		}
		var active bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE project_id = ? AND "+activeStateQuery+")", projectID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrJobActive
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM project_assets WHERE project_id = ?", projectID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrInvalid
		}
		settings, err := s.settingsOf(ctx, tx, row)
		if err != nil {
			return err
		}
		if settings.SongID == "" {
			return ErrNoSong
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		jobID, created = newID("job"), true
		now := sqlitedb.FormatTime(s.now())
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, state, not_before, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, 'queued', ?, ?, ?)`, jobID, owner, projectID, requestKey, string(encoded), now, now, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) SELECT ?, asset_id, object_id FROM project_assets WHERE project_id = ?",
			jobID, projectID)
		return err
	})
	if err != nil {
		return Job{}, false, err
	}
	s.wakeJobs()
	job, err := s.Job(ctx, owner, jobID)
	return job, created, err
}

// settingsOf is a project's settings as a job keeps them.
func (s *Store) settingsOf(ctx context.Context, q queryer, row projectRow) (JobSettings, error) {
	settings := JobSettings{Ask: row.ask, Chronological: row.chronological, ProjectName: row.name}
	if s.jobMusic != nil {
		chosen := s.jobMusic.Chosen
		if s.jobMusic == Music(s) {
			// The store's own songs, read in the job's transaction: the
			// database has one connection.
			chosen = func(ctx context.Context, owner, projectID string) (string, float64, error) {
				return s.chosenIn(ctx, q, owner, projectID)
			}
		}
		id, start, err := chosen(ctx, row.owner, row.id)
		if err != nil {
			return JobSettings{}, err
		}
		settings.SongID, settings.SongStart = id, start
	}
	return settings, nil
}

// Job returns owner's job.
func (s *Store) Job(ctx context.Context, owner, jobID string) (Job, error) {
	var job Job
	var step, reason, message sql.NullString
	var created string
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, project_id, state, step, wait_reason, error, created_at, finished_at
		FROM jobs WHERE id = ? AND owner_user_id = ?`, jobID, owner).
		Scan(&job.ID, &job.ProjectID, &job.State, &step, &reason, &message, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	job.Step, job.WaitReason, job.Error = step.String, reason.String, message.String
	if at := scanTime(sql.NullString{String: created, Valid: true}); at != nil {
		job.CreatedAt = *at
	}
	job.FinishedAt = scanTime(finished)
	done := map[string]Step{}
	rows, err := s.db.QueryContext(ctx, "SELECT name, state, started_at, finished_at FROM steps WHERE job_id = ?", jobID)
	if err != nil {
		return Job{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var st Step
		var started, ended sql.NullString
		if err := rows.Scan(&st.Name, &st.State, &started, &ended); err != nil {
			return Job{}, err
		}
		st.Started, st.Finished = scanTime(started), scanTime(ended)
		if st.State == JobRunning && job.State != JobRunning {
			// The step stopped with the job: it shows why.
			st.State = job.State
		}
		done[st.Name] = st
	}
	if err := rows.Err(); err != nil {
		return Job{}, err
	}
	for _, name := range StepNames {
		st, ok := done[name]
		if !ok {
			st = Step{Name: name, State: "pending"}
		}
		job.Steps = append(job.Steps, st)
	}
	if job.Missing, err = s.missingOf(ctx, jobID); err != nil {
		return Job{}, err
	}
	version, err := s.versionOf(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	job.Version = version
	return job, nil
}

func (s *Store) missingOf(ctx context.Context, jobID string) ([]string, error) {
	var raw sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT missing_json FROM jobs WHERE id = ?", jobID).Scan(&raw); err != nil || !raw.Valid {
		return nil, err
	}
	var missing []string
	return missing, json.Unmarshal([]byte(raw.String), &missing)
}

func (s *Store) versionOf(ctx context.Context, jobID string) (*Version, error) {
	var version Version
	var created string
	err := s.db.QueryRowContext(ctx, "SELECT number, film_asset_id, created_at FROM versions WHERE job_id = ?", jobID).
		Scan(&version.Number, &version.FilmAssetID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if at := scanTime(sql.NullString{String: created, Valid: true}); at != nil {
		version.CreatedAt = *at
	}
	return &version, nil
}

// Cancel stops owner's job: a queued or waiting one at once, a running one
// once its step can stop; a film it made is not saved.
func (s *Store) Cancel(ctx context.Context, owner, jobID string) (Job, error) {
	unlock, err := s.members.lock(ctx, owner)
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	now := sqlitedb.FormatTime(s.now())
	var state string
	err = s.db.QueryRowContext(ctx, `UPDATE jobs SET cancel_requested = 1, updated_at = ?,
		state = CASE WHEN state = 'running' THEN state ELSE 'cancelled' END,
		finished_at = CASE WHEN state = 'running' THEN finished_at ELSE ? END
		WHERE id = ? AND owner_user_id = ? AND save_started = 0 AND `+activeStateQuery+" RETURNING state", now, now, jobID, owner).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.Job(ctx, owner, jobID); err != nil {
			return Job{}, err
		}
		return Job{}, ErrJobState
	}
	if err != nil {
		return Job{}, err
	}
	if state == JobRunning {
		s.cancelRunning(jobID)
	} else {
		s.removeJobFiles(jobID)
		s.releaseSong(ctx, owner)
	}
	return s.Job(ctx, owner, jobID)
}

// resume puts a job that waits or needs the member back in the queue.
func (s *Store) resume(ctx context.Context, tx *sql.Tx, owner, jobID string, states ...string) error {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
	args := []any{sqlitedb.FormatTime(s.now()), sqlitedb.FormatTime(s.now()), jobID, owner}
	for _, state := range states {
		args = append(args, state)
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued', attempts = 0, not_before = ?, updated_at = ?,
		wait_reason = '', error_class = '', error = '', missing_json = NULL WHERE id = ? AND owner_user_id = ? AND cancel_requested = 0 AND state IN (`+marks+")", args...)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrJobState
	}
	return nil
}

// DropAssets takes videos the job can no longer use out of it and its
// project, and out of the grant, and goes on; the catalog changes, so any
// plan already taken is planned again. With nothing left the job can only
// be cancelled.
func (s *Store) DropAssets(ctx context.Context, owner, jobID string, assetIDs []string) (Job, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return Job{}, err
	}
	unlock, err := s.members.lock(ctx, owner)
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return Job{}, err
	}
	if job.State != JobNeedsInput || !slices.Contains([]string{NeedMissing, NeedAnalysis, NeedTooLong}, job.WaitReason) {
		return Job{}, ErrJobState
	}
	for _, id := range ids {
		if !slices.Contains(job.Missing, id) {
			return Job{}, ErrJobState
		}
	}
	var count int
	var grantID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT grant_id, (SELECT COUNT(*) FROM job_assets WHERE job_id = ?)
		FROM projects WHERE id = ?`, jobID, job.ProjectID).Scan(&grantID, &count); err != nil {
		return Job{}, err
	}
	if count <= len(ids) {
		return Job{}, ErrJobState
	}
	// Remove is idempotent. If its answer or the local commit is lost, the
	// job remains in needs_input and repeating the same drop finishes it.
	if grantID.Valid {
		if err := s.grants.Remove(ctx, grantID.String, ids); err != nil && photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
			return Job{}, grantError(err)
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "DELETE FROM job_assets WHERE job_id = ? AND asset_id = ?", jobID, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM project_assets WHERE project_id = ? AND asset_id = ?", job.ProjectID, id); err != nil {
				return err
			}
		}
		// The catalog changes: plans already taken are void.
		if err := voidPlans(ctx, tx, jobID, s.now()); err != nil {
			return err
		}
		return s.resume(ctx, tx, owner, jobID, JobNeedsInput)
	})
	if err != nil {
		return Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// RetryAnalysis asks the photo library to analyse again the videos whose
// analysis failed, and goes on.
func (s *Store) RetryAnalysis(ctx context.Context, owner, jobID string) (Job, error) {
	unlock, err := s.members.lock(ctx, owner)
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return Job{}, err
	}
	if job.WaitReason != NeedAnalysis {
		return Job{}, ErrJobState
	}
	grantID, _, err := s.jobGrant(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	if err := s.analyzeAssets(ctx, grantID, job.Missing, true); err != nil {
		return Job{}, grantError(err)
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error { return s.resume(ctx, tx, owner, jobID, JobNeedsInput) }); err != nil {
		return Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// Retry goes on from the step a job stopped at, once the member has made
// room or otherwise fixed what it needed.
func (s *Store) Retry(ctx context.Context, owner, jobID string) (Job, error) {
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return Job{}, err
	}
	if !slices.Contains([]string{NeedStorageFull, NeedNoFootage, NeedMusic}, job.WaitReason) {
		return Job{}, ErrJobState
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error { return s.resume(ctx, tx, owner, jobID, JobNeedsInput) }); err != nil {
		return Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// Replan plans again from the step not yet taken, when the plan was not
// handed back or kept failing its checks.
func (s *Store) Replan(ctx context.Context, owner, jobID string) (Job, error) {
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return Job{}, err
	}
	if !slices.Contains([]string{NeedVNASUnknown, NeedPlanInvalid, WaitVNAS}, job.WaitReason) {
		return Job{}, ErrJobState
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0 WHERE id = ?", jobID); err != nil {
			return err
		}
		return s.resume(ctx, tx, owner, jobID, JobNeedsInput, JobWaiting)
	})
	if err != nil {
		return Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// Notice is what the desktop tells a member about their jobs.
type Notice struct {
	Kind      string    `json:"kind"`
	JobID     string    `json:"jobId"`
	ProjectID string    `json:"projectId"`
	Project   string    `json:"project"`
	Reason    string    `json:"reason,omitempty"`
	FilmID    string    `json:"filmAssetId,omitempty"`
	At        time.Time `json:"at"`
}

// noticeWindow is how long a saved film stays a notice.
const noticeWindow = 24 * time.Hour

// Notices lists owner's jobs that wait for planning, plan, need the member,
// or saved a film lately.
func (s *Store) Notices(ctx context.Context, owner string) ([]Notice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id, j.project_id, p.name, j.state, j.step, j.wait_reason, j.updated_at, IFNULL(v.film_asset_id, '')
		FROM jobs j JOIN projects p ON p.id = j.project_id LEFT JOIN versions v ON v.job_id = j.id
		WHERE j.owner_user_id = ? AND (j.state IN ('waiting', 'needs_input') OR (j.state = 'running' AND j.step IN ('pick', 'shots'))
		  OR (j.state = 'done' AND j.finished_at >= ?)) ORDER BY j.updated_at DESC`, owner, sqlitedb.FormatTime(s.now().Add(-noticeWindow)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := []Notice{}
	for rows.Next() {
		var n Notice
		var state, step, at string
		if err := rows.Scan(&n.JobID, &n.ProjectID, &n.Project, &state, &step, &n.Reason, &at, &n.FilmID); err != nil {
			return nil, err
		}
		switch {
		case state == JobDone:
			n.Kind = "saved"
		case state == JobWaiting && n.Reason == WaitVNAS:
			n.Kind = "waiting_for_planning"
		case state == JobRunning:
			n.Kind = "planning"
		case state == JobNeedsInput:
			n.Kind = "needs_input"
		default:
			continue
		}
		if t := scanTime(sql.NullString{String: at, Valid: true}); t != nil {
			n.At = *t
		}
		notices = append(notices, n)
	}
	return notices, rows.Err()
}

// runsJobs reports whether the store has what jobs work with.
func (s *Store) runsJobs() bool {
	return s.jobCompute != nil && s.editing != nil && s.planner != nil && s.jobMusic != nil
}
