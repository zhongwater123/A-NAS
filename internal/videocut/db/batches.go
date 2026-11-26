package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of batches: the requests that bring videos to a
// project (docs/architecture/videocut-center.md).

// BatchRow is a request that brings videos to a project: kind create makes
// the project, kind add adds to it.
type BatchRow struct {
	ID, ProjectID, Kind, State string
	Assets                     []string
}

// Batch is owner's batch for requestKey; ErrNotFound when there is none.
func (tx *Tx) Batch(ctx context.Context, owner, requestKey string) (BatchRow, error) {
	var row BatchRow
	var assets string
	err := queryRow(ctx, tx.tx, "SELECT id, project_id, kind, asset_ids, state FROM batches WHERE owner_user_id = ? AND request_key = ?",
		owner, requestKey).Scan(&row.ID, &row.ProjectID, &row.Kind, &assets, &row.State)
	if err != nil {
		return row, err
	}
	err = json.Unmarshal([]byte(assets), &row.Assets)
	return row, err
}

// AddBatch keeps owner's batch row for requestKey as registering, made at
// at.
func (tx *Tx) AddBatch(ctx context.Context, owner, requestKey string, row BatchRow, at time.Time) error {
	encoded, err := json.Marshal(row.Assets)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO batches(id, owner_user_id, project_id, request_key, kind, asset_ids, state, created_at)
			VALUES(?, ?, ?, ?, ?, ?, 'registering', ?)`, row.ID, owner, row.ProjectID, requestKey, row.Kind, string(encoded), sqlitedb.FormatTime(at))
	return err
}

// FinishBatch marks batch id done.
func (tx *Tx) FinishBatch(ctx context.Context, id string) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE batches SET state = 'done' WHERE id = ?", id)
	return err
}

// DeleteRegisteringBatch forgets batch id while it is still registering.
func (d *DB) DeleteRegisteringBatch(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM batches WHERE id = ? AND state = 'registering'", id)
	return err
}

// AnyBatchRegistering reports whether any batch is still registering.
func (d *DB) AnyBatchRegistering(ctx context.Context) (bool, error) {
	var found bool
	err := queryRow(ctx, d.db, "SELECT EXISTS (SELECT 1 FROM batches WHERE state = 'registering')").Scan(&found)
	return found, err
}

// RegisteringBatches are owner's batches still registering, their State
// "registering" as the query keeps only those.
func (d *DB) RegisteringBatches(ctx context.Context, owner string) ([]BatchRow, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT id, project_id, kind, asset_ids FROM batches WHERE owner_user_id = ? AND state = 'registering'", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []BatchRow
	for rows.Next() {
		b := BatchRow{State: "registering"}
		var assets string
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.Kind, &assets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(assets), &b.Assets); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}
