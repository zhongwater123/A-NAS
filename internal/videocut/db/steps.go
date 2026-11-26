package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of steps: each job's steps, the input key each ran
// with and what it kept (docs/architecture/videocut-center.md, 每一步可重跑).

// DoneCatalog is what jobID's catalog step kept when done; ErrNotFound
// when it is not done.
func (d *DB) DoneCatalog(ctx context.Context, jobID string) (string, error) {
	var catalogJSON string
	err := queryRow(ctx, d.db, "SELECT result_json FROM steps WHERE job_id = ? AND name = 'catalog' AND state = 'done'",
		jobID).Scan(&catalogJSON)
	return catalogJSON, err
}

// DoneExcerpt is what jobID's song step kept when done, the excerpt;
// ErrNotFound when it is not done.
func (d *DB) DoneExcerpt(ctx context.Context, jobID string) (string, error) {
	var songJSON string
	err := queryRow(ctx, d.db, "SELECT result_json FROM steps WHERE job_id = ? AND name = 'song' AND state = 'done'",
		jobID).Scan(&songJSON)
	return songJSON, err
}

// DoneExcerpt is DB.DoneExcerpt inside the transaction, not valid when the
// step kept nothing.
func (tx *Tx) DoneExcerpt(ctx context.Context, jobID string) (sql.NullString, error) {
	var excerpt sql.NullString
	err := queryRow(ctx, tx.tx, "SELECT result_json FROM steps WHERE job_id = ? AND name = 'song' AND state = 'done'",
		jobID).Scan(&excerpt)
	return excerpt, err
}

// DoneResults are what the named steps of jobID kept when they were done,
// by name; a step that is not done, or that kept nothing, is not among
// them.
func (d *DB) DoneResults(ctx context.Context, jobID string, names []string) (map[string][]byte, error) {
	args := []any{jobID}
	for _, name := range names {
		args = append(args, name)
	}
	rows, err := d.db.QueryContext(ctx, "SELECT name, result_json FROM steps WHERE job_id = ? AND state = 'done' AND result_json IS NOT NULL AND name IN ("+
		sqlitedb.Placeholders(len(names))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := map[string][]byte{}
	for rows.Next() {
		var name, result string
		if err := rows.Scan(&name, &result); err != nil {
			return nil, err
		}
		results[name] = []byte(result)
	}
	return results, rows.Err()
}

// StepRow is one of a job's steps as a job's page shows it: its state and
// when it started and finished (not valid when it has not).
type StepRow struct {
	Name, State       string
	Started, Finished sql.NullString
}

// JobSteps are jobID's steps.
func (d *DB) JobSteps(ctx context.Context, jobID string) ([]StepRow, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT name, state, started_at, finished_at FROM steps WHERE job_id = ?", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []StepRow
	for rows.Next() {
		var st StepRow
		if err := rows.Scan(&st.Name, &st.State, &st.Started, &st.Finished); err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	return steps, rows.Err()
}

// DoneSaveKey is the input key jobID's save step was done with;
// ErrNotFound when it is not done.
func (d *DB) DoneSaveKey(ctx context.Context, jobID string) (string, error) {
	var key string
	err := queryRow(ctx, d.db, "SELECT input_key FROM steps WHERE job_id = ? AND name = 'save' AND state = 'done'", jobID).Scan(&key)
	return key, err
}

// StepResult is what jobID's step name kept, not valid when nothing;
// ErrNotFound when the job has no such step.
func (d *DB) StepResult(ctx context.Context, jobID, name string) (sql.NullString, error) {
	var saved sql.NullString
	err := queryRow(ctx, d.db, "SELECT result_json FROM steps WHERE job_id = ? AND name = ?", jobID, name).Scan(&saved)
	return saved, err
}

// RekeyStep gives jobID's step name the input key key, when it has
// another.
func (d *DB) RekeyStep(ctx context.Context, jobID, name, key string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE steps SET input_key = ? WHERE job_id = ? AND name = ? AND input_key <> ?",
		key, jobID, name, key)
	return err
}

// StoredStep is jobID's step name as kept: its input key, its state and
// what it kept (not valid when nothing); ErrNotFound when the job has no
// such step.
func (d *DB) StoredStep(ctx context.Context, jobID, name string) (key, state string, result sql.NullString, err error) {
	err = queryRow(ctx, d.db, "SELECT input_key, state, result_json FROM steps WHERE job_id = ? AND name = ?", jobID, name).
		Scan(&key, &state, &result)
	return key, state, result, err
}

// StepInputKey is the input key jobID's step name ran with; ErrNotFound
// when the job has no such step.
func (tx *Tx) StepInputKey(ctx context.Context, jobID, name string) (string, error) {
	var stored string
	err := queryRow(ctx, tx.tx, "SELECT input_key FROM steps WHERE job_id = ? AND name = ?", jobID, name).Scan(&stored)
	return stored, err
}

// AddRunningStep adds jobID's step name, running with key since at.
func (tx *Tx) AddRunningStep(ctx context.Context, jobID, name, key string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "INSERT INTO steps(job_id, name, input_key, state, started_at) VALUES(?, ?, ?, 'running', ?)",
		jobID, name, key, sqlitedb.FormatTime(at))
	return err
}

// RestartStep sets jobID's step name running again with its input key,
// keeping what it kept.
func (tx *Tx) RestartStep(ctx context.Context, jobID, name string) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE steps SET state = 'running', finished_at = NULL WHERE job_id = ? AND name = ?", jobID, name)
	return err
}

// RerunStep sets jobID's step name running anew with key since at, what it
// kept forgotten.
func (tx *Tx) RerunStep(ctx context.Context, jobID, name, key string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE steps SET input_key = ?, state = 'running', result_json = NULL, started_at = ?, finished_at = NULL
				WHERE job_id = ? AND name = ?`, key, sqlitedb.FormatTime(at), jobID, name)
	return err
}

// DeleteStep forgets jobID's step name.
func (tx *Tx) DeleteStep(ctx context.Context, jobID, name string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM steps WHERE job_id = ? AND name = ?", jobID, name)
	return err
}

// FinishStep records jobID's step name done at at, keeping result.
func (tx *Tx) FinishStep(ctx context.Context, jobID, name, result string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE steps SET state = 'done', result_json = ?, finished_at = ? WHERE job_id = ? AND name = ?",
		result, sqlitedb.FormatTime(at), jobID, name)
	return err
}

// SaveStepProgress keeps progress as what jobID's step name kept, while it
// runs.
func (d *DB) SaveStepProgress(ctx context.Context, jobID, name, progress string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE steps SET result_json = ? WHERE job_id = ? AND name = ? AND state = 'running'", progress, jobID, name)
	return err
}
