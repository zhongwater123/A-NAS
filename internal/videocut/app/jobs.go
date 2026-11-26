package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// A job makes the films of a project to choose from, 方案一 and 方案二
// (docs/architecture/videocut-center.md, 持久任务): analysis, catalog and
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

// versionOf is a version as the domain reads it.
func versionOf(row db.VersionRow) domain.Version {
	version := domain.Version{ID: row.ID, Number: row.Number, JobID: row.JobID, Plan: row.Plan, FilmAssetID: row.FilmAssetID, Editable: row.Editable,
		Kind: row.Kind, SourceVersionID: row.SourceVersionID, SourceNumber: row.SourceNumber, Change: row.Change}
	created, revision := row.Created, row.DraftRevision
	if revision.Valid {
		n := int(revision.Int64)
		version.DraftRevision = &n
	}
	if at := scanTime(sql.NullString{String: created, Valid: true}); at != nil {
		version.CreatedAt = *at
	}
	return version
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
		if row.State != projectReady {
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
		if jobID, err = s.queueJob(ctx, tx, settings, db.NewJob{Owner: owner, ProjectID: projectID, RequestKey: requestKey, Kind: KindGenerate, Plans: s.plans}); err != nil {
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
	id, priorKind, prior, err := q.KeyedJob(ctx, owner, requestKey, column)
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
	active, err := tx.ProjectJobActive(ctx, projectID)
	if err != nil {
		return err
	}
	if active {
		return domain.ErrJobActive
	}
	return nil
}

// queueJob writes job, due now, with settings, and returns its ID: an
// edit names the version it came from, its draft and the draft's revision;
// a recut the version. job's ID and Settings are queueJob's to fill.
func (s *Store) queueJob(ctx context.Context, tx *db.Tx, settings domain.JobSettings, job db.NewJob) (string, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	job.ID, job.Settings = newID("job"), string(encoded)
	return job.ID, tx.AddJob(ctx, job, s.now())
}

// jobSettings are a job's settings as it was started with them.
func jobSettings(ctx context.Context, q queryer, jobID string) (domain.JobSettings, error) {
	encoded, err := q.JobSettings(ctx, jobID)
	if err != nil {
		return domain.JobSettings{}, err
	}
	var settings domain.JobSettings
	if err := json.Unmarshal([]byte(encoded), &settings); err != nil {
		return domain.JobSettings{}, fmt.Errorf("job %s holds unusable settings: %w", jobID, err)
	}
	return settings, nil
}

// settingsOf is a project's settings as a job keeps them.
func (s *Store) settingsOf(ctx context.Context, q queryer, row db.ProjectRow) (domain.JobSettings, error) {
	settings := domain.JobSettings{Ask: row.Ask, Chronological: row.Chronological, ProjectName: row.Name}
	if s.jobMusic != nil {
		chosen := s.jobMusic.Chosen
		if s.jobMusic == Music(s) {
			// The store's own songs, read in the job's transaction: the
			// database has one connection.
			chosen = func(ctx context.Context, owner, projectID string) (string, float64, error) {
				return s.chosenIn(ctx, q, owner, projectID)
			}
		}
		id, start, err := chosen(ctx, row.Owner, row.ID)
		if err != nil {
			return domain.JobSettings{}, err
		}
		settings.SongID, settings.SongStart = id, start
	}
	return settings, nil
}

// Job returns owner's job.
func (s *Store) Job(ctx context.Context, owner, jobID string) (domain.Job, error) {
	row, err := s.db.Job(ctx, owner, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	job := domain.Job{ID: row.ID, ProjectID: row.ProjectID, Kind: row.Kind, State: row.State,
		Step: row.Step.String, WaitReason: row.WaitReason.String, Error: row.Error.String}
	if at := scanTime(sql.NullString{String: row.Created, Valid: true}); at != nil {
		job.CreatedAt = *at
	}
	job.FinishedAt = scanTime(row.Finished)
	done := map[string]domain.Step{}
	kept, err := s.db.JobSteps(ctx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	for _, row := range kept {
		st := domain.Step{Name: row.Name, State: row.State}
		st.Started, st.Finished = scanTime(row.Started), scanTime(row.Finished)
		if st.State == JobRunning && job.State != JobRunning {
			// The step stopped with the job: it shows why.
			st.State = job.State
		}
		done[st.Name] = st
	}
	// The steps of as many 方案 as the job started with.
	for _, name := range s.stepNames(job.Kind, row.Plans) {
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
		key, err := s.db.DoneSaveKey(ctx, jobID)
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
	raw, err := s.db.JobMissing(ctx, jobID)
	if err != nil || !raw.Valid {
		return nil, err
	}
	var missing []string
	return missing, json.Unmarshal([]byte(raw.String), &missing)
}

// versionsOf lists the versions whose column is value, ordered by order.
func (s *Store) versionsOf(ctx context.Context, column, value, order string) ([]domain.Version, error) {
	kept, err := s.db.Versions(ctx, column, value, order)
	if err != nil {
		return nil, err
	}
	versions := make([]domain.Version, 0, len(kept))
	for _, row := range kept {
		versions = append(versions, versionOf(row))
	}
	return versions, nil
}

// Cancel stops owner's job: a queued or waiting one at once, a running one
// once its step can stop; a film it made is not saved.
func (s *Store) Cancel(ctx context.Context, owner, jobID string) (domain.Job, error) {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.Job{}, err
	}
	defer unlock()
	state, err := s.db.RequestCancel(ctx, owner, jobID, s.now())
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
	if err := s.db.UnwantRound(ctx, jobID); err != nil {
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
	requeued, err := tx.Requeue(ctx, owner, jobID, s.now(), states...)
	if err != nil {
		return err
	}
	if !requeued {
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
	// and saves again (ADR 0039, decision 6; docs/architecture/videocut-center.md,
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
	grantID, count, err := s.db.ProjectGrantAndJobAssets(ctx, job.ProjectID, jobID)
	if err != nil {
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
	latest, err := tx.LatestJob(ctx, projectID)
	if err != nil {
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
	kept, err := s.db.Notices(ctx, owner, WaitVNAS, s.now().Add(-noticeWindow))
	if err != nil {
		return nil, err
	}
	notices := []domain.Notice{}
	for _, row := range kept {
		n := domain.Notice{JobID: row.JobID, ProjectID: row.ProjectID, Project: row.Project, Reason: row.Reason, FilmID: row.FilmID}
		state, detail, at, planning := row.State, row.Detail, row.At, row.Planning
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
	return notices, nil
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
