package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
)

// TestMontageCenterKeepsToTheContract drives 剪辑中心 through the Product
// Service as development runs it, with its switch off and on.
func TestMontageCenterKeepsToTheContract(t *testing.T) {
	dir := t.TempDir()
	store, err := accounts.OpenSQLite(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := os.Mkdir(filepath.Join(dir, "montage"), 0o700); err != nil {
		t.Fatal(err)
	}
	montageStore, err := montage.Open(t.Context(), filepath.Join(dir, "montage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = montageStore.Close() })
	// Whether the switch is on is the Product Service's answer alone.
	served := 0
	api := montageapi.New(montageStore, logger)
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		api.ServeHTTP(w, r)
	})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		Montage: counted, Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodGet, "/api/v1/montage", "", nil, http.StatusNoContent)
	if served != 0 {
		t.Fatal("the montage center was asked whether it is on")
	}
	response := owner.do(http.MethodGet, "/api/v1/montage/projects", "", nil, http.StatusOK)
	if body := strings.TrimSpace(response.Body.String()); body != `{"items":[]}` {
		t.Fatalf("projects = %s", body)
	}
	anonymous := &contractClient{t: t, handler: handler}
	anonymous.do(http.MethodGet, "/api/v1/montage", "", nil, http.StatusUnauthorized)
	anonymous.do(http.MethodGet, "/api/v1/montage/projects", "", nil, http.StatusUnauthorized)

	// Switched off, the API is not there at all.
	off := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Logger: logger,
	})
	for _, path := range []string{"/api/v1/montage", "/api/v1/montage/projects"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		request.AddCookie(owner.cookie)
		request.Header.Set("X-Forwarded-For", owner.forwardedFor)
		recorder := httptest.NewRecorder()
		off.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("switched off, %s: %d %s", path, recorder.Code, recorder.Body)
		}
	}
}
