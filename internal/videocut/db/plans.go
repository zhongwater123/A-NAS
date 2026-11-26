package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of plans: each 方案's plan for a step, taken for a
// catalog, until the catalog or the step's input changes and voids it
// (docs/architecture/videocut-center.md).

// AcceptedPlan is the latest plan, not voided, taken for jobID's plan-th
// 方案 and step with catalogKey, encoded; ErrNotFound when there is none.
func (d *DB) AcceptedPlan(ctx context.Context, jobID string, plan int, step, catalogKey string) (string, error) {
	var answer string
	err := queryRow(ctx, d.db, `SELECT answer_json FROM plans WHERE job_id = ? AND plan = ? AND step = ? AND catalog_key = ? AND voided_at IS NULL
		ORDER BY accepted_at DESC LIMIT 1`, jobID, plan, step, catalogKey).Scan(&answer)
	return answer, err
}

// AcceptPlan keeps answer as jobID's plan-th 方案's plan for step with
// catalogKey, taken at at.
func (d *DB) AcceptPlan(ctx context.Context, jobID string, plan int, step, catalogKey, answer string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO plans(job_id, plan, step, catalog_key, answer_json, accepted_at) VALUES(?, ?, ?, ?, ?, ?)",
		jobID, plan, step, catalogKey, answer, sqlitedb.FormatTime(at))
	return err
}

// VoidPlan voids, at at, the plans of jobID's plan-th 方案 for step not
// voided yet.
func (tx *Tx) VoidPlan(ctx context.Context, jobID string, plan int, step string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE plans SET voided_at = ? WHERE job_id = ? AND plan = ? AND step = ? AND voided_at IS NULL",
		sqlitedb.FormatTime(at), jobID, plan, step)
	return err
}

// VoidPlans voids, at at, every plan of jobID not voided yet.
func (tx *Tx) VoidPlans(ctx context.Context, jobID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE plans SET voided_at = ? WHERE job_id = ? AND voided_at IS NULL", sqlitedb.FormatTime(at), jobID)
	return err
}
