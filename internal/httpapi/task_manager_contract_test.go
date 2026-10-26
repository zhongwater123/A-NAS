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
	"github.com/zhongwater123/A-NAS/internal/openapitest"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func TestTaskManagerAuthenticatedResponseContract(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
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
	client := &contractClient{t: t, handler: httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: iconContractReader{fake.NewHealthy()}, DataSource: httpapi.DataSourceSimulated, Accounts: service,
	})}
	paths := []string{"/api/v1/task-manager/device", "/api/v1/task-manager/tasks"}
	for _, path := range paths {
		client.do(http.MethodGet, path, "", nil, http.StatusUnauthorized)
	}
	for _, name := range []string{"owner", "member"} {
		session, err := service.Authenticate(signedInDirectly(ctx), name, name+" password for testing")
		if err != nil {
			t.Fatal(err)
		}
		client.cookie = &http.Cookie{Name: "anas_session", Value: session.Token}
		for _, path := range paths {
			client.do(http.MethodGet, path, "", nil, http.StatusOK)
		}
	}
}

// The contract must distinguish unknown metrics from zero and reject accidental
// host metadata exposure, even when a valid response contains no such fields.
func TestTaskManagerSchemaPreservesUnknownsAndRejectsHostMetadata(t *testing.T) {
	document := openapitest.Document(t)
	tests := []struct {
		name     string
		endpoint string
		schema   string
		mutate   func(map[string]any)
		valid    bool
	}{
		{"unknown accelerator metrics", "device", "TaskManagerDeviceResponse", func(map[string]any) {}, true},
		{"unknown CPU usage", "device", "TaskManagerDeviceResponse", func(v map[string]any) { v["cpu"].(map[string]any)["usagePercent"] = nil }, true},
		{"missing nullable metric", "device", "TaskManagerDeviceResponse", func(v map[string]any) { delete(firstTaskManagerItem(v, "gpu"), "usagePercent") }, false},
		{"null array", "device", "TaskManagerDeviceResponse", func(v map[string]any) { v["npu"].(map[string]any)["items"] = nil }, false},
		{"invalid availability", "device", "TaskManagerDeviceResponse", func(v map[string]any) { v["gpu"].(map[string]any)["status"] = "healthy" }, false},
		{"host device path", "device", "TaskManagerDeviceResponse", func(v map[string]any) { firstTaskManagerItem(v, "disks")["path"] = "/dev/sda" }, false},
		{"zero sample window", "device", "TaskManagerDeviceResponse", func(v map[string]any) { v["sampleSeconds"] = float64(0) }, false},
		{"unknown task metrics", "tasks", "TaskManagerTasksResponse", func(v map[string]any) {
			item := firstTaskManagerItem(v, "processes")
			for _, key := range []string{"cpuPercent", "memoryBytes", "readBytesPerSecond", "writeBytesPerSecond"} {
				item[key] = nil
			}
		}, true},
		{"missing task metric", "tasks", "TaskManagerTasksResponse", func(v map[string]any) { delete(firstTaskManagerItem(v, "processes"), "cpuPercent") }, false},
		{"negative I/O rate", "tasks", "TaskManagerTasksResponse", func(v map[string]any) { firstTaskManagerItem(v, "processes")["readBytesPerSecond"] = float64(-1) }, false},
		{"per-core CPU percentage", "tasks", "TaskManagerTasksResponse", func(v map[string]any) { firstTaskManagerItem(v, "processes")["cpuPercent"] = float64(101) }, false},
		{"process command line", "tasks", "TaskManagerTasksResponse", func(v map[string]any) { firstTaskManagerItem(v, "processes")["commandLine"] = "secret argument" }, false},
		{"service cgroup path", "tasks", "TaskManagerTasksResponse", func(v map[string]any) { firstTaskManagerItem(v, "services")["cgroupPath"] = "/system.slice/service" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			newHandler(fake.NewHealthy()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/task-manager/"+test.endpoint, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			test.mutate(body)
			err := document.Components.Schemas[test.schema].Value.VisitJSON(body)
			if (err == nil) != test.valid {
				t.Fatalf("schema validation error = %v; want valid = %t", err, test.valid)
			}
		})
	}
}

func firstTaskManagerItem(body map[string]any, group string) map[string]any {
	return body[group].(map[string]any)["items"].([]any)[0].(map[string]any)
}

type iconContractReader struct{ *fake.Reader }

func (r iconContractReader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	s, err := r.Reader.ReadTasks(ctx)
	id, src := taskmanager.NormalizeIcon([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" fill="#369"/></svg>`))
	s.Icons = map[string]string{id: src}
	s.Processes.Items[0].IconID = id
	s.Services.Items[0].IconID = id
	return s, err
}
