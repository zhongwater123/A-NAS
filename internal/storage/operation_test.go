package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// Closing the page ends the request that started the plan; the Host Agent
// carries on, and the service follows it to the end (issue #165).
func TestAPlanRunsOnAfterTheRequestAndIsFollowedToTheEnd(t *testing.T) {
	host := newHostOperations()
	service := followingService(t, fake.NewHealthy(), host, nil)
	plan := confirmedPlan(t, service, "disk:fake-data-01")

	ctx, closePage := context.WithCancel(context.Background())
	running, err := service.ExecutePlan(ctx, plan.ID)
	closePage()
	if err != nil || running.State != storage.PlanStateRunning || running.Step != storage.StepErase {
		t.Fatalf("ExecutePlan() = %+v, %v; want running in the erase step", running, err)
	}
	if unfinished := service.UnfinishedPlans(); len(unfinished) != 1 || unfinished[0].ID != plan.ID {
		t.Fatalf("unfinished plans = %+v, want the running plan", unfinished)
	}
	host.set(plan.ID, storage.VolumeOperation{State: storage.OperationRunning, Step: storage.StepFormat})
	waitForPlan(t, service, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.Step == storage.StepFormat })
	host.set(plan.ID, storage.VolumeOperation{State: storage.OperationSucceeded, Volume: &storage.Volume{
		ID: "volume:data", FilesystemUUID: "0f6e1c2a-data", State: storage.VolumeStateAvailable,
	}})
	done := waitForPlan(t, service, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State != storage.PlanStateRunning })
	if done.State != storage.PlanStateSucceeded || done.Step != "" || done.Volume == nil || !slices.Equal(done.Volume.DiskIDs, []string{"disk:fake-data-01"}) {
		t.Fatalf("plan = %+v, want succeeded with the volume", done)
	}
	if got := service.DataVolumeFilesystemUUID(); got != "0f6e1c2a-data" {
		t.Fatalf("data volume UUID = %q, want the new volume's", got)
	}
	if again, err := service.ExecutePlan(context.Background(), plan.ID); err != nil || again.State != storage.PlanStateSucceeded || len(host.started()) != 1 {
		t.Fatalf("executing a succeeded plan = %+v, %v, starts %v; want it unchanged", again, err, host.started())
	}
	if unfinished := service.UnfinishedPlans(); len(unfinished) != 0 {
		t.Fatalf("unfinished plans = %+v, want none", unfinished)
	}
}

// Restarting the product service no longer ends a running plan in
// needs_attention: the new service follows the operation the Host Agent
// went on with.
func TestARestartedServiceFollowsThePlanThatRan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.db")
	host := newHostOperations()
	store := openStore(t, path)
	service := followingService(t, fake.NewHealthy(), host, store)
	plan := confirmedPlan(t, service, "disk:fake-data-01")
	if running, err := service.ExecutePlan(context.Background(), plan.ID); err != nil || running.State != storage.PlanStateRunning {
		t.Fatalf("ExecutePlan() = %+v, %v", running, err)
	}
	service.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	host.set(plan.ID, storage.VolumeOperation{State: storage.OperationSucceeded, Volume: &storage.Volume{FilesystemUUID: "0f6e1c2a-data"}})
	restarted := followingService(t, fake.NewHealthy(), host, openStore(t, path))
	done := waitForPlan(t, restarted, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State != storage.PlanStateRunning })
	if done.State != storage.PlanStateSucceeded || restarted.DataVolumeFilesystemUUID() != "0f6e1c2a-data" {
		t.Fatalf("plan after the restart = %+v, want succeeded", done)
	}
}

