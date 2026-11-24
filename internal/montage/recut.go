package montage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
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
	var prior, priorVersion sql.NullString
	var priorKind string
	err := s.db.QueryRowContext(ctx, "SELECT id, kind, source_version_id FROM jobs WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).
		Scan(&prior, &priorKind, &priorVersion)
	switch {
	case err == nil && (priorKind != KindRecut || priorVersion.String != versionID):
		return Job{}, false, ErrConflict
	case err == nil:
		job, err := s.Job(ctx, owner, prior.String)
		return job, false, err
	case !errors.Is(err, sql.ErrNoRows):
		return Job{}, false, err
	}
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return Job{}, false, err
	}
	base, err := recutBase(src)
	if err != nil {
		return Job{}, false, err
	}
	var jobID string
	created := false
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// A retry with the same key may have made the job since the look above.
		var again, againVersion sql.NullString
		var againKind string
		switch err := tx.QueryRowContext(ctx, "SELECT id, kind, source_version_id FROM jobs WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).
			Scan(&again, &againKind, &againVersion); {
		case err == nil && (againKind != KindRecut || againVersion.String != versionID):
			return ErrConflict
		case err == nil:
			jobID = again.String
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		row, err := s.project(ctx, tx, owner, src.projectID)
		if err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE project_id = ? AND "+activeStateQuery+")", src.projectID).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrJobActive
		}
		var settingsJSON string
		if err := tx.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", src.planJob).Scan(&settingsJSON); err != nil {
			return err
		}
		var settings JobSettings
		if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			return fmt.Errorf("the version's job holds unusable settings: %w", err)
		}
		settings.ProjectName, settings.Edit = row.name, nil
		settings.Recut = &RecutSettings{Change: change, Base: base}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		jobID, created = newID("job"), true
		now := sqlitedb.FormatTime(s.now())
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, plans, state, not_before,
				created_at, updated_at, kind, source_version_id)
			VALUES(?, ?, ?, ?, ?, 1, 'queued', ?, ?, ?, 'recut', ?)`,
			jobID, owner, src.projectID, requestKey, string(encoded), now, now, now, versionID); err != nil {
			return err
		}
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
