package accounts_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

func TestASystemUpdateOutcomeIsWrittenOnceAtTheTimeItHappened(t *testing.T) {
	service, admin := newSignInService(t, time.Now)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 3, 10, 0, 0, time.UTC)
	// Several readers offer the same outcome at once.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := service.RecordSystemUpdate(ctx, "system", "system.update.result.updated",
				"2026-11-14T03:10:00Z updated", `{"version":"2"}`, at); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	events, err := service.Activity(ctx, admin, activity.Query{Categories: []activity.Category{activity.CategorySystem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("the outcome was written %d times, want once", len(events))
	}
	if !events[0].OccurredAt.Equal(at) || events[0].ActorUserID != "system" {
		t.Fatalf("event = %+v, want the system's, at %s", events[0], at)
	}
}
