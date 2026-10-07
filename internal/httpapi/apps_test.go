package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	fakecontainers "github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

func TestDockerAndAppCenterAreForAdministratorsWithCSRF(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		Containers: containersapi.New(fakecontainers.New(), containersapi.DataSourceSimulated, logger),
		Apps:       appAPI(t),
		Logger:     logger,
	})

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/containers", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous containers = %d", anonymous.Code)
	}
	member := signIn(t, handler, "alice", "alice password for testing")
	for _, path := range []string{"/api/v1/containers", "/api/v1/apps", "/api/v1/apps/memos/plan"} {
		if got := member.call(t, http.MethodGet, path, ""); got.Code != http.StatusForbidden {
			t.Errorf("member GET %s = %d %s, want 403", path, got.Code, got.Body)
		}
	}
	administrator := signIn(t, handler, "owner", "correct horse battery staple")
	for _, path := range []string{"/api/v1/containers", "/api/v1/apps", "/api/v1/apps/memos/plan"} {
		if got := administrator.call(t, http.MethodGet, path, ""); got.Code != http.StatusOK {
			t.Errorf("administrator GET %s = %d %s, want 200", path, got.Code, got.Body)
		}
	}
	forged := administrator
	forged.csrf = "not-the-token"
	action := "/api/v1/containers/" + fakecontainers.ID("jellyfin") + "/actions"
	if got := forged.call(t, http.MethodPost, action, `{"action":"restart"}`); got.Code != http.StatusForbidden {
		t.Fatalf("container action without the CSRF token = %d %s", got.Code, got.Body)
	}
}