// A plan the Host Agent never received changed nothing; it stops in
// needs_attention so that the administrator retries it.
func TestAPlanTheHostAgentNeverReceivedNeedsAttention(t *testing.T) {
	host := newHostOperations()
	host.startErr = errors.New("host agent state is unavailable: connection refused")
	service := followingService(t, fake.NewHealthy(), host, nil)
	plan := confirmedPlan(t, service, "disk:fake-data-01")
	stopped, err := service.ExecutePlan(context.Background(), plan.ID)
	if !errors.Is(err, storage.ErrOperationAttention) || stopped.State != storage.PlanStateNeedsAttention || !strings.Contains(stopped.Failure, "connection refused") {
		t.Fatalf("ExecutePlan() = %+v, %v; want needs_attention", stopped, err)
	}

	// The Host Agent may also lose a request after the product saved the
	// plan as running: the service finds out when it follows the plan.
	path := filepath.Join(t.TempDir(), "storage.db")
	host = newHostOperations()
	store := openStore(t, path)
	service = followingService(t, fake.NewHealthy(), host, store)
	plan = confirmedPlan(t, service, "disk:fake-data-01")
	if _, err := service.ExecutePlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	service.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	host.forget(plan.ID)
	restarted := followingService(t, fake.NewHealthy(), host, openStore(t, path))
	stopped = waitForPlan(t, restarted, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State != storage.PlanStateRunning })
	if stopped.State != storage.PlanStateNeedsAttention || !strings.Contains(stopped.Failure, "never received") {
		t.Fatalf("plan = %+v, want needs_attention", stopped)
	}
}

// A plan the Host Agent stopped in is retried: the Host Agent, not the
// product, decides what is left to do, because the disks are no longer
// unused once formatted. Retrying a plan whose volume is complete takes it
// over.
func TestAPlanThatStoppedIsRetriedFromTheHostAgent(t *testing.T) {
	state, err := fake.NewHealthy().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader := &stateReader{state: state}
	host := newHostOperations()
	service := followingService(t, reader, host, nil)
	plan := confirmedPlan(t, service, "disk:fake-data-01")
	if _, err := service.ExecutePlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	host.set(plan.ID, storage.VolumeOperation{State: storage.OperationInterrupted, Step: storage.StepPrepare,
		Failure: "the Host Agent stopped during the prepare step; retrying continues after the steps that are done"})
	stopped := waitForPlan(t, service, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State != storage.PlanStateRunning })
	if stopped.State != storage.PlanStateNeedsAttention || stopped.Step != storage.StepPrepare || !strings.Contains(stopped.Failure, "prepare") {
		t.Fatalf("plan = %+v, want needs_attention in the prepare step", stopped)
	}
	if unfinished := service.UnfinishedPlans(); len(unfinished) != 1 || unfinished[0].ID != plan.ID {
		t.Fatalf("unfinished plans = %+v, want the stopped plan", unfinished)
	}
	// The volume the plan made is mounted, so new plans are refused.
	mountVolume(reader, []string{"disk:fake-data-01"}, 0, nil, nil)
	if _, err := service.PlanVolume(context.Background(), []string{"disk:fake-data-01"}, ""); !errors.Is(err, storage.ErrVolumeExists) {
		t.Fatalf("plan beside the unrecorded volume error = %v, want ErrVolumeExists", err)
	}

	retried, err := service.ExecutePlan(context.Background(), plan.ID)
	if err != nil || retried.State != storage.PlanStateRunning || retried.Failure != "" || !slices.Equal(host.started(), []string{plan.ID, plan.ID}) {
		t.Fatalf("retry = %+v, %v, starts %v; want the Host Agent asked again", retried, err, host.started())
	}
	host.set(plan.ID, storage.VolumeOperation{State: storage.OperationSucceeded, Volume: &storage.Volume{FilesystemUUID: volumeUUID}})
	done := waitForPlan(t, service, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State != storage.PlanStateRunning })
	if done.State != storage.PlanStateSucceeded || service.DataVolumeFilesystemUUID() != volumeUUID {
		t.Fatalf("plan = %+v, want the volume taken over", done)
	}
}

