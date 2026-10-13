package storage

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// ActivityCategories are the activity log categories the storage audit holds.
var ActivityCategories = []activity.Category{activity.CategoryStorage}

// Activity returns a page of storage plan events. They are system events, so
// only administrators may be shown them; the caller checks that.
func (s *Service) Activity(ctx context.Context, q activity.Query) ([]activity.Event, error) {
	if s.store == nil {
		return nil, nil
	}
	return activity.Table{DB: s.store.db}.List(ctx, q)
}
