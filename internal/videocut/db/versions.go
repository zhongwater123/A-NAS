package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of versions: the films saved, each with what it was
// cut from (docs/architecture/videocut-center.md).

// PlanStepSQL is the name a 方案's step has (app's planStep) as SQL, for
// a step's plain name and the column that holds the 方案
// (TestPlanStepsAreNamedAlikeInSQL).
func PlanStepSQL(name, column string) string {
	return "CASE WHEN " + column + " <= 1 THEN '" + name + "' ELSE '" + name + "-' || " + column + " END"
}

// versionColumns are what Versions scans, of versions v.
var versionColumns = `v.id, v.number, v.job_id, v.plan, v.film_asset_id, v.created_at,
	v.timeline_json IS NOT NULL OR EXISTS (SELECT 1 FROM steps f WHERE f.job_id = v.job_id AND f.state = 'done'
		AND f.name = ` + PlanStepSQL("fill", "v.plan") + ` AND f.result_json IS NOT NULL),
	v.kind, IFNULL(v.source_version_id, ''), IFNULL((SELECT number FROM versions o WHERE o.id = v.source_version_id), 0), v.draft_revision,
	CASE WHEN v.kind = 'recut' THEN IFNULL((SELECT json_extract(CASE WHEN json_valid(settings_json) THEN settings_json END, '$.recut.change')
		FROM jobs WHERE id = v.job_id), '') ELSE '' END`

// VersionRow is a version as Versions reads it (versionColumns): whether
// it can be edited, the version it came from and its number, the draft's
// revision it saved, and a recut's sentence.
type VersionRow struct {
	ID              string
	Number          int
	JobID           string
	Plan            int
	FilmAssetID     string
	Created         string
	Editable        bool
	Kind            string
	SourceVersionID string
	SourceNumber    int
	DraftRevision   sql.NullInt64
	Change          string
}

// Versions lists the versions whose column is value, ordered by order.
func (d *DB) Versions(ctx context.Context, column, value, order string) ([]VersionRow, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+versionColumns+" FROM versions v WHERE v."+column+" = ? ORDER BY v."+order, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []VersionRow
	for rows.Next() {
		var v VersionRow
		if err := rows.Scan(&v.ID, &v.Number, &v.JobID, &v.Plan, &v.FilmAssetID, &v.Created, &v.Editable,
			&v.Kind, &v.SourceVersionID, &v.SourceNumber, &v.DraftRevision, &v.Change); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// VersionSourceRow is what editing reads of a version: its project, its
// number, the job and 方案 that made it, its timeline (not valid when it
// kept none) and its project's grant (not valid when there is none).
type VersionSourceRow struct {
	ProjectID string
	Number    int
	JobID     string
	Plan      int
	Timeline  sql.NullString
	GrantID   sql.NullString
}

// VersionSource reads owner's version versionID of a ready project;
// sql.ErrNoRows when owner has none.
func (d *DB) VersionSource(ctx context.Context, owner, versionID string) (VersionSourceRow, error) {
	var row VersionSourceRow
	err := d.db.QueryRowContext(ctx, `SELECT v.project_id, v.number, v.job_id, v.plan, v.timeline_json, p.grant_id
		FROM versions v JOIN projects p ON p.id = v.project_id WHERE v.id = ? AND p.owner_user_id = ? AND p.state = 'ready'`,
		versionID, owner).Scan(&row.ProjectID, &row.Number, &row.JobID, &row.Plan, &row.Timeline, &row.GrantID)
	return row, err
}

// KeepVersionTimeline gives versionID timeline, unless it keeps one.
func (d *DB) KeepVersionTimeline(ctx context.Context, versionID, timeline string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE versions SET timeline_json = ? WHERE id = ? AND timeline_json IS NULL", timeline, versionID)
	return err
}

// RedoneSince reports whether any of the named steps of jobID finished
// after the plan-th 方案's version was saved.
func (d *DB) RedoneSince(ctx context.Context, jobID string, plan int, names []string) (bool, error) {
	args := []any{jobID, plan}
	for _, name := range names {
		args = append(args, name)
	}
	var redone bool
	err := d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM versions v JOIN steps s ON s.job_id = v.job_id
		WHERE v.job_id = ? AND v.plan = ? AND s.finished_at > v.created_at AND s.name IN (`+sqlitedb.Placeholders(len(names))+`))`,
		args...).Scan(&redone)
	return redone, err
}

// VersionProject is the project of owner's version versionID, when the
// project is ready; sql.ErrNoRows otherwise.
func (tx *Tx) VersionProject(ctx context.Context, owner, versionID string) (string, error) {
	var projectID string
	err := tx.tx.QueryRowContext(ctx, `SELECT v.project_id FROM versions v JOIN projects p ON p.id = v.project_id
			WHERE v.id = ? AND p.owner_user_id = ? AND p.state = 'ready'`, versionID, owner).Scan(&projectID)
	return projectID, err
}

// SavedEditJob is the job of the first version an edit of draftID saved
// at revision; sql.ErrNoRows when none did.
func (d *DB) SavedEditJob(ctx context.Context, draftID string, revision int) (string, error) {
	var saved string
	err := d.db.QueryRowContext(ctx, `SELECT v.job_id FROM versions v JOIN jobs j ON j.id = v.job_id
		WHERE j.draft_id = ? AND v.kind = 'edit' AND v.draft_revision = ? ORDER BY v.number LIMIT 1`, draftID, revision).Scan(&saved)
	return saved, err
}

// AddVersion keeps version id of jobID's plan-th 方案, saved at at as
// filmAssetID under versionKey with its timeline (nil when none), its kind,
// source and revision taken from the job; nothing when the version key
// has its version already.
func (tx *Tx) AddVersion(ctx context.Context, id, projectID string, number int, jobID string, plan int, versionKey, filmAssetID string, at time.Time, timeline any) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO versions(id, project_id, number, job_id, plan, version_key, film_asset_id, created_at, timeline_json,
				kind, source_version_id, draft_revision)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, CASE kind WHEN 'generate' THEN 'plan' ELSE kind END, source_version_id, draft_revision
			FROM jobs WHERE id = ? ON CONFLICT (version_key) DO NOTHING`,
		id, projectID, number, jobID, plan, versionKey, filmAssetID, sqlitedb.FormatTime(at), timeline, jobID)
	return err
}

// SavedPlans are jobID's 方案 whose films are saved.
func (d *DB) SavedPlans(ctx context.Context, jobID string) (map[int]bool, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT plan FROM versions WHERE job_id = ?", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	saved := map[int]bool{}
	for rows.Next() {
		var plan int
		if err := rows.Scan(&plan); err != nil {
			return nil, err
		}
		saved[plan] = true
	}
	return saved, rows.Err()
}

// NextUnsavedPlan is the 方案 after jobID's saved ones: one more than
// it has versions.
func (d *DB) NextUnsavedPlan(ctx context.Context, jobID string) (int, error) {
	var plan int
	err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) + 1 FROM versions WHERE job_id = ?", jobID).Scan(&plan)
	return plan, err
}
