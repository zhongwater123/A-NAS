package accounts

import "context"

// RecordSystemUpdate writes a system update event to the activity record
// (docs/specs/system-update.md): an administrator's request, with actorID,
// or an outcome the update service recorded, with no actor. resourceID,
// when set, names the event, which is then written only once however often
// it is offered.
func (s *Service) RecordSystemUpdate(ctx context.Context, actorID, action, resourceID, detail string) error {
	if resourceID != "" {
		var written bool
		err := s.store.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM audit_events WHERE action = ? AND resource_id = ?)", action, resourceID).Scan(&written)
		if err != nil {
			return err
		}
		if written {
			return nil
		}
	}
	return s.appendAudit(ctx, actorID, action, "system-update", resourceID, detail)
}
