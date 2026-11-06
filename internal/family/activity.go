package family

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// AuditDays bounds the family audit trail, including pending join requests.
const AuditDays = 180

// Activity returns only this account's events, including after leaving the
// family. Device administrators gain no access to another account's events.
func (s *Store) Activity(ctx context.Context, actor Actor, q activity.Query) ([]activity.Event, error) {
	q.Subject = &activity.Subject{ActorIDs: []string{actor.AccountID}}
	q.Reveal, q.TextActors = nil, nil
	return activity.Table{DB: s.db}.List(ctx, q)
}
