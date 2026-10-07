package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestAdministratorCanPlanConfirmAndExecuteDataVolumeCreation(t *testing.T) {
	now := time.Date(2026, time.October, 7, 9, 0, 0, 0, time.UTC)
	executor := &recordingExecutor{}
	service := storage.NewService(fake.NewHealthy(), executor, storage.Options{
		Now:   func() time.Time { return now },
		NewID: func() string { return "plan-01" },
	})

	plan, err := service.PlanCreateVolume(context.Background(), "disk:fake-data-01")
	if err != nil {
		t.Fatalf("PlanCreateVolume() error = %v", err)
	}
	if got, want := plan.State, storage.PlanStatePlanned; got != want {
		t.Fatalf("plan state = %q, want %q", got, want)
	}
	if got, want := plan.ConfirmationPhrase, "ERASE data-01"; got != want {
		t.Fatalf("confirmation phrase = %q, want %q", got, want)
	}
	if got, want := plan.ExpiresAt, now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
	if len(plan.Actions) == 0 {
		t.Fatal("plan has no explicit actions")
	}

	confirmed, err := service.ConfirmPlan(context.Background(), plan.ID, plan.ConfirmationPhrase)
	if err != nil {
		t.Fatalf("ConfirmPlan() error = %v", err)
	}
	if got, want := confirmed.State, storage.PlanStateConfirmed; got != want {
		t.Fatalf("confirmed state = %q, want %q", got, want)
	}

	completed, err := service.ExecutePlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if got, want := completed.State, storage.PlanStateSucceeded; got != want {
		t.Fatalf("completed state = %q, want %q", got, want)
	}
	if got, want := executor.executedDiskID, "disk:fake-data-01"; got != want {
		t.Fatalf("executed disk = %q, want %q", got, want)
	}
	if _, err := service.PlanCreateVolume(context.Background(), "disk:fake-data-01"); err != storage.ErrVolumeExists {
		t.Fatalf("second volume plan error = %v, want ErrVolumeExists", err)
	}
}

func TestExecutionPlanSurvivesServiceRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "storage.db")
	store, err := storage.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 7, 10, 0, 0, 0, time.UTC)
	service, err := storage.OpenService(fake.NewHealthy(), &recordingExecutor{}, storage.Options{
		Now: func() time.Time { return now }, NewID: func() string { return "plan-persisted" }, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanCreateVolume(ctx, "disk:fake-data-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := storage.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	executor := &recordingExecutor{}
	restarted, err := storage.OpenService(fake.NewHealthy(), executor, storage.Options{Now: func() time.Time { return now }, Store: reopenedStore})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.ExecutePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ExecutePlan() after restart error = %v", err)
	}
	if completed.State != storage.PlanStateSucceeded || executor.executedDiskID != "disk:fake-data-01" {
		t.Fatalf("completed = %#v, executed disk = %q", completed, executor.executedDiskID)
	}
	volumes, err := restarted.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].ID != "volume:data" {
		t.Fatalf("volumes = %#v", volumes)
	}
	audit, err := restarted.ListAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundSucceeded := false
	for _, event := range audit {
		if event.Action == "storage.plan.succeeded" {
			foundSucceeded = true
		}
	}
	if !foundSucceeded {
		t.Fatalf("storage audit = %#v, want succeeded event", audit)
	}
}

type recordingExecutor struct {
	executedDiskID string
}

func (e *recordingExecutor) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	e.executedDiskID = request.DiskID
	return storage.Volume{ID: "volume:data", DiskID: request.DiskID, State: storage.VolumeStateAvailable}, nil
}
