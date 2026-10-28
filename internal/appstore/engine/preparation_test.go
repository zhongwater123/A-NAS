package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

func TestInstallReservationWaitsForTheConfirmedIdentity(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	store, _ := newStore(t, &fakeDocker{}, runner)
	plan, err := store.Plan(ctx, "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveInstall(ctx, "memos", plan.Digest, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CancelInstall(ctx, "memos", reservation.Token); store.Stop() })
	if got := runner.verbs(); len(got) != 0 {
		t.Fatalf("Compose before preparation: %v", got)
	}
	wrong := identity
	wrong.UID++
	if _, err := store.CommitInstall(ctx, "memos", reservation.Token, wrong); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("changed identity: %v", err)
	}
	if _, err := store.CommitInstall(ctx, "navidrome", reservation.Token, identity); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("other app: %v", err)
	}
	if _, err := store.CommitInstall(ctx, "memos", reservation.Token, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitInstall(ctx, "memos", reservation.Token, identity); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("reused token: %v", err)
	}
	store.Wait()
	if got := runner.verbs(); len(got) != 1 || got[0] != "up" {
		t.Fatalf("Compose after preparation: %v", got)
	}
}

func TestAbandonedInstallReservationExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		runner := &fakeRunner{}
		store, _ := newStore(t, &fakeDocker{}, runner)
		plan, err := store.Plan(ctx, "memos", identity)
		if err != nil {
			t.Fatal(err)
		}
		reservation, err := store.ReserveInstall(ctx, "memos", plan.Digest, identity)
		if err != nil {
			t.Fatal(err)
		}
		store.Wait()
		if got := runner.verbs(); len(got) != 0 {
			t.Fatalf("abandoned reservation ran Compose: %v", got)
		}
		if _, err := store.CommitInstall(ctx, "memos", reservation.Token, identity); !errors.Is(err, appstore.ErrPlanChanged) {
			t.Fatalf("expired token: %v", err)
		}
		apps, err := store.Apps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, app := range apps {
			if app.ID == "memos" && (app.Job == nil || app.Job.State != appstore.JobFailed || !strings.Contains(app.Job.Error, "preparation")) {
				t.Fatalf("expired reservation: %+v", app)
			}
		}
	})
}

func TestStoppingTheAgentCancelsAPreparation(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}
	store, _ := newStore(t, &fakeDocker{}, runner)
	plan, err := store.Plan(ctx, "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveInstall(ctx, "memos", plan.Digest, identity)
	if err != nil {
		t.Fatal(err)
	}
	store.Stop()
	if got := runner.verbs(); len(got) != 0 {
		t.Fatalf("stopped preparation ran Compose: %v", got)
	}
	if _, err := store.CommitInstall(ctx, "memos", reservation.Token, identity); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("stopped agent accepted commit: %v", err)
	}
}
