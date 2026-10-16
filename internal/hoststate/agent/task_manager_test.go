package agent_test

import (
	"context"
	"encoding/json"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func TestTaskManagerClientRoundTrip(t *testing.T) {
	client := agent.NewClient(serve(t, agent.NewHandler(fake.NewHealthy(), discardLogger())))
	reader, ok := any(client).(taskmanager.Reader)
	if !ok {
		t.Fatal("Host Agent client must implement taskmanager.Reader")
	}
	device, err := reader.ReadDeviceStatus(context.Background())
	if err != nil || device.CPU.LogicalCores < 1 {
		t.Fatalf("device: %+v, %v", device, err)
	}
	tasks, err := reader.ReadTasks(context.Background())
	if err != nil || len(tasks.Processes.Items) == 0 {
		t.Fatalf("tasks: %+v, %v", tasks, err)
	}
}

func TestTaskManagerIPCRejectsWritesAndReportsUnavailable(t *testing.T) {
	for _, path := range []string{"/v1/task-manager/device", "/v1/task-manager/tasks"} {
		handler := agent.NewHandler(fake.NewUnavailable(), discardLogger())
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := httptest.NewRecorder()
			handler.ServeHTTP(r, httptest.NewRequest(method, path, nil))
			want := http.StatusServiceUnavailable
			if method == http.MethodPost {
				want = http.StatusMethodNotAllowed
			}
			if r.Code != want {
				t.Fatalf("%s %s = %d, want %d", method, path, r.Code, want)
			}
		}
	}
}

func TestTaskManagerClientRejectsInvalidAndSensitivePayloads(t *testing.T) {
	device, err := fake.NewHealthy().ReadDeviceStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := fake.NewHealthy().ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deviceJSON, _ := json.Marshal(device)
	taskJSON, _ := json.Marshal(tasks)
	tests := []struct{ name, path, body string }{
		{"empty", "/v1/task-manager/device", "{}"},
		{"invalid percent", "/v1/task-manager/device", strings.Replace(string(deviceJSON), `"usagePercent":23.5`, `"usagePercent":101`, 1)},
		{"negative rate", "/v1/task-manager/tasks", strings.Replace(string(taskJSON), `"readBytesPerSecond":524288`, `"readBytesPerSecond":-1`, 1)},
		{"unexpected command line", "/v1/task-manager/tasks", strings.Replace(string(taskJSON), `"name":"anas-api"`, `"name":"anas-api","cmdline":"secret"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			})))
			if test.path == "/v1/task-manager/device" {
				_, err = client.ReadDeviceStatus(context.Background())
			} else {
				_, err = client.ReadTasks(context.Background())
			}
			if err == nil {
				t.Fatal("invalid IPC response accepted")
			}
		})
	}
}

func TestTaskManagerOldObserverIsExplicitlyUnavailable(t *testing.T) {
	// Older/custom adapters may implement state and metrics only.
	observer := struct{ hoststate.Observer }{fake.NewHealthy()}
	handler := agent.NewHandler(observer, discardLogger())
	for _, path := range []string{"/v1/task-manager/device", "/v1/task-manager/tasks"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("old observer returned %d", recorder.Code)
		}
	}
}

type iconTaskReader struct{ *fake.Reader }

func (r iconTaskReader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	s, err := r.Reader.ReadTasks(ctx)
	id, src := taskmanager.NormalizeIcon([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="M0 0h16v16z" fill="#369"/></svg>`))
	s.Icons = map[string]string{id: src}
	s.Processes.Items[0].IconID = id
	return s, err
}
func TestTaskIconsSurviveIPCAndRejectRemoteImages(t *testing.T) {
	source := iconTaskReader{fake.NewHealthy()}
	expected, _ := source.ReadTasks(context.Background())
	client := agent.NewClient(serve(t, agent.NewHandler(source, discardLogger())))
	actual, err := client.ReadTasks(context.Background())
	id := expected.Processes.Items[0].IconID
	if err != nil || id == "" || actual.Icons[id] != expected.Icons[id] || actual.Processes.Items[0].IconID != id {
		t.Fatalf("icon round trip: %v", err)
	}
	expected.Icons[id] = "https://example.invalid/icon.png"
	bad := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(expected) })))
	if _, err := bad.ReadTasks(context.Background()); err == nil {
		t.Fatal("remote icon accepted")
	}
}
