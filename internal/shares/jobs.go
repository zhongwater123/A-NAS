package shares

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

var errCommitRecord = errors.New("target published but task state could not be recorded")

func selectedEntries(r *record, ids []string) ([]Entry, error) {
	if len(ids) == 0 || len(ids) > MaxEntries {
		return nil, ErrInvalid
	}
	selected := map[string]bool{}
	folders := []string{}
	for _, id := range ids {
		found := false
		for _, e := range r.Share.Entries {
			if e.ID == id {
				found = true
				selected[id] = true
				if e.IsDir {
					folders = append(folders, e.Path+"/")
				}
				break
			}
		}
		if !found {
			return nil, ErrInvalid
		}
	}
	out := []Entry{}
	for _, e := range r.Share.Entries {
		include := selected[e.ID]
		for _, folder := range folders {
			include = include || strings.HasPrefix(e.Path, folder)
		}
		if include {
			out = append(out, e)
		}
	}
	// A nested selection authorizes only its required ancestors, not siblings.
	for _, e := range r.Share.Entries {
		if !e.IsDir || selected[e.ID] {
			continue
		}
		found := false
		for _, included := range out {
			if included.ID == e.ID {
				found = true
				break
			}
		}
		if found {
			continue
		}
		for _, included := range out {
			if strings.HasPrefix(included.Path, e.Path+"/") {
				out = append(out, e)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (s *Store) Save(ctx context.Context, u User, shareID string, req SaveRequest) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.access(ctx, u, shareID)
	if err != nil {
		return Job{}, err
	}
	if recipient(r, u.ID) == nil {
		return Job{}, ErrNotFound
	}
	if !validKey(req.RequestID) || req.Target.RootID == "" || len(req.Target.RootID) > 4096 || len(req.Target.ParentID) > 4096 {
		return Job{}, ErrInvalid
	}
	req.Target.ShareID = ""
	req.Target.JobID = ""
	key := requestKey(u.ID, "save", req.RequestID)
	digest := hash(struct {
		ShareID string
		Request SaveRequest
	}{shareID, req})
	for _, existing := range s.records {
		if old, ok := existing.Requests[key]; ok {
			if old.Hash != digest {
				return Job{}, ErrConflict
			}
			return copyJob(existing.Jobs[old.ID].Job), nil
		}
	}
	if !s.isActive(r) {
		return Job{}, ErrInactive
	}
	if len(s.running) >= s.options.MaxTasks {
		return Job{}, ErrBusy
	}
	if err = s.volume(ctx); err != nil {
		return Job{}, err
	}
	entries, err := selectedEntries(r, req.EntryIDs)
	if err != nil {
		return Job{}, err
	}
	job := Job{ID: newID(), ShareID: shareID, UserID: u.ID, Status: "running", CreatedAt: s.now(), EntryIDs: []string{}, CompletedIDs: []string{}}
	for _, e := range entries {
		job.EntryIDs = append(job.EntryIDs, e.ID)
		job.BytesTotal += e.Size
	}
	target := req.Target
	target.ShareID = shareID
	target.JobID = job.ID
	r.Jobs[job.ID] = &storedJob{Job: job, Target: target}
	r.Requests[key] = requestRecord{Hash: digest, ID: job.ID}
	if err = s.persist(r); err != nil {
		delete(r.Jobs, job.ID)
		delete(r.Requests, key)
		return Job{}, err
	}
	taskCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.running[job.ID] = running{shareID: shareID, cancel: cancel, save: true, ctx: taskCtx, user: u}
	s.wg.Add(1)
	go s.save(taskCtx, u, shareID, job.ID, entries, target)
	return copyJob(job), nil
}
func copyJob(j Job) Job {
	j.EntryIDs = append([]string{}, j.EntryIDs...)
	j.CompletedIDs = append([]string{}, j.CompletedIDs...)
	return j
}
func (s *Store) jobAllowed(ctx context.Context, u User, r *record, j *storedJob) error {
	if err := s.authorize(ctx, u); err != nil {
		return err
	}
	if r.Share.Status == "revoked" || j.Job.Status != "running" {
		return ErrInactive
	}
	active, err := s.options.Active(ctx, r.Share.Sender.ID)
	if err != nil {
		return err
	}
	if !active {
		return ErrUnauthorized
	}
	return s.volume(ctx)
}
func (s *Store) save(ctx context.Context, u User, shareID, jobID string, entries []Entry, target Target) {
	defer s.wg.Done()
	allowed := map[string]Entry{}
	for _, e := range entries {
		allowed[e.ID] = e
	}
	open := func(id string) (io.ReadCloser, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		r := s.records[shareID]
		if err := s.jobAllowed(ctx, u, r, r.Jobs[jobID]); err != nil {
			return nil, err
		}
		e, ok := allowed[id]
		if !ok || e.IsDir {
			return nil, ErrNotFound
		}
		file, err := os.Open(filepath.Join(s.dir, shareID, id))
		if err != nil {
			return nil, err
		}
		return &jobReader{ReadCloser: file, ctx: ctx, store: s, user: u, shareID: shareID, jobID: jobID}, nil
	}
	commit := func(id string, publish func() error) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		r := s.records[shareID]
		j := r.Jobs[jobID]
		if _, ok := allowed[id]; !ok || publish == nil {
			return ErrInvalid
		}
		if err := s.jobAllowed(ctx, u, r, j); err != nil {
			return err
		}
		if slices.Contains(j.Job.CompletedIDs, id) {
			return ErrConflict
		}
		if err := publish(); err != nil {
			return err
		}
		j.Job.CompletedIDs = append(j.Job.CompletedIDs, id)
		p := recipient(r, u.ID)
		p.Saved = true
		if r.Saved[u.ID] == nil {
			r.Saved[u.ID] = map[string]bool{}
		}
		if !allowed[id].IsDir {
			r.Saved[u.ID][id] = true
		}
		p.SavedFiles = len(r.Saved[u.ID])
		if err := s.persist(r); err != nil {
			return errors.Join(errCommitRecord, err)
		}
		return nil
	}
	progress := func(bytes int64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j := s.records[shareID].Jobs[jobID]
		if bytes > 0 {
			j.Job.BytesDone += bytes
			if j.Job.BytesDone > j.Job.BytesTotal {
				j.Job.BytesDone = j.Job.BytesTotal
			}
		}
	}
	err := s.options.Backend.Save(ctx, u, target, entries, open, commit, progress)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[shareID]
	j := r.Jobs[jobID]
	if err == nil {
		err = s.jobAllowed(ctx, u, r, j)
	}
	if err == nil && len(j.Job.CompletedIDs) != len(entries) {
		err = errors.New("save did not publish all entries")
	}
	if err != nil {
		j.Job.Status = "failed"
		j.Job.Error = "Unable to save share: " + safeError(err)
		if r.Share.Status == "revoked" || errors.Is(err, context.Canceled) {
			j.Job.Status = "cancelled"
		}
		if s.interruptReason != "" && r.Share.Status != "revoked" {
			j.Job.Status = "interrupted"
			j.Job.Error = s.interruptReason
		}
		if errors.Is(err, ErrPublicationUnknown) {
			j.Job.Status = "interrupted"
			j.Job.Error = "Publication could not be confirmed; complete target files may exist and the saved count may be incomplete"
			recipient(r, u.ID).SaveUncertain = true
		}
	} else {
		j.Job.Status = "completed"
		j.Job.BytesDone = j.Job.BytesTotal
	}
	now := s.now()
	j.Job.FinishedAt = &now
	delete(s.running, jobID)
	if persistErr := s.persist(r); persistErr != nil {
		if j.Job.Status == "interrupted" {
			j.Job.Error += "; task state could not be recorded"
		} else {
			j.Job.Status = "failed"
			j.Job.Error = "Task state could not be recorded; completed files remain saved"
		}
		_ = s.persist(r)
	}
	_ = s.cleanupLocked(ctx, r)
}
func (s *Store) Job(ctx context.Context, u User, id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorize(ctx, u); err != nil {
		return Job{}, err
	}
	for _, r := range s.records {
		if j := r.Jobs[id]; j != nil && j.Job.UserID == u.ID {
			if _, err := s.access(ctx, u, r.Share.ID); err != nil {
				return Job{}, err
			}
			return copyJob(j.Job), nil
		}
	}
	return Job{}, ErrNotFound
}
func (s *Store) Jobs(ctx context.Context, u User, shareID string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.access(ctx, u, shareID)
	if err != nil {
		return nil, err
	}
	if recipient(r, u.ID) == nil {
		return nil, ErrNotFound
	}
	out := []Job{}
	for _, job := range r.Jobs {
		if job.Job.UserID == u.ID {
			out = append(out, copyJob(job.Job))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

type jobReader struct {
	io.ReadCloser
	ctx            context.Context
	store          *Store
	user           User
	shareID, jobID string
}

func (r *jobReader) Read(p []byte) (int, error) {
	r.store.mu.Lock()
	share := r.store.records[r.shareID]
	err := r.store.jobAllowed(r.ctx, r.user, share, share.Jobs[r.jobID])
	r.store.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}
