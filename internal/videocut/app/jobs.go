package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// A job makes the films of a project to choose from, 方案一 and 方案二
// (docs/architecture/montage-center.md, 持久任务): analysis, catalog and
// song once, then for each 方案 planning, filling and checking the timeline,
// rendering and saving, each step's result stored before the next so that a
// restart or a retry goes on from the first step not done. The
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

// Why a job waits, needs the member, or failed. The member is asked only
// for what they can change; what VNAS could not plan is planned again on its
// own, and after the last round the job fails (FailPlanning). The three
// planning reasons a job needed the member for are those of jobs stopped
// before 2026-11-25, which can still be planned again (Replan).
const (
	WaitAnalysis    = "analysis"
	WaitTrashed     = "asset_trashed"
	WaitVNAS        = "vnas"
	NeedMissing     = "asset_missing"
	NeedAnalysis    = "analysis_failed"
	NeedNoFootage   = "no_usable_footage"
	NeedMusic       = "music_invalid"
	NeedVNASUnknown = "vnas_unknown"
	NeedPlanInvalid = "plan_invalid"
	NeedPlanError   = "plan_error"
	NeedStorageFull = "storage_full"
	// FailPlanning: the planning was tried maxPlanRounds rounds and none
	// finished it; the job failed, its error says how the last ended.
	FailPlanning   = "plan_failed"
	jobLease       = 5 * time.Minute
	jobMaxAttempts = 3
	jobRetryDelay  = time.Minute
	lifecycleCheck = 10 * time.Minute
)

// The kinds of job and of version (#596).
const (
	KindGenerate = "generate"
	KindEdit     = "edit"
	KindRecut    = "recut"
)

// versionColumns are what versionsOf scans, of versions v.
var versionColumns = `v.id, v.number, v.job_id, v.plan, v.film_asset_id, v.created_at,
	v.timeline_json IS NOT NULL OR EXISTS (SELECT 1 FROM steps f WHERE f.job_id = v.job_id AND f.state = 'done'
		AND f.name = ` + planStepSQL("fill", "v.plan") + ` AND f.result_json IS NOT NULL),
	v.kind, IFNULL(v.source_version_id, ''), IFNULL((SELECT number FROM versions o WHERE o.id = v.source_version_id), 0), v.draft_revision,
	CASE WHEN v.kind = 'recut' THEN IFNULL((SELECT json_extract(CASE WHEN json_valid(settings_json) THEN settings_json END, '$.recut.change')
		FROM jobs WHERE id = v.job_id), '') ELSE '' END`

func scanVersion(rows interface{ Scan(...any) error }) (domain.Version, error) {
	var version domain.Version
	var created string
	var revision sql.NullInt64
	if err := rows.Scan(&version.ID, &version.Number, &version.JobID, &version.Plan, &version.FilmAssetID, &created, &version.Editable,
		&version.Kind, &version.SourceVersionID, &version.SourceNumber, &revision, &version.Change); err != nil {
		return domain.Version{}, err
	}
	if revision.Valid {
		n := int(revision.Int64)
		version.DraftRevision = &n
	}
	if at := scanTime(sql.NullString{String: created, Valid: true}); at != nil {
		version.CreatedAt = *at
	}
	return version, nil
}

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
func (s *Store) Generate(ctx context.Context, owner, projectID, requestKey string) (domain.Job, bool, error) {
	if !printable(requestKey, MaxRequestKey) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if !s.runsJobs() {
		return domain.Job{}, false, domain.ErrJobsUnavailable
	}
	var jobID string
	created := false
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		var err error
		if jobID, err = keyedJob(ctx, tx, owner, requestKey, KindGenerate, "project_id", projectID); err != nil || jobID != "" {
			return err
		}
		row, err := s.project(ctx, tx, owner, projectID)
		if err != nil {
			return err
		}
		if row.state != "ready" {
			return domain.ErrNotFound
		}
		if err := projectIdle(ctx, tx, projectID); err != nil {
			return err
		}
		count, err := tx.ProjectAssetCount(ctx, projectID)
		if err != nil {
			return err
		}
		if count == 0 {
			return domain.ErrInvalid
		}
		settings, err := s.settingsOf(ctx, tx, row)
		if err != nil {
			return err
		}
		if settings.SongID == "" {
			return domain.ErrNoSong
		}
		if jobID, err = s.queueJob(ctx, tx, newJob{owner: owner, projectID: projectID, requestKey: requestKey, kind: KindGenerate, settings: settings, plans: s.plans}); err != nil {
			return err
		}
		created = true
		return tx.CopyProjectAssets(ctx, jobID, projectID)
	})
	if err != nil {
		return domain.Job{}, false, err
	}
	s.wakeJobs()
	job, err := s.Job(ctx, owner, jobID)
	return job, created, err
}

