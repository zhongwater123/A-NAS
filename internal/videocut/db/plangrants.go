package db

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of the planning grants' registrations (规划授权;
// docs/architecture/videocut-planning-session.md).

// RegisterPlanGrant records that owner, signed in as username, has a
// planning grant registered at at, replacing the registration before.
func (d *DB) RegisterPlanGrant(ctx context.Context, owner, username string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO plan_grants(owner_user_id, username, registered_at) VALUES(?, ?, ?)
		ON CONFLICT(owner_user_id) DO UPDATE SET username = excluded.username, registered_at = excluded.registered_at`,
		owner, username, sqlitedb.FormatTime(at))
	return err
}

// PlanGrant is owner's last registration: the username and when, as kept;
// ErrNotFound when owner has none.
func (c *conn) PlanGrant(ctx context.Context, owner string) (username, at string, err error) {
	err = queryRow(ctx, c.q, "SELECT username, registered_at FROM plan_grants WHERE owner_user_id = ?", owner).Scan(&username, &at)
	return username, at, err
}

// DeletePlanGrant forgets owner's registration.
func (tx *Tx) DeletePlanGrant(ctx context.Context, owner string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM plan_grants WHERE owner_user_id = ?", owner)
	return err
}
