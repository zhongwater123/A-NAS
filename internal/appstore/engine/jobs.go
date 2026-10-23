package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// Each app's latest job is kept in the agent's state directory, written when
// the job starts and when it ends. An agent that stops or crashes while
// Compose runs therefore finds the job still running when it starts again:
// an install that never finished its checks is rolled back, and a cut-short
// uninstall is finished (issue #169).

// errStopping ends an install the agent is stopped in the middle of.
var errStopping = errors.New("容器代理停止，安装被中断")

// errInterrupted ends an install the agent found still running when it started.
var errInterrupted = errors.New("容器代理在安装过程中重启，安装没有完成核对")

// jobRecord is a job as the state directory keeps it.
type jobRecord struct {
	AppID      string             `json:"appId"`
	Action     appstore.JobAction `json:"action"`
	State      appstore.JobState  `json:"state"`
	StartedAt  time.Time          `json:"startedAt"`
	FinishedAt time.Time          `json:"finishedAt,omitzero"`
	Output     []string           `json:"output,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func (s *Store) jobPath(id string) string {
	return filepath.Join(s.stateDir, "jobs", id+".json")
}

// saveJob replaces the app's job record. Compose output can carry an app's
// own logs, so only the agent can read it.
func (s *Store) saveJob(job *appstore.Job) error {
	data, err := json.Marshal(jobRecord{
		AppID: job.AppID, Action: job.Action, State: job.State, StartedAt: job.StartedAt,
		FinishedAt: job.FinishedAt, Output: job.Output, Error: job.Error,
	})
	if err != nil {
		return err
	}
	path := s.jobPath(job.AppID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// readJobs returns the recorded jobs, skipping records it cannot read and
// reporting them.
func (s *Store) readJobs() ([]*appstore.Job, error) {
	files, err := os.ReadDir(filepath.Join(s.stateDir, "jobs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []*appstore.Job
	var problems []error
	for _, file := range files {
		id, ok := strings.CutSuffix(file.Name(), ".json")
		if !ok || !appstore.ValidID(id) {
			continue
		}
		var record jobRecord
		data, err := os.ReadFile(s.jobPath(id))
		if err == nil {
			err = json.Unmarshal(data, &record)
		}
		if err == nil && (record.AppID != id || (record.Action != appstore.JobInstall && record.Action != appstore.JobUninstall)) {
			err = errors.New("the record is for another app or action")
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("job record of %s: %w", id, err))
			continue
		}
		jobs = append(jobs, &appstore.Job{
			AppID: id, Action: record.Action, State: record.State, StartedAt: record.StartedAt,
			FinishedAt: record.FinishedAt, Output: record.Output, Error: record.Error,
		})
	}
	return jobs, errors.Join(problems...)
}

// Recover loads each app's latest job and, in the background, removes the
// containers of installs the agent was interrupted in and finishes
// interrupted uninstalls; meanwhile those apps show as installing or
// uninstalling. Call it once, before serving. Unreadable records are
// reported; the others are recovered.
func (s *Store) Recover() error {
	jobs, err := s.readJobs()
	s.mu.Lock()
	defer s.mu.Unlock()
	var interrupted []*appstore.Job
	for _, job := range jobs {
		s.jobs[job.AppID] = job
		if job.State == appstore.JobRunning {
			interrupted = append(interrupted, job)
		}
	}
	if len(interrupted) == 0 {
		return err
	}
	// Only one job runs at a time, so normally there is one; the agent takes
	// no other job until all are done.
	s.active = interrupted[0].AppID
	s.wait.Go(func() {
		for _, job := range interrupted {
			s.mu.Lock()
			s.active = job.AppID
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), uninstallTimeout)
			result := s.resume(ctx, job, &jobWriter{store: s, job: job})
			cancel()
			s.mu.Lock()
			s.end(job, result)
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.active = ""
		s.mu.Unlock()
	})
	return err
}

// resume takes an interrupted job down: the containers of an install were
// never checked against its plan, so they go whether or not it got that far.
func (s *Store) resume(ctx context.Context, job *appstore.Job, output io.Writer) error {
	down := takeDown(job.AppID)
	if job.Action == appstore.JobUninstall {
		_, _ = fmt.Fprintln(output, "容器代理重启时卸载没有完成，继续卸载")
		return s.runner.Run(ctx, down, output)
	}
	_, _ = fmt.Fprintln(output, "容器代理重启时安装没有完成，正在删除它创建的容器")
	if err := s.runner.Run(ctx, down, output); err != nil {
		return fmt.Errorf("%w; the install could not be rolled back: %v", errInterrupted, err)
	}
	return fmt.Errorf("%w，安装已回滚，请重新安装", errInterrupted)
}

// end records a job's result; s.mu is held.
func (s *Store) end(job *appstore.Job, err error) {
	job.FinishedAt = s.now().UTC()
	job.State = appstore.JobSucceeded
	if err != nil {
		job.State = appstore.JobFailed
		job.Error = err.Error()
	}
	if saveErr := s.saveJob(job); saveErr != nil {
		job.Output = append(job.Output, "无法保存任务记录："+saveErr.Error())
	}
}

// Stop cancels a running install, which removes what it created, lets an
// uninstall finish, and waits for both; the agent takes no job after it.
func (s *Store) Stop() {
	s.mu.Lock()
	s.stopped = true
	if job := s.jobs[s.active]; s.cancel != nil && job != nil && job.Action == appstore.JobInstall {
		s.cancel(errStopping)
	}
	s.mu.Unlock()
	s.wait.Wait()
}
