package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of plan_answers: the planning model's answers, the
// first good one for a step's input key (docs/architecture/
// montage-planning-session.md).

// KeepPlanAnswer keeps answer, handed back in roundID at at, for jobID's
// step and input key; false when one is kept for them already, which
// stays.
func (tx *Tx) KeepPlanAnswer(ctx context.Context, jobID, step, key, roundID, answer string, at time.Time) (bool, error) {
	inserted, err := tx.tx.ExecContext(ctx, `INSERT INTO plan_answers(job_id, step, input_key, round_id, answer_json, accepted_at) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(job_id, step, input_key) DO NOTHING`, jobID, step, key, roundID, answer, sqlitedb.FormatTime(at))
	if err != nil {
		return false, err
	}
	n, _ := inserted.RowsAffected()
	return n > 0, nil
}

// PlanAnswer is the answer kept for jobID's step and input key;
// sql.ErrNoRows when there is none.
func (d *DB) PlanAnswer(ctx context.Context, jobID, step, key string) (string, error) {
	var answer string
	err := d.db.QueryRowContext(ctx, "SELECT answer_json FROM plan_answers WHERE job_id = ? AND step = ? AND input_key = ?",
		jobID, step, key).Scan(&answer)
	return answer, err
}
