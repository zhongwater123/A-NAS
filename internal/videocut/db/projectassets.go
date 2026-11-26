package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of project_assets: the videos a project holds,
// each with the content it was added with (docs/architecture/
// videocut-center.md).

// AddProjectAsset records that batchID added assetID, as objectID, to
// projectID at at; nothing when the project holds it already.
func (tx *Tx) AddProjectAsset(ctx context.Context, projectID, assetID, objectID, batchID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO project_assets(project_id, asset_id, object_id, batch_id, added_at)
				VALUES(?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, projectID, assetID, objectID, batchID, sqlitedb.FormatTime(at))
	return err
}

// HeldAssets reports which of ids projectID holds.
func (d *DB) HeldAssets(ctx context.Context, projectID string, ids []string) (map[string]bool, error) {
	args := []any{projectID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.db.QueryContext(ctx, "SELECT asset_id FROM project_assets WHERE project_id = ? AND asset_id IN ("+
		sqlitedb.Placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	held := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		held[id] = true
	}
	return held, rows.Err()
}

// ProjectAssetIDs are projectID's videos in the order they were added.
func (d *DB) ProjectAssetIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT asset_id FROM project_assets WHERE project_id = ? ORDER BY added_at, asset_id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		all = append(all, id)
	}
	return all, rows.Err()
}

// ProjectAssetRow is a video a project holds: the content it was added
// with and when.
type ProjectAssetRow struct {
	AssetID, ObjectID string
	AddedAt           time.Time
}

// ProjectAssets are projectID's videos in the order they were added.
func (d *DB) ProjectAssets(ctx context.Context, projectID string) ([]ProjectAssetRow, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT asset_id, object_id, added_at FROM project_assets WHERE project_id = ? ORDER BY added_at, asset_id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []ProjectAssetRow
	for rows.Next() {
		var asset ProjectAssetRow
		var added string
		if err := rows.Scan(&asset.AssetID, &asset.ObjectID, &added); err != nil {
			return nil, err
		}
		if asset.AddedAt, err = time.Parse(time.RFC3339Nano, added); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

// ProjectAssetCount is how many videos projectID holds.
func (tx *Tx) ProjectAssetCount(ctx context.Context, projectID string) (int, error) {
	var count int
	err := tx.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM project_assets WHERE project_id = ?", projectID).Scan(&count)
	return count, err
}

// DeleteProjectAsset takes assetID out of projectID.
func (tx *Tx) DeleteProjectAsset(ctx context.Context, projectID, assetID string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM project_assets WHERE project_id = ? AND asset_id = ?", projectID, assetID)
	return err
}
