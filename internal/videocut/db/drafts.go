package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of drafts and their revisions (#596; docs/
// architecture/videocut-center.md, 出片后的二次编辑).

// DraftID is the id of owner's draft of versionID; ErrNotFound when
// there is none.
func (d *DB) DraftID(ctx context.Context, owner, versionID string) (string, error) {
	var id string
	err := queryRow(ctx, d.db, "SELECT id FROM drafts WHERE base_version_id = ? AND owner_user_id = ?", versionID, owner).Scan(&id)
	return id, err
}

// CreateDraft makes owner's draft id of versionID at revision 0, holding
// shots, with that first revision kept; nothing when the version has a
// draft already (another request made it first).
func (tx *Tx) CreateDraft(ctx context.Context, id, owner, projectID, versionID, shots string, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	result, err := tx.tx.ExecContext(ctx, `INSERT INTO drafts(id, owner_user_id, project_id, base_version_id, revision, shots_json, updated_at)
				VALUES(?, ?, ?, ?, 0, ?, ?) ON CONFLICT (base_version_id) DO NOTHING`, id, owner, projectID, versionID, shots, now)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return err // another request made it first
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO draft_revisions(draft_id, revision, op, shots_json, created_at) VALUES(?, 0, 'create', ?, ?)`,
		id, shots, now)
	return err
}

// DraftRow is a draft as kept: the version it edits, its revision, its
// shots and when it last changed (zero when that cannot be read).
type DraftRow struct {
	VersionID string
	Revision  int
	Shots     string
	Updated   time.Time
}

// Draft reads owner's draft; ErrNotFound when owner has none by that id.
func (d *DB) Draft(ctx context.Context, owner, draftID string) (DraftRow, error) {
	var row DraftRow
	var updated string
	err := queryRow(ctx, d.db, "SELECT base_version_id, revision, shots_json, updated_at FROM drafts WHERE id = ? AND owner_user_id = ?",
		draftID, owner).Scan(&row.VersionID, &row.Revision, &row.Shots, &updated)
	row.Updated = timeOrZero(updated)
	return row, err
}

// DraftRevision is the revision draftID stands at.
func (tx *Tx) DraftRevision(ctx context.Context, draftID string) (int, error) {
	var revision int
	err := queryRow(ctx, tx.tx, "SELECT revision FROM drafts WHERE id = ?", draftID).Scan(&revision)
	return revision, err
}

// DraftJob is the job that made the version owner's draft edits;
// ErrNotFound when owner has no such draft.
func (d *DB) DraftJob(ctx context.Context, owner, draftID string) (string, error) {
	var jobID string
	err := queryRow(ctx, d.db, `SELECT v.job_id FROM drafts d JOIN versions v ON v.id = d.base_version_id
		WHERE d.id = ? AND d.owner_user_id = ?`, draftID, owner).Scan(&jobID)
	return jobID, err
}

// AdvanceDraft takes owner's draft from revision base to base+1 holding
// shots; false when the draft is not at base any more.
func (tx *Tx) AdvanceDraft(ctx context.Context, owner, draftID string, base int, shots string, at time.Time) (bool, error) {
	result, err := tx.tx.ExecContext(ctx, "UPDATE drafts SET revision = revision + 1, shots_json = ?, updated_at = ? WHERE id = ? AND owner_user_id = ? AND revision = ?",
		shots, sqlitedb.FormatTime(at), draftID, owner, base)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// AddRevision keeps revision of draftID: the step op that made it, the
// seed it drew with, the revision undoing it goes back to, and its shots.
// Seed and back are nil for none.
func (tx *Tx) AddRevision(ctx context.Context, draftID string, revision int, op string, seed *int64, back *int, shots string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO draft_revisions(draft_id, revision, op, seed, back, shots_json, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		draftID, revision, op, seed, back, shots, sqlitedb.FormatTime(at))
	return err
}

// Revision is a kept revision as an undo reads it: its number, its shots
// and the revision undoing it goes back to (nil when none).
type Revision struct {
	Revision int
	Shots    string
	Back     *int
}

// UndoTarget is the revision undoing revision of draftID goes back to;
// ErrNotFound when it has none or it is no longer kept.
func (d *DB) UndoTarget(ctx context.Context, draftID string, revision int) (Revision, error) {
	var target Revision
	var back sql.NullInt64
	err := queryRow(ctx, d.db, `SELECT t.revision, t.shots_json, t.back FROM draft_revisions r
		JOIN draft_revisions t ON t.draft_id = r.draft_id AND t.revision = r.back
		WHERE r.draft_id = ? AND r.revision = ?`, draftID, revision).Scan(&target.Revision, &target.Shots, &back)
	target.Back = intOrNil(back)
	return target, err
}

// RevisionBacks are the revisions kept of draftID, each with the revision
// undoing it goes back to (nil when none).
func (tx *Tx) RevisionBacks(ctx context.Context, draftID string) (map[int]*int, error) {
	rows, err := tx.tx.QueryContext(ctx, "SELECT revision, back FROM draft_revisions WHERE draft_id = ?", draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	backs := map[int]*int{}
	for rows.Next() {
		var revision int
		var back sql.NullInt64
		if err := rows.Scan(&revision, &back); err != nil {
			return nil, err
		}
		backs[revision] = intOrNil(back)
	}
	return backs, rows.Err()
}

// intOrNil is a revision as kept, nil for NULL.
func intOrNil(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	return new(int(value.Int64))
}

// DeleteRevision stops keeping revision of draftID.
func (tx *Tx) DeleteRevision(ctx context.Context, draftID string, revision int) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM draft_revisions WHERE draft_id = ? AND revision = ?", draftID, revision)
	return err
}
