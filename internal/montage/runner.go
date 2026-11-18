package montage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The scheduler (docs/architecture/montage-center.md, 持久任务): one
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
}

func (w *stepWait) Error() string { return "waiting: " + w.reason }

// stepNeeds: the member must act (docs/specs/montage-center.md); missing
// names the videos concerned.
type stepNeeds struct {
	reason  string
	missing []string
	err     error
}

func (n *stepNeeds) Error() string {
	if n.err != nil {
		return "needs input: " + n.reason + ": " + n.err.Error()
	}
	return "needs input: " + n.reason
}

// stepFailed: the step's input or output is wrong and would be wrong again;
// the job fails without retrying.
type stepFailed struct{ err error }

func (f *stepFailed) Error() string { return f.err.Error() }
func (f *stepFailed) Unwrap() error { return f.err }

func failed(format string, args ...any) error { return &stepFailed{fmt.Errorf(format, args...)} }

// errCancelled is the cause that ends a step the member cancelled.
var errCancelled = errors.New("the job was cancelled")

// claimed is a job the scheduler runs.
type claimed struct {
	id, owner, projectID string
	settings             JobSettings
	attempts             int
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
	now := sqlitedb.FormatTime(s.now())
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'queued', lease_until = NULL, not_before = ?, updated_at = ?
		WHERE state = 'running'`, now, now); err != nil {
		return err
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
		var active bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = ? AND "+activeStateQuery+")",
			entry.Name()).Scan(&active); err != nil {
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
		job, found, err := s.claim(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "claiming a montage job failed", "error", err)
			}
			found = false
		}
		if found {
			s.run(ctx, job)
			continue
		}
		wait := s.nextDue(ctx)
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
	var next sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT MIN(not_before) FROM jobs WHERE state IN ('queued', 'waiting')").Scan(&next); err != nil || !next.Valid {
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
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		var settings string
		err := tx.QueryRowContext(ctx, `SELECT id, owner_user_id, project_id, settings_json, attempts FROM jobs
			WHERE state IN ('queued', 'waiting') AND not_before <= ? AND cancel_requested = 0
			ORDER BY not_before, created_at LIMIT 1`, sqlitedb.FormatTime(now)).
			Scan(&job.id, &job.owner, &job.projectID, &settings, &job.attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(settings), &job.settings); err != nil {
			return err
		}
		found = true
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'running', lease_until = ?, heartbeat_at = ?, updated_at = ? WHERE id = ?`,
			sqlitedb.FormatTime(now.Add(jobLease)), sqlitedb.FormatTime(now), sqlitedb.FormatTime(now), job.id)
		return err
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
	s.settle(context.WithoutCancel(ctx), job, err, ctx.Err() != nil)
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
			var requested bool
			err := s.db.QueryRowContext(ctx, `UPDATE jobs SET lease_until = ?, heartbeat_at = ? WHERE id = ? RETURNING cancel_requested`,
				sqlitedb.FormatTime(now.Add(jobLease)), sqlitedb.FormatTime(now), job.id).Scan(&requested)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				cancel(errCancelled) // the project went
				return
			case err != nil:
				s.logger.WarnContext(ctx, "renewing a montage job's lease failed", "job", job.id, "error", err)
			case requested:
				cancel(errCancelled)
				return
			}
			if now.Sub(lastCheck) >= lifecycleCheck {
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

// settle records how a run ended.
func (s *Store) settle(ctx context.Context, job claimed, err error, stopping bool) {
	now := s.now()
	at := sqlitedb.FormatTime(now)
	var wait *stepWait
	var needs *stepNeeds
	var bad *stepFailed
	var statement string
	var args []any
	switch {
	case err == nil:
		return // the save step recorded the end
	case errors.Is(err, errCancelled) || s.cancelRequested(ctx, job.id):
		statement, args = `UPDATE jobs SET state = 'cancelled', lease_until = NULL, finished_at = ?, updated_at = ? WHERE id = ?`, []any{at, at, job.id}
	case stopping:
		// The service stops: the job goes on from this step at the next start.
		statement, args = `UPDATE jobs SET state = 'queued', lease_until = NULL, not_before = ?, updated_at = ? WHERE id = ?`, []any{at, at, job.id}
	case errors.As(err, &wait):
		statement, args = `UPDATE jobs SET state = 'waiting', wait_reason = ?, lease_until = NULL, not_before = ?, updated_at = ? WHERE id = ?`,
			[]any{wait.reason, sqlitedb.FormatTime(now.Add(wait.delay)), at, job.id}
	case errors.As(err, &needs):
		var missing any
		if len(needs.missing) > 0 {
			encoded, _ := json.Marshal(needs.missing)
			missing = string(encoded)
		}
		statement, args = `UPDATE jobs SET state = 'needs_input', wait_reason = ?, missing_json = ?, error_class = 'input', error = ?,
			lease_until = NULL, updated_at = ? WHERE id = ?`, []any{needs.reason, missing, needs.Error(), at, job.id}
	case errors.As(err, &bad):
		statement, args = `UPDATE jobs SET state = 'failed', error_class = 'invalid', error = ?, lease_until = NULL, finished_at = ?, updated_at = ?
			WHERE id = ?`, []any{bad.Error(), at, at, job.id}
	case job.attempts+1 >= jobMaxAttempts:
		statement, args = `UPDATE jobs SET state = 'failed', attempts = attempts + 1, error_class = 'retryable', error = ?, lease_until = NULL,
			finished_at = ?, updated_at = ? WHERE id = ?`, []any{err.Error(), at, at, job.id}
	default:
		// Tried again after one minute, then two.
		delay := jobRetryDelay * time.Duration(job.attempts+1)
		statement, args = `UPDATE jobs SET state = 'queued', attempts = attempts + 1, error_class = 'retryable', error = ?, lease_until = NULL,
			not_before = ?, updated_at = ? WHERE id = ?`, []any{err.Error(), sqlitedb.FormatTime(now.Add(delay)), at, job.id}
	}
	if _, dbErr := s.db.ExecContext(ctx, statement, args...); dbErr != nil {
		s.logger.ErrorContext(ctx, "recording a montage job's step failed", "job", job.id, "error", dbErr)
		return
	}
	s.logger.InfoContext(ctx, "montage job stopped", "job", job.id, "reason", err.Error())
	if s.finished(ctx, job.id) {
		s.removeJobFiles(job.id)
	}
}

func (s *Store) cancelRequested(ctx context.Context, jobID string) bool {
	var requested bool
	err := s.db.QueryRowContext(ctx, "SELECT cancel_requested FROM jobs WHERE id = ?", jobID).Scan(&requested)
	return err == nil && requested
}

func (s *Store) finished(ctx context.Context, jobID string) bool {
	var active bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = ? AND "+activeStateQuery+")", jobID).Scan(&active)
	return err == nil && !active
}
