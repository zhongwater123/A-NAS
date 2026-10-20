package httpapi_test

import (
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
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewWithSpareDisk(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		Storage: storage.NewService(fake.NewWithSpareDisk(), apiVolumeExecutor{}, storage.Options{}),
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
	admin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)

	admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01"],"layout":"mirror"}`, nil, http.StatusUnprocessableEntity)
	response := admin.do(http.MethodPost, "/api/v1/storage/plans", `{"diskIds":["disk:fake-data-01","disk:fake-data-02"]}`, nil, http.StatusCreated)
	var plan storage.ExecutionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Kind != storage.PlanKindCreateVolume || plan.Layout != storage.LayoutMirror || len(plan.Disks) != 2 {
		t.Fatalf("plan = %+v, want a mirror on both disks", plan)
	}
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/confirm", `{"confirmationPhrase":"`+plan.ConfirmationPhrase+`"}`, nil, http.StatusOK)
	admin.do(http.MethodPost, "/api/v1/storage/plans/"+plan.ID+"/execute", `{}`, nil, http.StatusOK)

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
