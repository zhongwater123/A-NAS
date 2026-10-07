package containersapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/containers"
	"github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
)

func TestSnapshotIncludesDataSourceAndUsage(t *testing.T) {
	recorder := serve(newHandler(fake.New()), http.MethodGet, "/api/v1/containers", "", nil)

	var body struct {
		DataSource string
		Engine     struct{ Version string }
		Containers []struct {
			Name  string
			State string
			Usage *struct{ CPUPercent float64 }
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, err = %v, body = %s", recorder.Code, err, recorder.Body)
	}
	if body.DataSource != "simulated" || body.Engine.Version != "29.1.3" || len(body.Containers) != 3 || body.Containers[0].Usage.CPUPercent != 12.4 {
		t.Fatalf("body = %+v", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("snapshot is cacheable")
	}
}

func TestActionChangesStateOnlyForLocalJSONRequests(t *testing.T) {
	manager := fake.New()
	handler := newHandler(manager)
	path := "/api/v1/containers/" + fake.ID("homeassistant") + "/actions"
	local := http.Header{"Content-Type": {"application/json"}, "Origin": {"http://127.0.0.1:8080"}}

	tests := []struct {
		name    string
		host    string
		headers http.Header
		body    string
		want    int
		code    string
	}{
		{name: "cross-site form", host: "127.0.0.1:8080", headers: http.Header{"Content-Type": {"text/plain"}}, body: `{"action":"start"}`, want: http.StatusForbidden, code: "forbidden"},
		{name: "foreign origin", host: "127.0.0.1:8080", headers: http.Header{"Content-Type": {"application/json"}, "Origin": {"http://attacker.example"}}, body: `{"action":"start"}`, want: http.StatusForbidden, code: "forbidden"},
		{name: "rebound host", host: "attacker.example:8080", headers: local, body: `{"action":"start"}`, want: http.StatusForbidden, code: "forbidden"},
		{name: "unknown action", host: "127.0.0.1:8080", headers: local, body: `{"action":"remove"}`, want: http.StatusBadRequest, code: "invalid_request"},
		{name: "extra fields", host: "127.0.0.1:8080", headers: local, body: `{"action":"start","privileged":true}`, want: http.StatusBadRequest, code: "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveHost(handler, http.MethodPost, path, test.host, test.body, test.headers)
			if recorder.Code != test.want || errorCode(t, recorder) != test.code {
				t.Fatalf("got %d %s, want %d %s", recorder.Code, recorder.Body, test.want, test.code)
			}
		})
	}

	recorder := serveHost(handler, http.MethodPost, path, "127.0.0.1:8080", `{"action":"start"}`, local)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("start = %d %s", recorder.Code, recorder.Body)
	}
	snapshot, _ := manager.Snapshot(t.Context())
	if snapshot.Containers[2].State != containers.StateRunning {
		t.Fatalf("homeassistant state = %s, want running", snapshot.Containers[2].State)
	}
}

func TestErrorsUseStableCodes(t *testing.T) {
	local := http.Header{"Content-Type": {"application/json"}}
	tests := []struct {
		name    string
		handler http.Handler
		method  string
		path    string
		body    string
		want    int
		code    string
	}{
		{name: "disabled", handler: containersapi.New(nil, containersapi.DataSourceLive, discard()), method: http.MethodGet, path: "/api/v1/containers", want: http.StatusServiceUnavailable, code: "containers_disabled"},
		{name: "engine offline", handler: newHandler(fake.NewUnavailable()), method: http.MethodGet, path: "/api/v1/containers", want: http.StatusServiceUnavailable, code: "containers_unavailable"},
		{name: "missing container", handler: newHandler(fake.New()), method: http.MethodGet, path: "/api/v1/containers/" + strings.Repeat("0", 64) + "/logs", want: http.StatusNotFound, code: "container_not_found"},
		{name: "name instead of ID", handler: newHandler(fake.New()), method: http.MethodPost, path: "/api/v1/containers/jellyfin/actions", body: `{"action":"stop"}`, want: http.StatusBadRequest, code: "invalid_request"},
		{name: "tail too large", handler: newHandler(fake.New()), method: http.MethodGet, path: "/api/v1/containers/" + fake.ID("jellyfin") + "/logs?tail=9999", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown subresource", handler: newHandler(fake.New()), method: http.MethodGet, path: "/api/v1/containers/" + fake.ID("jellyfin") + "/exec", want: http.StatusNotFound, code: "not_found"},
		{name: "wrong method", handler: newHandler(fake.New()), method: http.MethodDelete, path: "/api/v1/containers", want: http.StatusMethodNotAllowed, code: "method_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serve(test.handler, test.method, test.path, test.body, local)
			if recorder.Code != test.want || errorCode(t, recorder) != test.code {
				t.Fatalf("got %d %s, want %d %s", recorder.Code, recorder.Body, test.want, test.code)
			}
		})
	}
}

func TestLogsReturnBoundedLines(t *testing.T) {
	recorder := serve(newHandler(fake.New()), http.MethodGet, "/api/v1/containers/"+fake.ID("jellyfin")+"/logs?tail=2", "", nil)
	var body struct {
		Lines []struct{ Stream, Time, Text string }
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || len(body.Lines) != 2 || body.Lines[1].Stream != "stderr" {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body)
	}
}

func newHandler(manager containers.Manager) http.Handler {
	return containersapi.New(manager, containersapi.DataSourceSimulated, discard())
}

func serve(handler http.Handler, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	return serveHost(handler, method, path, "127.0.0.1:8080", body, headers)
}

func serveHost(handler http.Handler, method, path, host, body string, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = host
	for key, values := range headers {
		request.Header[key] = values
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error struct{ Code string } }
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return body.Error.Code
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
