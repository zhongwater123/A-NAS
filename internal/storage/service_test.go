package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
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

	plan, err := service.PlanVolume(context.Background(), []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
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
	// The fake host never mounts the volume, so nothing can join it.
	if _, err := service.PlanVolume(context.Background(), []string{"disk:fake-data-01"}, storage.LayoutMirror); !errors.Is(err, storage.ErrVolumeUnavailable) {
		t.Fatalf("plan for an offline volume error = %v, want ErrVolumeUnavailable", err)
	}
}

func TestPlanForBlankDiskSerializesEmptySignaturesArray(t *testing.T) {
	service := storage.NewService(fake.NewHealthy(), &recordingExecutor{}, storage.Options{
		Now:   func() time.Time { return time.Date(2026, time.October, 7, 9, 0, 0, 0, time.UTC) },
		NewID: func() string { return "plan-blank-disk" },
	})

	plan, err := service.PlanVolume(context.Background(), []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if plan.Disks[0].Signatures == nil {
		t.Fatal("blank-disk signatures = nil, want an empty array")
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"signatures":[]`) {
		t.Fatalf("plan JSON = %s, want signatures array", payload)
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
	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-01"}, "")
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

func TestListVolumesRefreshesFilesystemUsageFromHostState(t *testing.T) {
	ctx := context.Background()
	state, err := fake.NewHealthy().Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := &stateReader{state: state}
	service := storage.NewService(reader, &recordingExecutor{}, storage.Options{NewID: func() string { return "plan-usage" }})
	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	total, available := uint64(500_107_862_016), uint64(482_000_000_000)
	for index := range reader.state.Disks {
		if reader.state.Disks[index].ID.String() == "disk:fake-data-01" {
			reader.state.Disks[index].Role = hoststate.DiskRoleData
			reader.state.Disks[index].FilesystemCapacityBytes = &total
			reader.state.Disks[index].FilesystemAvailableBytes = &available
		}
	}

	volumes, err := service.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].CapacityBytes != total || volumes[0].AvailableBytes == nil || *volumes[0].AvailableBytes != available {
		t.Fatalf("volumes = %#v, want live filesystem usage", volumes)
	}
}

// The file service checks the mounted volume against this UUID before every
// request; a Host Agent read there made each one wait for the disks.
func TestDataVolumeFilesystemUUIDNeedsNoHostRead(t *testing.T) {
	ctx := context.Background()
	state, err := fake.NewHealthy().Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := &stateReader{state: state}
	service := storage.NewService(reader, &recordingExecutor{}, storage.Options{NewID: func() string { return "plan-uuid" }})
	if got := service.DataVolumeFilesystemUUID(); got != "" {
		t.Fatalf("UUID before any volume = %q, want empty", got)
	}
	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	reader.reads = 0
	reader.failing = true

	if got, want := service.DataVolumeFilesystemUUID(), "0f6e1c2a-data"; got != want {
		t.Fatalf("UUID = %q, want %q", got, want)
	}
	if reader.reads != 0 {
		t.Fatalf("host reads = %d, want none", reader.reads)
	}
}

type recordingExecutor struct {
	executedDiskID string
	created        storage.CreateVolumeRequest
	added          []storage.AddDisksRequest
	continued      int
}

type stateReader struct {
	state   hoststate.State
	reads   int
	failing bool
}

func (r *stateReader) Read(ctx context.Context) (hoststate.State, error) {
	r.reads++
	if err := ctx.Err(); err != nil {
		return hoststate.State{}, err
	}
	if r.failing {
		return hoststate.State{}, errors.New("host agent timed out")
	}
	return r.state, nil
}

// recordingExecutor finishes every operation at once.
func (e *recordingExecutor) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	e.executedDiskID = request.Disks[0].DiskID
	e.created = request
	return storage.VolumeOperation{PlanID: request.PlanID, State: storage.OperationSucceeded, Volume: &storage.Volume{
		ID: "volume:data", DiskIDs: []string{request.Disks[0].DiskID}, FilesystemUUID: "0f6e1c2a-data", State: storage.VolumeStateAvailable,
	}}, nil
}

func (e *recordingExecutor) StartAddDisks(_ context.Context, request storage.AddDisksRequest) (storage.VolumeOperation, error) {
	e.added = append(e.added, request)
	return storage.VolumeOperation{PlanID: request.PlanID, State: storage.OperationSucceeded, Volume: &storage.Volume{
		FilesystemUUID: request.FilesystemUUID, State: storage.VolumeStateAvailable,
	}}, nil
}

func (e *recordingExecutor) VolumeOperation(context.Context, string) (storage.VolumeOperation, error) {
	return storage.VolumeOperation{}, storage.ErrOperationNotFound
}

func (e *recordingExecutor) ContinueConversion(context.Context) error {
	e.continued++
	return nil
}
