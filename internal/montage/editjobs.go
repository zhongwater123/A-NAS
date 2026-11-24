package montage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Saving a draft as a new version (#596, ADR 0039 decisions 4–6): an edit
// job fills the draft's slots again with their beats pinned, renders and
// saves the film as a version of its own. The version it came from and its
// film stay as they were.

// SaveDraft starts an edit job that renders owner's draft as it stands. The
// same requestKey again returns the same job; a project with a job in
// progress answers ErrJobActive.
func (s *Store) SaveDraft(ctx context.Context, owner, draftID, requestKey string) (Job, bool, error) {
	if !printable(requestKey, MaxRequestKey) {
		return Job{}, false, ErrInvalid
	}
	if !s.runsEdits() {
		return Job{}, false, ErrJobsUnavailable
	}
	// A request key used before answers its job, whatever became of the draft.
	jobID, err := keyedJob(ctx, s.db, owner, requestKey, KindEdit, "draft_id", draftID)
	if err != nil {
		return Job{}, false, err
	}
	if jobID != "" {
		job, err := s.Job(ctx, owner, jobID)
		return job, false, err
	}
	created := false
	draft, err := s.readDraft(ctx, owner, draftID, nil)
	if err != nil {
		return Job{}, false, err
	}
	src, state := draft.source, draft.state
	// A version whose videos were analysed again with another model since the
	// draft was opened cannot be saved: say so now rather than fail in the fill.
	if err := s.checkAnalysis(ctx, src); err != nil {
		return Job{}, false, err
	}
	edit := EditSettings{Job: src.planJob, Plan: src.planNo}
	used := map[string]string{}
	for _, shot := range state.Shots {
		first := state.Slots[shot.First]
		edit.Pins = append(edit.Pins, FillPin{Part: first.Part, Picture: first.Picture,
			Slot: [2]float64{first.Start, state.Slots[shot.Last].End}, Beat: shot.Beat})
		card, _, _ := src.beat(shot.Beat)
		used[card.Video] = src.videos[card.Video].ObjectID
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// A retry with the same key may have made the job since the look above.
		var err error
		if jobID, err = keyedJob(ctx, tx, owner, requestKey, KindEdit, "draft_id", draftID); err != nil || jobID != "" {
			return err
		}
		row, err := s.project(ctx, tx, owner, src.projectID)
		if err != nil {
			return err
		}
		if err := projectIdle(ctx, tx, src.projectID); err != nil {
			return err
		}
		var revision int
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM drafts WHERE id = ?", draftID).Scan(&revision); err != nil {
			return err
		}
		if revision != draft.Revision {
			return ErrDraftConflict
		}
		// The song, its excerpt and the plan are the version's own: the
		// project may have another song since.
		var settingsJSON string
		if err := tx.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", src.jobID).Scan(&settingsJSON); err != nil {
			return err
		}
		var settings JobSettings
		if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			return fmt.Errorf("the version's job holds unusable settings: %w", err)
		}
		// An edit of a recut version is not a recut: its sentence and base
		// stay with the version they planned.
		settings.ProjectName, settings.Edit, settings.Recut = row.name, &edit, nil
		if jobID, err = s.queueJob(ctx, tx, newJob{owner: owner, projectID: src.projectID, requestKey: requestKey, kind: KindEdit, settings: settings, plans: 1,
			sourceVersion: sql.NullString{String: src.versionID, Valid: true}, draft: sql.NullString{String: draftID, Valid: true},
			draftRevision: sql.NullInt64{Int64: int64(draft.Revision), Valid: true}}); err != nil {
			return err
		}
		created = true
		// The job looks after the videos its shots lead with; a piece the
		// fill takes from another is passed over when that video is gone.
		for asset, object := range used {
			if _, err := tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) VALUES(?, ?, ?)", jobID, asset, object); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Job{}, false, err
	}
	s.wakeJobs()
	job, err := s.Job(ctx, owner, jobID)
	return job, created, err
}

// editSteps are an edit job's steps: its source (the version's plan and the
// draft's slots), the fill with the slots pinned, the render and the save.
// They are the first 方案's steps, so that the fill, render and save are
// those a generate job runs.
func (s *Store) editSteps() []step {
	return []step{
		{name: StepSource, plan: 1, key: s.editSourceKey, do: func(_ context.Context, run *jobRun) (any, error) {
			return map[string]any{"cards": len(run.catalog.Catalog), "pins": len(run.pins)}, nil
		}, load: func(*jobRun, []byte) error { return nil }},
		{name: "fill", plan: 1, key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys[StepSource], fillVersion), nil
		}, do: s.fill, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.timeline) }},
		{name: "render", plan: 1, key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(digestJSON(run.timeline.Clips), renderVersion, run.excerpt.Key), nil
		}, do: s.renderFilm, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.render) }, reusable: s.filmKept},
		{name: "save", plan: 1, key: func(ctx context.Context, run *jobRun) (string, error) {
			// The draft's revision and the render: retrying saves it once.
			var draftID string
			var revision int
			if err := s.db.QueryRowContext(ctx, "SELECT draft_id, draft_revision FROM jobs WHERE id = ?", run.id).Scan(&draftID, &revision); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s|%d|%s", draftID, revision, run.keys["render"]), nil
		}, do: s.save, load: func(*jobRun, []byte) error { return nil }},
	}
}

