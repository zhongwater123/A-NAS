package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// planned is a fresh plan to erase the fake host's blank disk, on a clock the
// test moves, and the executor that would erase it.
func planned(t *testing.T) (*storage.Service, storage.ExecutionPlan, *clocktest.Clock, *recordingExecutor) {
	t.Helper()
	clock := clocktest.New(time.Date(2026, time.November, 9, 9, 0, 0, 0, time.UTC))
	executor := &recordingExecutor{}
	service := newService(t, fake.NewHealthy(), executor, storage.Options{Now: clock.Now})
	t.Cleanup(service.Close)
	plan, err := service.PlanVolume(t.Context(), storageAdministrator, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return service, plan, clock, executor
}

// assertPlanState fails unless the stored plan is in state and the disks
// were never handed to the executor.
func assertPlanState(t *testing.T, service *storage.Service, executor *recordingExecutor, planID string, state storage.PlanState) {
	t.Helper()
	stored, err := service.GetPlan(planID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != state {
		t.Fatalf("plan state = %q, want %q", stored.State, state)
	}
	if executor.created.PlanID != "" {
		t.Fatalf("the executor erased the disks for plan %q", executor.created.PlanID)
	}
}

// Only the plan's own phrase, typed exactly, confirms erasing the disks.
func TestAPlanIsConfirmedOnlyWithItsExactPhrase(t *testing.T) {
	service, plan, _, executor := planned(t)
	for _, phrase := range []string{"", "erase data-01", "ERASE data-02", plan.ConfirmationPhrase + " ", "ERASE"} {
		if _, err := service.ConfirmPlan(t.Context(), storageAdministrator, plan.ID, phrase); !errors.Is(err, storage.ErrConfirmation) {
			t.Fatalf("ConfirmPlan(%q) error = %v, want ErrConfirmation", phrase, err)
		}
	}
	assertPlanState(t, service, executor, plan.ID, storage.PlanStatePlanned)
	if _, err := service.ExecutePlan(t.Context(), storageAdministrator, plan.ID); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("ExecutePlan() of an unconfirmed plan error = %v, want ErrPlanState", err)
	}
	assertPlanState(t, service, executor, plan.ID, storage.PlanStatePlanned)
}

// A plan describes the disks as they were when it was made, so it cannot be
// confirmed once its ten minutes are up, even with the right phrase.
func TestAPlanCannotBeConfirmedOnceItExpires(t *testing.T) {
	service, plan, clock, executor := planned(t)
	clock.Set(plan.ExpiresAt)
	expired, err := service.ConfirmPlan(t.Context(), storageAdministrator, plan.ID, plan.ConfirmationPhrase)
	if !errors.Is(err, storage.ErrPlanExpired) {
		t.Fatalf("ConfirmPlan() at expiry error = %v, want ErrPlanExpired", err)
	}
	if expired.State != storage.PlanStateExpired {
		t.Fatalf("returned plan state = %q, want %q", expired.State, storage.PlanStateExpired)
	}
	assertPlanState(t, service, executor, plan.ID, storage.PlanStateExpired)
	if _, err := service.ExecutePlan(t.Context(), storageAdministrator, plan.ID); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("ExecutePlan() of an expired plan error = %v, want ErrPlanState", err)
	}
	assertPlanState(t, service, executor, plan.ID, storage.PlanStateExpired)
}

// Confirming does not extend the ten minutes: a confirmed plan that was not
// started in time expires instead of erasing the disks.
func TestAConfirmedPlanDoesNotRunOnceItExpires(t *testing.T) {
	service, plan, clock, executor := planned(t)
	if _, err := service.ConfirmPlan(t.Context(), storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	clock.Set(plan.ExpiresAt)
	expired, err := service.ExecutePlan(t.Context(), storageAdministrator, plan.ID)
	if !errors.Is(err, storage.ErrPlanExpired) {
		t.Fatalf("ExecutePlan() at expiry error = %v, want ErrPlanExpired", err)
	}
	if expired.State != storage.PlanStateExpired {
		t.Fatalf("returned plan state = %q, want %q", expired.State, storage.PlanStateExpired)
	}
	assertPlanState(t, service, executor, plan.ID, storage.PlanStateExpired)
}
