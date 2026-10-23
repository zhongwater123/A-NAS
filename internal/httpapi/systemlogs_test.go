package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

func TestSystemLogsAreForAdministratorsAndFollowTheContract(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		SystemLogs: systemlog.NewFake(nil), Logger: logger,
	})
	router := openapitest.Router(t)
	f := &activityFixture{t: t, handler: handler, accounts: accountService, client: &contractClient{t: t, handler: handler, router: router}}
	asAdmin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	session, err := accountService.ResolveSession(ctx, asAdmin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, session.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}

	var page struct {
		Items []struct {
			Cursor  string `json:"cursor"`
			Service string `json:"service"`
			Level   string `json:"level"`
		} `json:"items"`
		Next       string `json:"next"`
		DataSource string `json:"dataSource"`
	}
	response := asAdmin.do(http.MethodGet, "/api/v1/system-logs?limit=20", "", nil, http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 20 || page.Next == "" || page.DataSource != "simulated" {
		t.Fatalf("first page: %d items, next %q, source %q", len(page.Items), page.Next, page.DataSource)
	}
	response = asAdmin.do(http.MethodGet, "/api/v1/system-logs?level=error&service=photos", "", nil, http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Level != "error" || item.Service != "photos" {
			t.Fatalf("filtered item = %+v", item)
		}
	}
	asAdmin.do(http.MethodGet, "/api/v1/system-logs?after="+page.Items[0].Cursor, "", nil, http.StatusOK)

	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	asAlice.do(http.MethodGet, "/api/v1/system-logs", "", nil, http.StatusForbidden)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/system-logs?service=ssh", nil)
	request.AddCookie(asAdmin.cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("ssh logs status = %d, want 400", recorder.Code)
	}

	withoutLogs := httpapi.NewProduct(httpapi.ProductDependencies{Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceLive, Accounts: accountService, Logger: logger})
	asAdmin.handler = withoutLogs
	asAdmin.do(http.MethodGet, "/api/v1/system-logs", "", nil, http.StatusServiceUnavailable)
}
