package storage_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

var storageAdministrator = accounts.User{ID: "admin", Role: accounts.RoleAdmin, Status: accounts.UserStatusActive}

func TestStorageMutationsRejectUnauthorizedActorsBeforeAccessingState(t *testing.T) {
	// No dependencies: authorization must precede even reading host or plan
	// state, including unknown IDs and retry/idempotent execution paths.
	service := &storage.Service{}
	for name, actor := range map[string]accounts.User{
		"missing":                {},
		"member":                 {ID: "member", Role: accounts.RoleMember, Status: accounts.UserStatusActive},
		"disabled administrator": {ID: "admin", Role: accounts.RoleAdmin, Status: accounts.UserStatusDisabled},
		"pending administrator":  {ID: "admin", Role: accounts.RoleAdmin, Status: accounts.UserStatusPending},
	} {
		t.Run(name, func(t *testing.T) {
			calls := map[string]func() error{
				"plan":    func() error { _, err := service.PlanVolume(t.Context(), actor, nil, ""); return err },
				"confirm": func() error { _, err := service.ConfirmPlan(t.Context(), actor, "missing", ""); return err },
				"execute": func() error { _, err := service.ExecutePlan(t.Context(), actor, "missing"); return err },
				"cancel":  func() error { _, err := service.CancelPlan(t.Context(), actor, "missing"); return err },
				"convert": func() error { return service.ContinueConversion(t.Context(), actor) },
			}
			for operation, call := range calls {
				t.Run(operation, func(t *testing.T) {
					if err := call(); !errors.Is(err, accounts.ErrForbidden) {
						t.Fatalf("error = %v, want forbidden", err)
					}
				})
			}
		})
	}
}

func TestMemberCannotChangeAnAdministratorsStoragePlan(t *testing.T) {
	executor := &recordingExecutor{}
	service := storage.NewService(fake.NewHealthy(), executor, storage.Options{})
	t.Cleanup(service.Close)
	member := accounts.User{ID: "member", Role: accounts.RoleMember, Status: accounts.UserStatusActive}
	plan, err := service.PlanVolume(t.Context(), storageAdministrator, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanVolume(t.Context(), member, []string{"disk:fake-data-01"}, ""); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(t.Context(), member, plan.ID, plan.ConfirmationPhrase); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.CancelPlan(t.Context(), member, plan.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatal(err)
	}
	unchanged, err := service.GetPlan(plan.ID)
	if err != nil || !reflect.DeepEqual(unchanged, plan) {
		t.Fatalf("plan changed: %+v, %v", unchanged, err)
	}
	if _, err := service.ConfirmPlan(t.Context(), storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(t.Context(), member, plan.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatal(err)
	}
	unchanged, err = service.GetPlan(plan.ID)
	if err != nil || unchanged.State != storage.PlanStateConfirmed || executor.created.PlanID != "" {
		t.Fatalf("member executed plan: %+v, %v", unchanged, err)
	}
	if _, err := service.ExecutePlan(t.Context(), storageAdministrator, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(t.Context(), member, plan.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("completed plan bypassed authorization: %v", err)
	}
}
