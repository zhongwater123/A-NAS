package accounts

import (
	"context"
	"time"
)

// RecordSystemUpdate writes a system update event to the activity record
// (docs/specs/system-update.md): an administrator's request, with actorID,
// or what the system did, with the system's actor. at is when it happened;
// zero is now. resourceID, when set, names the event, which is then written
// only once however often it is offered: the check and the write are one
// statement, so two readers offering it at once write it once.
func (s *Service) RecordSystemUpdate(ctx context.Context, actorID, action, resourceID, detail string, at time.Time) error {
	if at.IsZero() {
		at = s.now()
	}
	if resourceID == "" {
		_, err := s.store.db.ExecContext(ctx,
			"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)",
			s.randomID("audit"), nullableString(actorID), action, "system-update", resourceID, formatTime(at.UTC()), detail)
		return err
	}
	_, err := s.store.db.ExecContext(ctx, `INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail)
SELECT ?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM audit_events WHERE action = ? AND resource_id = ?)`,
		s.randomID("audit"), nullableString(actorID), action, "system-update", resourceID, formatTime(at.UTC()), detail,
		action, resourceID)
	return err
}
