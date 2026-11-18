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
	// The header that says a planning grant was registered reaches the
	// montage center only when the Product Service sets it.
	var forwarded []string
	api := montageapi.New(montageStore, logger)
	recorded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.Header.Get(montageapi.AssistantHeader))
		api.ServeHTTP(w, r)
	})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		Montage: recorded, Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	response := owner.do(http.MethodGet, "/api/v1/montage/projects", "", map[string]string{montageapi.AssistantHeader: "on"}, http.StatusOK)
	if body := strings.TrimSpace(response.Body.String()); body != `{"items":[]}` {
		t.Fatalf("projects = %s", body)
	}
	if len(forwarded) != 1 || forwarded[0] != "" {
		t.Fatalf("the montage center saw %s = %q", montageapi.AssistantHeader, forwarded)
	}
	anonymous := &contractClient{t: t, handler: handler}
	anonymous.do(http.MethodGet, "/api/v1/montage/projects", "", nil, http.StatusUnauthorized)

	// Switched off, the API is not there at all.
	off := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Logger: logger,
	})
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/v1/montage/projects", nil)
	request.AddCookie(owner.cookie)
	request.Header.Set("X-Forwarded-For", owner.forwardedFor)
	recorder := httptest.NewRecorder()
	off.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("switched off: %d %s", recorder.Code, recorder.Body)
	}
}
