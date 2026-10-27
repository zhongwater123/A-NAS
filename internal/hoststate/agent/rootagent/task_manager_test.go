package rootagent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/rootagent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

func TestTaskManagerClientRoundTrip(t *testing.T) {
	client := agent.NewClient(serve(t, rootagent.NewHandler(fake.NewHealthy(), discardLogger())))
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
		handler := rootagent.NewHandler(fake.NewUnavailable(), discardLogger())
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
	handler := rootagent.NewHandler(observer, discardLogger())
	for _, path := range []string{"/v1/task-manager/device", "/v1/task-manager/tasks"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("old observer returned %d", recorder.Code)
		}
	}
}

type iconTaskReader struct{ *fake.Reader }

// testIconFile is an installed icon the Host Agent names; the client reads
// it only through its loader, which the test cannot reach, so the icon is
// dropped on the way (taskicon's tests cover decoding).
const testIconFile = "/usr/share/icons/hicolor/scalable/apps/a-nas-test-missing.svg"

func (r iconTaskReader) ReadTasks(ctx context.Context) (taskmanager.TaskSnapshot, error) {
	s, err := r.Reader.ReadTasks(ctx)
	id := strings.Repeat("a", 64)
	s.IconFiles = map[string][]string{id: {testIconFile}}
	s.Processes.Items[0].IconID = id
	return s, err
}

// The Host Agent names icon files and decodes none; the client accepts only
// installed files, and a snapshot carrying images of its own.
func TestTaskIconsCrossIPCAsFilesAndRejectOthers(t *testing.T) {
	source := iconTaskReader{fake.NewHealthy()}
	expected, _ := source.ReadTasks(context.Background())
	client := agent.NewClient(serve(t, rootagent.NewHandler(source, discardLogger())))
	actual, err := client.ReadTasks(context.Background())
	if err != nil || actual.IconFiles != nil || actual.Processes.Items[0].IconID != "" {
		t.Fatalf("icon files after the client = %v, %v, %q", err, actual.IconFiles, actual.Processes.Items[0].IconID)
	}
	id := expected.Processes.Items[0].IconID
	for name, snapshot := range map[string]func(taskmanager.TaskSnapshot) taskmanager.TaskSnapshot{
		"outside": func(s taskmanager.TaskSnapshot) taskmanager.TaskSnapshot {
			s.IconFiles = map[string][]string{id: {"/home/member/private.svg"}}
			return s
		},
		"traversal": func(s taskmanager.TaskSnapshot) taskmanager.TaskSnapshot {
			s.IconFiles = map[string][]string{id: {"/usr/share/../../etc/shadow"}}
			return s
		},
		"image": func(s taskmanager.TaskSnapshot) taskmanager.TaskSnapshot {
			s.IconFiles = nil
			s.Icons = map[string]string{id: "https://example.invalid/icon.png"}
			return s
		},
	} {
		bad := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(snapshot(expected))
		})))
		if _, err := bad.ReadTasks(context.Background()); err == nil {
			t.Fatalf("%s icon accepted", name)
		}
	}
}
