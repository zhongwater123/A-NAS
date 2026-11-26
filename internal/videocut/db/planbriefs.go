package db

import "context"

// The reads and writes of plan_briefs: what the planning model is told
// for a step's input key (docs/architecture/montage-planning-session.md).

// PlanBrief is the brief kept for jobID's step and input key, encoded;
// sql.ErrNoRows when none is.
func (d *DB) PlanBrief(ctx context.Context, jobID, step, key string) (string, error) {
	var kept string
	err := d.db.QueryRowContext(ctx, "SELECT brief_json FROM plan_briefs WHERE job_id = ? AND step = ? AND input_key = ?", jobID, step, key).Scan(&kept)
	return kept, err
}

// KeepPlanBrief keeps brief for jobID's step and input key; nothing when
// one is kept already.
func (d *DB) KeepPlanBrief(ctx context.Context, jobID, step, key, brief string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO plan_briefs(job_id, step, input_key, brief_json) VALUES(?, ?, ?, ?)
		ON CONFLICT(job_id, step, input_key) DO NOTHING`, jobID, step, key, brief)
	return err
}
