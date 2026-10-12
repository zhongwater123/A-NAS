package files

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
)

// ActivityCategories are the activity log categories the file audit holds.
var ActivityCategories = []activity.Category{activity.CategoryFile}

// Activity returns a page of file and snapshot events, each with its space.
// Members only ever see their own.
func (s *Service) Activity(ctx context.Context, actor accounts.User, q activity.Query) ([]activity.Event, error) {
	if actor.Status != accounts.UserStatusActive {
		return nil, ErrForbidden
	}
	if actor.Role != accounts.RoleAdmin {
		q.Subject = &activity.Subject{ActorIDs: []string{actor.ID}}
		q.Reveal = nil
	}
	return activity.Table{DB: s.store.db, SpaceColumn: true}.List(ctx, q)
}