// keyedJob is the job owner started with requestKey, "" when there is none
// yet. The key names one request: a job of another kind, or of another
// subject (the project generated, the draft saved, the version recut, which
// column holds), is another request's, ErrConflict.
func keyedJob(ctx context.Context, q queryer, owner, requestKey, kind, column, subject string) (string, error) {
	var id, priorKind string
	var prior sql.NullString
	err := q.QueryRowContext(ctx, "SELECT id, kind, "+column+" FROM jobs WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).
		Scan(&id, &priorKind, &prior)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case priorKind != kind || prior.String != subject:
		return "", domain.ErrConflict
	}
	return id, nil
}

// projectIdle answers ErrJobActive while a job of the project has not
// ended: a project has one job in progress at a time, of any kind.
func projectIdle(ctx context.Context, tx *db.Tx, projectID string) error {
	var active bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE project_id = ? AND "+db.ActiveStateQuery+")", projectID).Scan(&active); err != nil {
		return err
	}
	if active {
		return domain.ErrJobActive
	}
	return nil
}

// newJob is a job to queue: an edit names the version it came from, its
// draft and the draft's revision; a recut the version.
type newJob struct {
	owner, projectID, requestKey, kind string
	settings                           domain.JobSettings
	plans                              int
	sourceVersion, draft               sql.NullString
	draftRevision                      sql.NullInt64
}