// Once a later plan has run, a stopped plan is history: it is neither shown
// nor retried.
func TestAStoppedPlanIsSupersededByAPlanThatRan(t *testing.T) {
	clock := time.Date(2026, time.October, 21, 9, 0, 0, 0, time.UTC)
	host := newHostOperations()
	ids := 0
	service, err := storage.OpenService(fake.NewWithSpareDisk(), host, storage.Options{
		Now:          func() time.Time { clock = clock.Add(time.Second); return clock },
		NewID:        func() string { ids++; return "plan-" + string(rune('0'+ids)) },
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	stop := func(plan storage.ExecutionPlan) {
		t.Helper()
		if _, err := service.ExecutePlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		host.set(plan.ID, storage.VolumeOperation{State: storage.OperationFailed, Step: storage.StepErase, Failure: storage.ErrDiskChanged.Error()})
		waitForPlan(t, service, plan.ID, func(plan storage.ExecutionPlan) bool { return plan.State == storage.PlanStateNeedsAttention })
	}
	first := confirmedPlan(t, service, "disk:fake-data-01")
	stop(first)
	second := confirmedPlan(t, service, "disk:fake-data-02")
	if _, err := service.ExecutePlan(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(context.Background(), first.ID); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("retry while another plan runs error = %v, want ErrPlanState", err)
	}
	host.set(second.ID, storage.VolumeOperation{State: storage.OperationFailed, Step: storage.StepErase, Failure: storage.ErrDiskChanged.Error()})
	waitForPlan(t, service, second.ID, func(plan storage.ExecutionPlan) bool { return plan.State == storage.PlanStateNeedsAttention })
	if unfinished := service.UnfinishedPlans(); len(unfinished) != 1 || unfinished[0].ID != second.ID {
		t.Fatalf("unfinished plans = %+v, want only the later one", unfinished)
	}
	if _, err := service.ExecutePlan(context.Background(), first.ID); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("retry of a superseded plan error = %v, want ErrPlanState", err)
	}
}

// hostOperations stands in for the Host Agent's operation journal: an
// operation runs until the test ends it.
type hostOperations struct {
	mu         sync.Mutex
	operations map[string]storage.VolumeOperation
	starts     []string
	startErr   error
}

func newHostOperations() *hostOperations {
	return &hostOperations{operations: make(map[string]storage.VolumeOperation)}
}

func (h *hostOperations) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	return h.start(request.PlanID)
}

func (h *hostOperations) StartAddDisks(_ context.Context, request storage.AddDisksRequest) (storage.VolumeOperation, error) {
	return h.start(request.PlanID)
}

func (h *hostOperations) start(planID string) (storage.VolumeOperation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, planID)
	if h.startErr != nil {
		return storage.VolumeOperation{}, h.startErr
	}
	if operation, ok := h.operations[planID]; ok && (operation.State == storage.OperationRunning || operation.State == storage.OperationSucceeded) {
		return operation, nil
	}
	operation := storage.VolumeOperation{PlanID: planID, State: storage.OperationRunning, Step: storage.StepErase}
	h.operations[planID] = operation
	return operation, nil
}

func (h *hostOperations) VolumeOperation(_ context.Context, planID string) (storage.VolumeOperation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	operation, ok := h.operations[planID]
	if !ok {
		return storage.VolumeOperation{}, storage.ErrOperationNotFound
	}
	return operation, nil
}

func (h *hostOperations) ContinueConversion(context.Context) error { return nil }

func (h *hostOperations) set(planID string, operation storage.VolumeOperation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	operation.PlanID = planID
	h.operations[planID] = operation
}

func (h *hostOperations) forget(planID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.operations, planID)
}

func (h *hostOperations) started() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.starts...)
}

func followingService(t *testing.T, reader hoststate.Reader, host storage.VolumeExecutor, store *storage.Store) *storage.Service {
	t.Helper()
	service, err := storage.OpenService(reader, host, storage.Options{Store: store, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func openStore(t *testing.T, path string) *storage.Store {
	t.Helper()
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func confirmedPlan(t *testing.T, service *storage.Service, diskIDs ...string) storage.ExecutionPlan {
	t.Helper()
	plan, err := service.PlanVolume(context.Background(), diskIDs, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if _, err := service.ConfirmPlan(context.Background(), plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	return plan
}

func waitForPlan(t *testing.T, service *storage.Service, planID string, done func(storage.ExecutionPlan) bool) storage.ExecutionPlan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		plan, err := service.GetPlan(planID)
		if err != nil {
			t.Fatal(err)
		}
		if done(plan) {
			return plan
		}
		if time.Now().After(deadline) {
			t.Fatalf("plan = %+v, still waiting", plan)
		}
		time.Sleep(time.Millisecond)
	}
}
