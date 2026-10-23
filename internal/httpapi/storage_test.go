package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestStoragePlansOnSeveralDisksFollowTheContract(t *testing.T) {
	admin := storageAdmin(t, apiVolumeExecutor{})

	admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01"],"layout":"mirror"}`, nil, http.StatusUnprocessableEntity)
	response := admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01"]}`, nil, http.StatusCreated)
	var abandoned storage.ExecutionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &abandoned); err != nil {
		t.Fatal(err)
	}
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+abandoned.ID+"/cancel", `{}`, nil, http.StatusOK)
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+abandoned.ID+"/execute", `{}`, nil, http.StatusConflict)
	// A reload forgets a pending plan; the next one takes its place.
	admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-02"]}`, nil, http.StatusCreated)
	response = admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01","disk:fake-data-02"]}`, nil, http.StatusCreated)
	var plan storage.ExecutionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Kind != storage.PlanKindCreateVolume || plan.Layout != storage.LayoutMirror || len(plan.Disks) != 2 {
		t.Fatalf("plan = %+v, want a mirror on both disks", plan)
	}
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/confirm", `{"confirmationPhrase":"`+plan.ConfirmationPhrase+`"}`, nil, http.StatusOK)
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/execute", `{}`, nil, http.StatusOK)
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/cancel", `{}`, nil, http.StatusConflict)
	if unfinished := unfinishedPlans(t, admin); len(unfinished) != 0 {
		t.Fatalf("unfinished plans = %+v, want none", unfinished)
	}

	response = admin.do(http.MethodGet, "/api/v1/volumes", "", nil, http.StatusOK)
	var volumes struct{ Items []storage.Volume }
	if err := json.Unmarshal(response.Body.Bytes(), &volumes); err != nil {
		t.Fatal(err)
	}
	if len(volumes.Items) != 1 || volumes.Items[0].Layout != storage.LayoutMirror || len(volumes.Items[0].DiskIDs) != 2 {
		t.Fatalf("volumes = %+v", volumes.Items)
	}
	// The simulated host never mounts it: nothing converts and nothing joins.
	admin.do(http.MethodPost, "/api/v1/volumes/"+volumes.Items[0].ID+"/conversion", "", nil, http.StatusConflict)
	admin.do(http.MethodPost, "/api/v1/volumes/volume:other/conversion", "", nil, http.StatusNotFound)
}

// The Host Agent runs a plan apart from the request: execute answers 202
// with the step, and the plan can be found again after a reload.
func TestARunningStoragePlanFollowsTheContract(t *testing.T) {
	admin := storageAdmin(t, runningVolumeExecutor{})
	response := admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01"]}`, nil, http.StatusCreated)
	var plan storage.ExecutionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/confirm", `{"confirmationPhrase":"`+plan.ConfirmationPhrase+`"}`, nil, http.StatusOK)
	response = admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/execute", `{}`, nil, http.StatusAccepted)
	var running storage.ExecutionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	if running.State != storage.PlanStateRunning || running.Step != storage.StepFormat {
		t.Fatalf("executed plan = %+v, want running in the format step", running)
	}
	if unfinished := unfinishedPlans(t, admin); len(unfinished) != 1 || unfinished[0].ID != plan.ID {
		t.Fatalf("unfinished plans = %+v, want the running plan", unfinished)
	}
	admin.do(http.MethodGet, "/api/v1/operations/"+plan.ID, "", nil, http.StatusOK)
	admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-02"]}`, nil, http.StatusConflict)
}

func storageAdmin(t *testing.T, executor storage.VolumeExecutor) *contractClient {
	t.Helper()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	storageService := storage.NewService(fake.NewWithSpareDisk(), executor, storage.Options{})
	t.Cleanup(storageService.Close)
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewWithSpareDisk(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		Storage: storageService,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	f := &activityFixture{t: t, handler: handler, accounts: accountService, client: &contractClient{t: t, handler: handler, router: router}}
	return f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
}

func unfinishedPlans(t *testing.T, admin *contractClient) []storage.ExecutionPlan {
	t.Helper()
	response := admin.do(http.MethodGet, "/api/v1/storage/plans", "", nil, http.StatusOK)
	var page struct{ Items []storage.ExecutionPlan }
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// runningVolumeExecutor reports every operation formatting.
type runningVolumeExecutor struct{ apiVolumeExecutor }

func (runningVolumeExecutor) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	return storage.VolumeOperation{PlanID: request.PlanID, State: storage.OperationRunning, Step: storage.StepFormat}, nil
}

func (runningVolumeExecutor) VolumeOperation(_ context.Context, planID string) (storage.VolumeOperation, error) {
	return storage.VolumeOperation{PlanID: planID, State: storage.OperationRunning, Step: storage.StepFormat}, nil
}
