package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func TestTaskManagerIsReadOnlyAndAvailableToEverySignedInAccount(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "owner password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, admin, "member", "member password for testing"); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewProduct(httpapi.ProductDependencies{Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: service})
	paths := []string{"/api/v1/task-manager/device", "/api/v1/task-manager/tasks"}
	for _, path := range paths {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s = %d, want 401", path, r.Code)
		}
	}
	for _, name := range []string{"owner", "member"} {
		session, err := service.Authenticate(ctx, name, name+" password for testing")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			r := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(&http.Cookie{Name: "anas_session", Value: session.Token})
			handler.ServeHTTP(r, request)
			if r.Code != http.StatusOK {
				t.Fatalf("%s %s = %d: %s", name, path, r.Code, r.Body.String())
			}
			if r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("task-manager readings must not be cached")
			}
			var source struct {
				DataSource string `json:"dataSource"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &source); err != nil || source.DataSource != "simulated" {
				t.Fatalf("data source: %+v, %v", source, err)
			}
			if path == paths[0] {
				var device taskmanager.DeviceSnapshot
				if err := json.Unmarshal(r.Body.Bytes(), &device); err != nil || device.CPU.LogicalCores < 1 {
					t.Fatalf("device: %+v, %v", device, err)
				}
			} else {
				var tasks taskmanager.TaskSnapshot
				if err := json.Unmarshal(r.Body.Bytes(), &tasks); err != nil || len(tasks.Processes.Items) == 0 || len(tasks.Services.Items) == 0 {
					t.Fatalf("tasks: %+v, %v", tasks, err)
				}
			}
			post := httptest.NewRequest(http.MethodPost, path, nil)
			post.AddCookie(&http.Cookie{Name: "anas_session", Value: session.Token})
			r = httptest.NewRecorder()
			handler.ServeHTTP(r, post)
			if r.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s = %d, want 405", path, r.Code)
			}
		}
	}
}

func TestTaskManagerUnavailable(t *testing.T) {
	for _, path := range []string{"/api/v1/task-manager/device", "/api/v1/task-manager/tasks"} {
		r := httptest.NewRecorder()
		newHandler(fake.NewUnavailable()).ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d, want 503", path, r.Code)
		}
	}
}
