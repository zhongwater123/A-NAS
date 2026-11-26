package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of revocations: the photo grants waiting to be
// revoked (docs/architecture/montage-center.md).

// QueueRevocation records that grantID is to be revoked, due at at;
// nothing when it is queued already.
func (d *DB) QueueRevocation(ctx context.Context, grantID string, at time.Time) error {
	return queueRevocation(ctx, d, grantID, at)
}

// QueueRevocation is DB.QueueRevocation inside the transaction.
func (tx *Tx) QueueRevocation(ctx context.Context, grantID string, at time.Time) error {
	return queueRevocation(ctx, tx, grantID, at)
}

func queueRevocation(ctx context.Context, q queryer, grantID string, at time.Time) error {
	_, err := q.ExecContext(ctx, "INSERT INTO revocations(grant_id, next_try) VALUES(?, ?) ON CONFLICT DO NOTHING",
		grantID, sqlitedb.FormatTime(at))
	return err
}

// PostponeRevocation counts a failed try at revoking grantID and makes the
// next one due at next.
func (d *DB) PostponeRevocation(ctx context.Context, grantID string, next time.Time) error {
	_, err := d.ExecContext(ctx, `UPDATE revocations SET attempts = attempts + 1,
			next_try = ? WHERE grant_id = ?`, sqlitedb.FormatTime(next), grantID)
	return err
}

// DeleteRevocation forgets grantID's revocation.
func (d *DB) DeleteRevocation(ctx context.Context, grantID string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM revocations WHERE grant_id = ?", grantID)
	return err
}

// DueRevocations lists the grants whose revocation is due at now, the
// longest due first.
func (d *DB) DueRevocations(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT grant_id FROM revocations WHERE next_try <= ? ORDER BY next_try", sqlitedb.FormatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		due = append(due, id)
	}
	return due, rows.Err()
}
