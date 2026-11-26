package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// The scheduler (docs/architecture/videocut-center.md, 持久任务): one
// goroutine runs one step of one job at a time. A job claimed holds a lease
// of five minutes, renewed every 30 seconds; the service is the only one,
// so at start every running job goes back to the queue at once and goes on
// from its first step not done.

const (
	// heartbeat renews a running job's lease.
	heartbeat = 30 * time.Second
	// waitPoll is how often a job waiting for the photo library's
	// analysis, the song's or the plan looks again.
	waitPoll = time.Minute
	// idlePoll bounds how long the scheduler sleeps with nothing due.
	idlePoll = 10 * time.Minute
	// tmpDir holds each job's intermediate files, tmp/<job-id>/.
	tmpDir = "tmp"
)

// stepWait: the step cannot go on yet; the job waits for reason and looks
// again after delay, without counting an attempt.
type stepWait struct {
	reason string
	delay  time.Duration
	// detail says more for the project page, such as why VNAS has not
	// taken the plan.
	detail string
	// missing are the videos waited for, where the member is told which:
	// an edit's videos in the trash.
	missing []string
}

func (w *stepWait) Error() string { return "waiting: " + w.reason }

// stepNeeds: the member must act (docs/specs/videocut-center.md); missing
// names the videos concerned.
type stepNeeds struct {
	reason  string
	missing []string
	err     error
	// said, when set, is what the job's page shows of why, in place of
	// the error.
	said string
}

func (n *stepNeeds) Error() string {
	if n.err != nil {
		return "needs input: " + n.reason + ": " + n.err.Error()
	}
	return "needs input: " + n.reason
}

// errCancelled is the cause that ends a step the member cancelled.
var errCancelled = errors.New("the job was cancelled")

// claimed is a job the scheduler runs.
type claimed struct {
	id, owner, projectID string
	// kind is generate, edit or recut (#596).
	kind     string
	settings domain.JobSettings
	// plans is how many 方案 the job makes, as it started.
	plans int
}

// wakeJobs tells the scheduler a job is due.
func (s *Store) wakeJobs() {
	select {
	case s.jobWake <- struct{}{}:
	default:
	}
}

// cancelRunning ends the running step of jobID, if it runs.
func (s *Store) cancelRunning(jobID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if cancel, ok := s.running[jobID]; ok {
		cancel(errCancelled)
	}
}

// RecoverJobs puts the jobs left running back in the queue and removes the
// intermediate files of jobs that ended; it runs before RunJobs.
func (s *Store) RecoverJobs(ctx context.Context) error {
	now := s.now()
	if err := s.db.Update(ctx, func(tx *db.Tx) error {
		if err := tx.CancelRequestedJobs(ctx, now); err != nil {
			return err
		}
		return tx.RequeueRunningJobs(ctx, now)
	}); err != nil {
		return err
	}
	// The run directory starts empty: the markers come back from the jobs.
	if err := s.SyncMarkers(ctx); err != nil {
		s.logger.WarnContext(ctx, "rebuilding the planning markers failed", "error", err)
	}
	return s.removeEndedFiles(ctx)
}

// removeEndedFiles removes tmp/<job-id>/ of every job not in progress.
func (s *Store) removeEndedFiles(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(s.root, tmpDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		active, err := s.db.JobActive(ctx, entry.Name())
		if err != nil {
			return err
		}
		if !active {
			s.removeJobFiles(entry.Name())
		}
	}
	return nil
}

func (s *Store) jobDir(jobID string) string { return filepath.Join(s.root, tmpDir, jobID) }

func (s *Store) removeJobFiles(jobID string) {
	if err := os.RemoveAll(s.jobDir(jobID)); err != nil {
		s.logger.Warn("removing a job's files failed", "job", jobID, "error", err)
	}
}

// RunJobs runs the jobs until ctx ends.
func (s *Store) RunJobs(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.SweepRounds(ctx); err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "ending the planning rounds out of time failed", "error", err)
		}
		job, found, err := s.claim(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "claiming a VideoCut job failed", "error", err)
			}
			found = false
		}
		if found {
			s.run(ctx, job)
			continue
		}
		wait := jobRetryDelay
		if err == nil {
			wait = s.nextDue(ctx)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-s.jobWake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// nextDue is how long until a queued or waiting job is due.
func (s *Store) nextDue(ctx context.Context) time.Duration {
	next, err := s.db.NextNotBefore(ctx)
	if err != nil || !next.Valid {
		return idlePoll
	}
	at := scanTime(next)
	if at == nil {
		return idlePoll
	}
	return min(max(at.Sub(s.now()), 0), idlePoll)
}

// claim takes the job due first.
func (s *Store) claim(ctx context.Context) (claimed, bool, error) {
	var job claimed
	found := false
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		now := s.now()
		due, err := tx.DueJob(ctx, now)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job.id, job.owner, job.projectID, job.kind, job.plans = due.ID, due.Owner, due.ProjectID, due.Kind, due.Plans
		if err := json.Unmarshal([]byte(due.Settings), &job.settings); err != nil {
			return tx.FailInvalidJob(ctx, job.id, "invalid job settings: "+err.Error(), now)
		}
		found = true
		return tx.RunJob(ctx, job.id, now.Add(jobLease), now)
	})
	return job, found, err
}

