package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
	"github.com/zhongwater123/A-NAS/internal/systemupdateapi"
)

type systemUpdateStatus struct {
	Phase     string   `json:"phase"`
	Running   bool     `json:"running"`
	Current   string   `json:"current"`
	Previous  string   `json:"previous"`
	Candidate string   `json:"candidate"`
	Waiting   []string `json:"waiting"`
	Records   []struct {
		Kind string `json:"kind"`
		By   string `json:"by"`
	} `json:"records"`
	Settings systemupdate.Settings `json:"settings"`
}

func TestSystemUpdateFollowsTheContractForAdministratorsAlone(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Each operation of the service in memory ends once 100 ms have passed.
	updater := systemupdate.NewMemory("1.0.1", "1.0.2", 100*time.Millisecond)
	var waiting []string
	work := func(context.Context) []string { return waiting }
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		SystemUpdate: systemupdateapi.New(updater, work, accountService, logger), Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, accounts: accountService, client: &contractClient{t: t, handler: handler}}
	asAdmin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	session, err := accountService.ResolveSession(ctx, asAdmin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, session.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)

	status := func() systemUpdateStatus {
		t.Helper()
		var status systemUpdateStatus
		if err := json.Unmarshal(asAdmin.do(http.MethodGet, "/api/v1/system/update", "", nil, http.StatusOK).Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	settled := func() systemUpdateStatus {
		t.Helper()
		time.Sleep(150 * time.Millisecond)
		return status()
	}

	// Members neither see nor act.
	asAlice.do(http.MethodGet, "/api/v1/system/update", "", nil, http.StatusForbidden)
	asAlice.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"install"}`, nil, http.StatusForbidden)
	asAlice.do(http.MethodPut, "/api/v1/system/update/settings", `{"autoDownload":true,"autoInstall":true,"windowStart":"03:00","windowEnd":"05:00"}`, nil, http.StatusForbidden)

	if got := status(); got.Phase != "ready" || got.Candidate != "1.0.2" || got.Running {
		t.Fatalf("status = %+v, want 1.0.2 ready", got)
	}
	// An install waits while the services must not restart.
	waiting = []string{"apps"}
	asAdmin.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"install"}`, nil, http.StatusConflict)
	if got := status(); len(got.Waiting) != 1 || got.Waiting[0] != "apps" {
		t.Fatalf("waiting = %v", got.Waiting)
	}
	waiting = nil
	asAdmin.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"install"}`, nil, http.StatusAccepted)
	asAdmin.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"check"}`, nil, http.StatusConflict)
	got := settled()
	if got.Phase != "up-to-date" || got.Current != "1.0.2" || got.Previous != "1.0.1" || got.Records[0].Kind != "updated" || got.Records[0].By != "owner" {
		t.Fatalf("after the install = %+v", got)
	}
	asAdmin.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"install"}`, nil, http.StatusConflict)

	asAdmin.do(http.MethodPost, "/api/v1/system/update/actions", `{"action":"revert"}`, nil, http.StatusAccepted)
	if got := settled(); got.Current != "1.0.1" || got.Previous != "" || got.Records[0].Kind != "reverted" {
		t.Fatalf("after going back = %+v", got)
	}

	asAdmin.do(http.MethodPut, "/api/v1/system/update/settings", `{"autoDownload":false,"autoInstall":true,"windowStart":"23:30","windowEnd":"02:00"}`, nil, http.StatusOK)
	if got := status().Settings; got != (systemupdate.Settings{AutoInstall: true, WindowStart: "23:30", WindowEnd: "02:00"}) {
		t.Fatalf("settings = %+v", got)
	}
	asAdmin.do(http.MethodPut, "/api/v1/system/update/settings", `{"autoDownload":false,"autoInstall":true,"windowStart":"02:00","windowEnd":"02:00"}`, nil, http.StatusBadRequest)

	// Requests and outcomes are in the activity record under 系统, each
	// outcome once however often the page reads it.
	status()
	actions := map[string]int{}
	for _, item := range asAdmin.activity("?category=system").Items {
		actions[item.Action]++
	}
	for action, want := range map[string]int{
		"system.update.install": 1, "system.update.revert": 1, "system.update.settings": 1,
		"system.update.result.updated": 2, "system.update.result.reverted": 1,
	} {
		if actions[action] != want {
			t.Errorf("activity has %d of %s, want %d (all: %v)", actions[action], action, want, actions)
		}
	}
}
