package photos

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// Activity returns a page of the caller's own photo library events. Photo
// activity names what is in private libraries, so nobody sees anyone else's,
// administrators included.
func (s *Service) Activity(ctx context.Context, p Principal, q activity.Query) ([]activity.Event, error) {
	q.Subject = &activity.Subject{ActorIDs: []string{p.UserID}}
	q.Reveal = nil
	q.TextActors = nil
	return activity.Table{DB: s.db}.List(ctx, q)
}