// run runs job's steps until it ends, waits or fails a step.
func (s *Store) run(ctx context.Context, job claimed) {
	stepCtx, cancel := context.WithCancelCause(ctx)
	s.runMu.Lock()
	s.running[job.id] = cancel
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		delete(s.running, job.id)
		s.runMu.Unlock()
		cancel(nil)
	}()
	stop := s.keepJob(stepCtx, job, cancel)
	err := s.runSteps(stepCtx, job)
	stop()
	if cause := context.Cause(stepCtx); err != nil && stepCtx.Err() != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		// The lease keeper or the member stopped the step: its reason wins.
		err = cause
	}
	s.settleJob(context.WithoutCancel(ctx), job, err, ctx.Err() != nil)
}

// keepJob renews the job's lease every 30 seconds, ends the step when the
// member cancels, and every lifecycleCheck looks whether its videos are
// still there (素材与成片的生命周期).
func (s *Store) keepJob(ctx context.Context, job claimed, cancel context.CancelCauseFunc) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(s.beat)
		defer ticker.Stop()
		lastCheck := s.now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			now := s.now()
			requested, saving, err := s.db.RenewLease(ctx, job.id, now.Add(jobLease), now)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				cancel(errCancelled) // the project went
				return
			case err != nil:
				s.logger.WarnContext(ctx, "renewing a VideoCut job's lease failed", "job", job.id, "error", err)
			case requested:
				cancel(errCancelled)
				return
			}
			if !saving && now.Sub(lastCheck) >= lifecycleCheck {
				lastCheck = now
				if err := s.checkAssets(ctx, job); err != nil {
					var wait *stepWait
					var needs *stepNeeds
					if errors.As(err, &wait) || errors.As(err, &needs) {
						cancel(err)
						return
					}
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// settleJob records how a run ended.
func (s *Store) settleJob(ctx context.Context, job claimed, err error, stopping bool) {
	if err == nil {
		// Saving finishes the job and releases its intermediates.
		s.releaseSong(ctx, job.owner)
		return
	}
	dbErr := s.db.Update(ctx, func(tx *db.Tx) error {
		attempts, cancelled, saving, readErr := tx.RunningJob(ctx, job.id)
		if readErr != nil {
			if errors.Is(readErr, sql.ErrNoRows) {
				return nil
			}
			return readErr
		}
		now := s.now()
		at := sqlitedb.FormatTime(now)
		state, reason, class, message := JobQueued, "", "", ""
		var finished, missing any
		next := at
		var wait *stepWait
		var needs *stepNeeds
		var bad *domain.StepFailed
		var refused *saveRefusal
		if errors.As(err, &refused) {
			saving = false
		}
		switch {
		case cancelled || errors.Is(err, errCancelled):
			state, finished = JobCancelled, at
		case stopping && refused == nil:
			// The service is stopping. Resume this step immediately on startup.
		case saving:
			// The photo library may already have committed. Replay the same
			// version key until its outcome is known, retaining the rendered film.
			state, reason, message, next = JobWaiting, WaitPhotos, err.Error(), sqlitedb.FormatTime(now.Add(waitPoll))
		case errors.As(err, &wait):
			// The detail says more for the project page, such as why VNAS has
			// not taken the plan.
			state, reason, message, next = JobWaiting, wait.reason, wait.detail, sqlitedb.FormatTime(now.Add(wait.delay))
			if len(wait.missing) != 0 {
				encoded, _ := json.Marshal(wait.missing)
				missing = string(encoded)
			}
		case errors.As(err, &needs):
			state, reason, class, message = JobNeedsInput, needs.reason, "input", cmp.Or(needs.said, needs.Error())
			if len(needs.missing) != 0 {
				encoded, _ := json.Marshal(needs.missing)
				missing = string(encoded)
			}
		case errors.As(err, &bad):
			state, reason, class, message, finished = JobFailed, bad.Reason, "invalid", bad.Error(), at
		default:
			attempts++
			class, message = "retryable", err.Error()
			if attempts >= jobMaxAttempts {
				state, finished = JobFailed, at
			} else {
				next = sqlitedb.FormatTime(now.Add(jobRetryDelay * time.Duration(attempts)))
			}
		}
		return tx.SettleJob(ctx, job.id, db.JobSettlement{State: state, WaitReason: reason, Attempts: attempts, ErrorClass: class, Error: message,
			Missing: missing, SaveStarted: saving, NotBefore: next, FinishedAt: finished, UpdatedAt: at})
	})
	if dbErr != nil {
		s.logger.ErrorContext(ctx, "recording a VideoCut job's step failed", "job", job.id, "error", dbErr)
		return
	}
	s.logger.InfoContext(ctx, "VideoCut job stopped", "job", job.id, "reason", err.Error())
	if s.finished(ctx, job.id) {
		s.removeJobFiles(job.id)
		s.releaseSong(ctx, job.owner)
	}
}

func (s *Store) cancelRequested(ctx context.Context, jobID string) bool {
	requested, err := s.db.CancelRequested(ctx, jobID)
	return err == nil && requested
}

func (s *Store) finished(ctx context.Context, jobID string) bool {
	active, err := s.db.JobActive(ctx, jobID)
	return err == nil && !active
}
