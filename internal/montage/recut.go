package montage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Recutting a version from a sentence (#596, ADR 0039 decision 7): the
// member's VNAS plans the version again, one 方案, starting from the plan
// it was cut by and changing only what the sentence asks. It is the one
// part of editing that plans, so it takes a round of the planning grant.

// RecutVersion starts a recut job of owner's version. The videos, song,
// excerpt and order are the version's job's; a version edited by hand is
// planned again from the plan it came from, without the shots changed by
// hand. The same requestKey again returns the same job; a project with a
// job in progress answers ErrJobActive.
func (s *Store) RecutVersion(ctx context.Context, owner, versionID, requestKey, change string) (Job, bool, error) {
	if !printable(requestKey, MaxRequestKey) || strings.TrimSpace(change) == "" || !validText(change, MaxAskRunes) {
		return Job{}, false, ErrInvalid
	}
	if !s.runsJobs() {
		return Job{}, false, ErrJobsUnavailable
	}
	jobID, err := keyedJob(ctx, s.db, owner, requestKey, KindRecut, "source_version_id", versionID)
	if err != nil {
		return Job{}, false, err
	}
	if jobID != "" {
		job, err := s.Job(ctx, owner, jobID)
		return job, false, err
	}
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return Job{}, false, err
	}
	base, err := recutBase(src)
	if err != nil {
		return Job{}, false, err
	}
	created := false
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// A retry with the same key may have made the job since the look above.
		var err error
		if jobID, err = keyedJob(ctx, tx, owner, requestKey, KindRecut, "source_version_id", versionID); err != nil || jobID != "" {
			return err
		}
		row, err := s.project(ctx, tx, owner, src.projectID)
		if err != nil {
			return err
		}
		if err := projectIdle(ctx, tx, src.projectID); err != nil {
			return err
		}
		settings, err := jobSettings(ctx, tx, src.planJob)
		if err != nil {
			return err
		}
		settings.ProjectName, settings.Edit = row.name, nil
		settings.Recut = &RecutSettings{Change: change, Base: base}
		if jobID, err = s.queueJob(ctx, tx, newJob{owner: owner, projectID: src.projectID, requestKey: requestKey, kind: KindRecut, settings: settings, plans: 1,
			sourceVersion: sql.NullString{String: versionID, Valid: true}}); err != nil {
			return err
		}
		created = true
		// The videos the version was planned from, as they were then.
		_, err = tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) SELECT ?, asset_id, object_id FROM job_assets WHERE job_id = ?",
			jobID, src.planJob)
		return err
	})
	if err != nil {
		return Job{}, false, err
	}
	s.wakeJobs()
	job, err := s.Job(ctx, owner, jobID)
	return job, created, err
}

// recutBase is the plan a recut starts from: the version's outline and, for
// each part, its pictures as the second step answered them. Both are passed
// on whole, with each part's and picture's link, which the template asks to
// keep where the sentence does not touch; only what the earlier request did
// not find (unmet) is left out, which the new round says for itself.
func recutBase(src *editSource) (json.RawMessage, error) {
	var outline map[string]json.RawMessage
	var shots struct {
		Answer struct {
			Parts json.RawMessage `json:"parts"`
		} `json:"answer"`
	}
	if err := errors.Join(json.Unmarshal(src.pickedRaw, &outline), json.Unmarshal(src.plannedRaw, &shots)); err != nil {
		return nil, ErrNotEditable
	}
	delete(outline, "unmet")
	return json.Marshal(map[string]any{"outline": outline, "shots": shots.Answer.Parts})
}
