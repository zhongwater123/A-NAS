package db

import "context"

// The reads and writes of job_assets: the videos a job makes its films
// from, each with the content it had when the job took it
// (docs/architecture/montage-center.md).

// JobAsset is one of a job's videos and the content the job took.
type JobAsset struct{ AssetID, ObjectID string }

// AddJobAsset gives jobID assetID as objectID.
func (tx *Tx) AddJobAsset(ctx context.Context, jobID, assetID, objectID string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) VALUES(?, ?, ?)", jobID, assetID, objectID)
	return err
}

// CopyProjectAssets gives jobID every video projectID holds, as it holds
// them.
func (tx *Tx) CopyProjectAssets(ctx context.Context, jobID, projectID string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) SELECT ?, asset_id, object_id FROM project_assets WHERE project_id = ?",
		jobID, projectID)
	return err
}

// CopyJobAssets gives jobID the videos of fromJobID, as that job took them.
func (tx *Tx) CopyJobAssets(ctx context.Context, jobID, fromJobID string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO job_assets(job_id, asset_id, object_id) SELECT ?, asset_id, object_id FROM job_assets WHERE job_id = ?",
		jobID, fromJobID)
	return err
}

// DeleteJobAsset takes assetID out of jobID.
func (tx *Tx) DeleteJobAsset(ctx context.Context, jobID, assetID string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM job_assets WHERE job_id = ? AND asset_id = ?", jobID, assetID)
	return err
}

// JobAssets are jobID's videos with their content, by asset id.
func (d *DB) JobAssets(ctx context.Context, jobID string) ([]JobAsset, error) {
	rows, err := d.QueryContext(ctx, "SELECT asset_id, object_id FROM job_assets WHERE job_id = ? ORDER BY asset_id", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []JobAsset
	for rows.Next() {
		var asset JobAsset
		if err := rows.Scan(&asset.AssetID, &asset.ObjectID); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

// JobAssetIDs are jobID's videos, by asset id.
func (d *DB) JobAssetIDs(ctx context.Context, jobID string) ([]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT asset_id FROM job_assets WHERE job_id = ? ORDER BY asset_id", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		assets = append(assets, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, nil
}

// JobAssetObjects is the content jobID took of each of its videos.
func (d *DB) JobAssetObjects(ctx context.Context, jobID string) (map[string]string, error) {
	objects := map[string]string{}
	rows, err := d.QueryContext(ctx, "SELECT asset_id, object_id FROM job_assets WHERE job_id = ?", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var asset, object string
		if err := rows.Scan(&asset, &object); err != nil {
			return nil, err
		}
		objects[asset] = object
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}