// queueJob writes a job, due now, and returns its ID.
func (s *Store) queueJob(ctx context.Context, tx *db.Tx, job newJob) (string, error) {
	encoded, err := json.Marshal(job.settings)
	if err != nil {
		return "", err
	}
	id, now := newID("job"), sqlitedb.FormatTime(s.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, plans, state, not_before,
			created_at, updated_at, kind, source_version_id, draft_id, draft_revision)
		VALUES(?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?)`,
		id, job.owner, job.projectID, job.requestKey, string(encoded), job.plans, now, now, now, job.kind, job.sourceVersion, job.draft, job.draftRevision)
	return id, err
}

// jobSettings are a job's settings as it was started with them.
func jobSettings(ctx context.Context, q queryer, jobID string) (domain.JobSettings, error) {
	var encoded string
	if err := q.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", jobID).Scan(&encoded); err != nil {
		return domain.JobSettings{}, err
	}
	var settings domain.JobSettings
	if err := json.Unmarshal([]byte(encoded), &settings); err != nil {
		return domain.JobSettings{}, fmt.Errorf("job %s holds unusable settings: %w", jobID, err)
	}
	return settings, nil
}

// settingsOf is a project's settings as a job keeps them.
func (s *Store) settingsOf(ctx context.Context, q queryer, row projectRow) (domain.JobSettings, error) {
	settings := domain.JobSettings{Ask: row.ask, Chronological: row.chronological, ProjectName: row.name}
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
			return domain.JobSettings{}, err
		}
		settings.SongID, settings.SongStart = id, start
	}
	return settings, nil
}

// Job returns owner's job.
func (s *Store) Job(ctx context.Context, owner, jobID string) (domain.Job, error) {
	var job domain.Job
	var step, reason, message sql.NullString
	var created string
	var finished sql.NullString
	var plans int
	err := s.db.QueryRowContext(ctx, `SELECT id, project_id, kind, state, step, wait_reason, error, created_at, finished_at, plans
		FROM jobs WHERE id = ? AND owner_user_id = ?`, jobID, owner).
		Scan(&job.ID, &job.ProjectID, &job.Kind, &job.State, &step, &reason, &message, &created, &finished, &plans)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	job.Step, job.WaitReason, job.Error = step.String, reason.String, message.String
	if at := scanTime(sql.NullString{String: created, Valid: true}); at != nil {
		job.CreatedAt = *at
	}
	job.FinishedAt = scanTime(finished)
	done := map[string]domain.Step{}
	rows, err := s.db.QueryContext(ctx, "SELECT name, state, started_at, finished_at FROM steps WHERE job_id = ?", jobID)
	if err != nil {
		return domain.Job{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var st domain.Step
		var started, ended sql.NullString
		if err := rows.Scan(&st.Name, &st.State, &started, &ended); err != nil {
			return domain.Job{}, err
		}
		st.Started, st.Finished = scanTime(started), scanTime(ended)
		if st.State == JobRunning && job.State != JobRunning {
			// The step stopped with the job: it shows why.
			st.State = job.State
		}
		done[st.Name] = st
	}
	if err := rows.Err(); err != nil {
		return domain.Job{}, err
	}
	// The steps of as many 方案 as the job started with.
	for _, name := range s.stepNames(job.Kind, plans) {
		st, ok := done[name]
		if !ok {
			st = domain.Step{Name: name, State: "pending"}
		}
		job.Steps = append(job.Steps, st)
	}
	if job.Missing, err = s.missingOf(ctx, jobID); err != nil {
		return domain.Job{}, err
	}
	if job.Versions, err = s.versionsOf(ctx, "job_id", jobID, "plan"); err != nil {
		return domain.Job{}, err
	}
	if len(job.Versions) == 0 && job.Kind == KindEdit {
		// The same revision saved again: its film was saved once, by the
		// job before, under the same version key.
		var key string
		err := s.db.QueryRowContext(ctx, "SELECT input_key FROM steps WHERE job_id = ? AND name = 'save' AND state = 'done'", jobID).Scan(&key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.Job{}, err
		}
		if key != "" {
			if job.Versions, err = s.versionsOf(ctx, "version_key", key, "plan"); err != nil {
				return domain.Job{}, err
			}
		}
	}
	if len(job.Versions) > 0 {
		job.Version = &job.Versions[0]
	}
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

// versionsOf lists the versions whose column is value, ordered by order.
func (s *Store) versionsOf(ctx context.Context, column, value, order string) ([]domain.Version, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+versionColumns+" FROM versions v WHERE v."+column+" = ? ORDER BY v."+order, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []domain.Version{}
	for rows.Next() {
		version, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// Cancel stops owner's job: a queued or waiting one at once, a running one
// once its step can stop; a film it made is not saved.
func (s *Store) Cancel(ctx context.Context, owner, jobID string) (domain.Job, error) {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.Job{}, err
	}
	defer unlock()
	now := sqlitedb.FormatTime(s.now())
	var state string
	err = s.db.QueryRowContext(ctx, `UPDATE jobs SET cancel_requested = 1, updated_at = ?,
		state = CASE WHEN state = 'running' THEN state ELSE 'cancelled' END,
		finished_at = CASE WHEN state = 'running' THEN finished_at ELSE ? END
		WHERE id = ? AND owner_user_id = ? AND save_started = 0 AND `+db.ActiveStateQuery+" RETURNING state", now, now, jobID, owner).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.Job(ctx, owner, jobID); err != nil {
			return domain.Job{}, err
		}
		return domain.Job{}, domain.ErrJobState
	}
	if err != nil {
		return domain.Job{}, err
	}
	// The round in progress is over: its answers are told the job was
	// cancelled. Its Hermes job is the next round's to remove.
	if err := s.db.EndRounds(ctx, jobID, s.now(), "cancelled"); err != nil {
		return domain.Job{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE jobs SET plan_wanted = 0 WHERE id = ?", jobID); err != nil {
		return domain.Job{}, err
	}
	s.syncMarkers(ctx)
	if state == JobRunning {
		s.cancelRunning(jobID)
	} else {
		s.removeJobFiles(jobID)
		s.releaseSong(ctx, owner)
	}
	return s.Job(ctx, owner, jobID)
}

// resume puts a job that waits, needs the member or failed back in the
// queue. It no longer wants a round: a round is claimed only for a job that
// came to planning and waits there (awaitPlan).
func (s *Store) resume(ctx context.Context, tx *db.Tx, owner, jobID string, states ...string) error {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
	args := []any{sqlitedb.FormatTime(s.now()), sqlitedb.FormatTime(s.now()), jobID, owner}
	for _, state := range states {
		args = append(args, state)
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued', attempts = 0, not_before = ?, updated_at = ?, finished_at = NULL,
		wait_reason = '', error_class = '', error = '', missing_json = NULL, plan_wanted = 0 WHERE id = ? AND owner_user_id = ? AND cancel_requested = 0 AND state IN (`+marks+")", args...)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return domain.ErrJobState
	}
	return nil
}

