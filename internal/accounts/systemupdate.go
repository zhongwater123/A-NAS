package accounts

import (
	"context"
	"time"
)

// RecordSystemUpdate writes a system update event to the activity record
// (docs/specs/system-update.md): an administrator's request, with actorID,
// or what the system did, with the system's actor. at is when it happened;
// zero is now. resourceID, when set, names the event, which is then written
// only once however often it is offered. It returns whether it was written.
func (s *Service) RecordSystemUpdate(ctx context.Context, actorID, action, resourceID, detail string, at time.Time) (bool, error) {
	if at.IsZero() {
		at = s.now()
	}
	return s.insertAudit(ctx, s.store.db, actorID, action, "system-update", resourceID, detail, at, resourceID != "")
}
