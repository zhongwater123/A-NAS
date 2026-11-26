package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of revocations: the photo grants waiting to be
// revoked (docs/architecture/videocut-center.md).

// QueueRevocation records that grantID is to be revoked, due at at;
// nothing when it is queued already.
func (c *Conn) QueueRevocation(ctx context.Context, grantID string, at time.Time) error {
	_, err := c.q.ExecContext(ctx, "INSERT INTO revocations(grant_id, next_try) VALUES(?, ?) ON CONFLICT DO NOTHING",
		grantID, sqlitedb.FormatTime(at))
	return err
}

// PostponeRevocation counts a failed try at revoking grantID and makes the
// next one due at next.
func (d *DB) PostponeRevocation(ctx context.Context, grantID string, next time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE revocations SET attempts = attempts + 1,
			next_try = ? WHERE grant_id = ?`, sqlitedb.FormatTime(next), grantID)
	return err
}

// DeleteRevocation forgets grantID's revocation.
func (d *DB) DeleteRevocation(ctx context.Context, grantID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM revocations WHERE grant_id = ?", grantID)
	return err
}

// DueRevocations lists the grants whose revocation is due at now, the
// longest due first.
func (d *DB) DueRevocations(ctx context.Context, now time.Time) ([]string, error) {
	return column[string](ctx, d.db, "SELECT grant_id FROM revocations WHERE next_try <= ? ORDER BY next_try", sqlitedb.FormatTime(now))
}