// StepSource is an edit job's first step.
const StepSource = "source"

// editSourceKey reads what an edit renders from: the plan's job, its
// analysis, catalog, excerpt and plan, and the draft's pins. A card of a
// video gone since the version was made leaves the catalog, so that the
// fill takes no piece from it; the videos the shots lead with were checked
// as the job's own.
func (s *Store) editSourceKey(ctx context.Context, run *jobRun) (string, error) {
	edit := run.settings.Edit
	if edit == nil || len(edit.Pins) == 0 {
		return "", failed("the edit job has no draft")
	}
	results := map[string][]byte{}
	for _, name := range []string{"analysis", "catalog", "song", planStep("pick", edit.Plan), planStep("shots", edit.Plan)} {
		result, kept, err := s.doneResult(ctx, edit.Job, name)
		if err != nil {
			return "", err
		}
		if !kept {
			return "", failed("the version's plan is gone: no %s", name)
		}
		results[name] = result
	}
	if err := errors.Join(json.Unmarshal(results["analysis"], &run.analysis), json.Unmarshal(results["catalog"], &run.catalog),
		json.Unmarshal(results["song"], &run.excerpt)); err != nil {
		return "", failed("the version's plan is unusable: %v", err)
	}
	run.picked, run.planned = results[planStep("pick", edit.Plan)], results[planStep("shots", edit.Plan)]
	run.pins = edit.Pins
	ids := make([]string, len(run.analysis.Videos))
	for i, video := range run.analysis.Videos {
		ids[i] = video.AssetID
	}
	looked, err := s.lookupAssets(ctx, run.grantID, ids)
	if err != nil {
		return "", jobGrantError(err, ids)
	}
	there := map[string]bool{}
	for _, asset := range looked {
		if video, ok := run.assetOf(asset.AssetID); ok && asset.Status == photos.GrantAvailable && asset.ObjectID == video.ObjectID {
			there[asset.AssetID] = true
		}
	}
	for _, asset := range run.assets {
		there[asset.assetID] = true // checkAssets passed them
	}
	run.catalog.Catalog = slices.DeleteFunc(slices.Clone(run.catalog.Catalog), func(id string) bool { return !there[run.catalog.Cards[id].Video] })
	return digest(edit.Job, fmt.Sprint(edit.Plan), digestJSON(edit.Pins), digestJSON(run.catalog.Catalog), run.excerpt.Key, digestJSON(run.analysis)), nil
}

// --- the draft's song ---

// OpenDraftSongAudio opens the song of the version owner's draft edits, as
// its job had it: the project may have another song since.
func (s *Store) OpenDraftSongAudio(ctx context.Context, owner, draftID string) (SongAudio, error) {
	var settingsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT j.settings_json FROM drafts d JOIN versions v ON v.id = d.base_version_id JOIN jobs j ON j.id = v.job_id
		WHERE d.id = ? AND d.owner_user_id = ?`, draftID, owner).Scan(&settingsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return SongAudio{}, ErrNotFound
	}
	if err != nil {
		return SongAudio{}, err
	}
	var settings JobSettings
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil || settings.SongID == "" {
		return SongAudio{}, ErrNoSong
	}
	if _, err := s.ownSong(ctx, owner, settings.SongID); err != nil {
		return SongAudio{}, ErrNoSong
	}
	return s.openSong(ctx, settings.SongID)
}

// openSong opens a song's copy to be heard.
func (s *Store) openSong(ctx context.Context, songID string) (SongAudio, error) {
	row, err := s.song(ctx, s.db, songID)
	if err != nil {
		return SongAudio{}, err
	}
	file, err := os.Open(s.songPath(row.sha))
	if err != nil {
		return SongAudio{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return SongAudio{}, errors.Join(err, file.Close())
	}
	return SongAudio{File: file, ContentType: row.contentType, ModifiedAt: info.ModTime(),
		Song: SongDetail{ID: row.id, Title: row.title, Duration: row.duration, Transcode: row.contentType == ""}}, nil
}