// DropAssets takes videos the job can no longer use out of it and its
// project, and out of the grant, and goes on; the catalog changes, so any
// plan already taken is planned again. With nothing left the job can only
// be cancelled.
func (s *Store) DropAssets(ctx context.Context, owner, jobID string, assetIDs []string) (domain.Job, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return domain.Job{}, err
	}
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.Job{}, err
	}
	defer unlock()
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	// An edit takes no video out: the member replaces the shots that use it
	// and saves again (ADR 0039, decision 6; docs/architecture/montage-center.md,
	// 出片后的二次编辑: 任务的种类). A recut
	// plans anew as generating does, and goes on without them the same way.
	if job.Kind == KindEdit || job.State != JobNeedsInput || !slices.Contains([]string{NeedMissing, NeedAnalysis, NeedTooLong}, job.WaitReason) {
		return domain.Job{}, domain.ErrJobState
	}
	for _, id := range ids {
		if !slices.Contains(job.Missing, id) {
			return domain.Job{}, domain.ErrJobState
		}
	}
	var count int
	var grantID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT grant_id, (SELECT COUNT(*) FROM job_assets WHERE job_id = ?)
		FROM projects WHERE id = ?`, jobID, job.ProjectID).Scan(&grantID, &count); err != nil {
		return domain.Job{}, err
	}
	if count <= len(ids) {
		return domain.Job{}, domain.ErrJobState
	}
	// Remove is idempotent. If its answer or the local commit is lost, the
	// job remains in needs_input and repeating the same drop finishes it.
	if grantID.Valid {
		if err := s.grants.Remove(ctx, grantID.String, ids); err != nil && photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
			return domain.Job{}, domain.GrantError(err)
		}
	}
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		for _, id := range ids {
			if err := tx.DeleteJobAsset(ctx, jobID, id); err != nil {
				return err
			}
			if err := tx.DeleteProjectAsset(ctx, job.ProjectID, id); err != nil {
				return err
			}
		}
		// The catalog changes: plans already taken are void, the rounds
		// planning the old one are over, and both steps are planned again
		// in a new epoch.
		if err := tx.VoidPlans(ctx, jobID, s.now()); err != nil {
			return err
		}
		if err := s.newPlanEpoch(ctx, tx, jobID, "the job's videos changed"); err != nil {
			return err
		}
		return s.resume(ctx, tx, owner, jobID, JobNeedsInput)
	})
	if err != nil {
		return domain.Job{}, err
	}
	s.syncMarkers(ctx)
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// RetryAnalysis asks the photo library to analyse again the videos whose
// analysis failed, and goes on.
func (s *Store) RetryAnalysis(ctx context.Context, owner, jobID string) (domain.Job, error) {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.Job{}, err
	}
	defer unlock()
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if job.WaitReason != NeedAnalysis {
		return domain.Job{}, domain.ErrJobState
	}
	grantID, _, err := s.jobGrant(ctx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if err := s.analyzeAssets(ctx, grantID, job.Missing, true); err != nil {
		return domain.Job{}, domain.GrantError(err)
	}
	if err := s.db.Update(ctx, func(tx *db.Tx) error { return s.resume(ctx, tx, owner, jobID, JobNeedsInput) }); err != nil {
		return domain.Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// Retry goes on from the step a job stopped at, once the member has made
// room or otherwise fixed what it needed, or the local model it lacked is
// installed.
func (s *Store) Retry(ctx context.Context, owner, jobID string) (domain.Job, error) {
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if !slices.Contains([]string{NeedStorageFull, NeedNoFootage, NeedMusic, NeedModel}, job.WaitReason) {
		return domain.Job{}, domain.ErrJobState
	}
	if err := s.db.Update(ctx, func(tx *db.Tx) error { return s.resume(ctx, tx, owner, jobID, JobNeedsInput) }); err != nil {
		return domain.Job{}, err
	}
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// Replan plans again from the step not yet taken: the member's one click
// after the planning failed (FailPlanning), or while the job waits for a
// VNAS that has not taken it; and for a job stopped before 2026-11-25 whose
// plan was not handed back, kept failing its checks or whose planning failed.
// A round still in progress is over: what it hands back from now on is not
// kept, and the rounds tried so far do not count against the new epoch.
// A failed job goes on only as its project's latest, while no other job of
// the project runs, and with the song it was started with still there.
func (s *Store) Replan(ctx context.Context, owner, jobID string) (domain.Job, error) {
	job, err := s.Job(ctx, owner, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	failedPlanning := job.State == JobFailed && job.WaitReason == FailPlanning
	if !failedPlanning && !slices.Contains([]string{NeedVNASUnknown, NeedPlanInvalid, NeedPlanError, WaitVNAS}, job.WaitReason) {
		return domain.Job{}, domain.ErrJobState
	}
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		if failedPlanning {
			if err := s.reopenable(ctx, tx, job.ProjectID, jobID); err != nil {
				return err
			}
		}
		if err := s.newPlanEpoch(ctx, tx, jobID, "planned again"); err != nil {
			return err
		}
		return s.resume(ctx, tx, owner, jobID, JobNeedsInput, JobWaiting, JobFailed)
	})
	if err != nil {
		return domain.Job{}, err
	}
	s.syncMarkers(ctx)
	s.wakeJobs()
	return s.Job(ctx, owner, jobID)
}

// reopenable answers whether a failed job may go on: it is its project's
// latest, no other job of the project runs (a project has one at a time),
// and the song it was started with is still kept, which ending it may have
// let go (releaseSong) when the project has another since.
func (s *Store) reopenable(ctx context.Context, tx *db.Tx, projectID, jobID string) error {
	var latest string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM jobs WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", projectID).
		Scan(&latest); err != nil {
		return err
	}
	if latest != jobID {
		return domain.ErrJobState
	}
	if err := projectIdle(ctx, tx, projectID); err != nil {
		return err
	}
	if s.jobMusic != Music(s) {
		// A test's stand-in keeps its own songs.
		return nil
	}
	settings, err := jobSettings(ctx, tx, jobID)
	if err != nil {
		return err
	}
	_, err = s.song(ctx, tx, settings.SongID)
	return err
}

// Kinds of notice.
const (
	NoticeWaiting  = "waiting_for_planning"
	NoticePlanning = "planning"
	NoticeNeeds    = "needs_input"
	NoticeSaved    = "saved"
	// NoticeFailed: a job failed lately; its reason says whether the member
	// can plan it again (FailPlanning).
	NoticeFailed = "failed"
)

// noticeWindow is how long a saved film, or a job that failed, stays a
// notice.
const noticeWindow = 24 * time.Hour

// Notices lists owner's jobs that wait for planning, are being planned, need
// the member, saved a film lately, or failed lately. A job is being planned
// while a round of its epoch is claimed or started: it waits for VNAS all
// the while, so its own state does not tell.
func (s *Store) Notices(ctx context.Context, owner string) ([]domain.Notice, error) {
	since := sqlitedb.FormatTime(s.now().Add(-noticeWindow))
	rows, err := s.db.QueryContext(ctx, `SELECT j.id, j.project_id, p.name, j.state, j.wait_reason, j.error, j.updated_at, IFNULL(v.film_asset_id, ''),
		EXISTS (SELECT 1 FROM rounds r WHERE r.job_id = j.id AND r.epoch = j.plan_epoch AND r.state IN ('claimed', 'started'))
		FROM jobs j JOIN projects p ON p.id = j.project_id LEFT JOIN versions v ON v.job_id = j.id AND v.plan = 1
		WHERE j.owner_user_id = ? AND ((j.state = 'waiting' AND j.wait_reason = ?) OR j.state = 'needs_input'
		  OR (j.state IN ('done', 'failed') AND j.finished_at >= ?)) ORDER BY j.updated_at DESC`, owner, WaitVNAS, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := []domain.Notice{}
	for rows.Next() {
		var n domain.Notice
		var state, detail, at string
		var planning bool
		if err := rows.Scan(&n.JobID, &n.ProjectID, &n.Project, &state, &n.Reason, &detail, &at, &n.FilmID, &planning); err != nil {
			return nil, err
		}
		switch {
		case state == JobDone:
			n.Kind = NoticeSaved
		case state == JobFailed:
			n.Kind, n.Detail = NoticeFailed, detail
		case state == JobWaiting && planning:
			n.Kind = NoticePlanning
		case state == JobWaiting:
			n.Kind, n.Detail = NoticeWaiting, detail
		default:
			n.Kind = NoticeNeeds
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

// runsEdits reports whether this store can render a draft: as a film,
// without a plan.
func (s *Store) runsEdits() bool {
	return s.jobCompute != nil && s.editing != nil && s.jobMusic != nil
}
