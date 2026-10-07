package appstoreapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
	"github.com/zhongwater123/A-NAS/internal/appstore/fake"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
)

func newStore(t *testing.T) *engine.Store {
	t.Helper()
	store, err := fake.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestListPlanInstallFlow(t *testing.T) {
	store := newStore(t)
	host := &recordingHost{}
	handler := appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: host, Logger: discard()})

	var list struct {
		DataSource string
		Apps       []struct{ ID, Title, State string }
	}
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps", "", nil), http.StatusOK, &list)
	if list.DataSource != "simulated" || len(list.Apps) < 20 || list.Apps[0].State != "available" {
		t.Fatalf("list = %+v", list)
	}

	var plan struct {
		Digest   string
		Identity struct {
			Username string
			UID      int
		}
		Ports  []struct{ HostPort int }
		Mounts []struct{ HostPath, Kind string }
	}
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps/navidrome/plan", "", nil), http.StatusOK, &plan)
	if len(plan.Digest) != 64 || plan.Ports[0].HostPort != 4533 || plan.Mounts[0].HostPath != "/srv/a-nas/data/apps/navidrome/data" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Identity.Username != "app-navidrome" || plan.Identity.UID != 30042 {
		t.Fatalf("plan identity = %+v", plan.Identity)
	}

	local := http.Header{"Content-Type": {"application/json"}, "Origin": {"http://127.0.0.1:8080"}}
	var job struct{ State, Action string }
	decode(t, serve(handler, http.MethodPost, "/api/v1/apps/navidrome/install", `{"digest":"`+plan.Digest+`"}`, local), http.StatusAccepted, &job)
	if job.State != "running" || job.Action != "install" {
		t.Fatalf("job = %+v", job)
	}
	if len(host.prepared) != 1 || host.prepared[0].id != "navidrome" || !host.prepared[0].shared ||
		!slices.Contains(host.prepared[0].folders, "/srv/a-nas/data/apps/navidrome/data") {
		t.Fatalf("host preparation = %+v", host.prepared)
	}
	store.Wait()
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps", "", nil), http.StatusOK, &list)
	for _, app := range list.Apps {
		if app.ID == "navidrome" && app.State != "installed" {
			t.Fatalf("navidrome state = %s", app.State)
		}
	}
	decode(t, serve(handler, http.MethodPost, "/api/v1/apps/navidrome/uninstall", `{}`, local), http.StatusAccepted, &job)
	store.Wait()
	if !slices.Equal(host.released, []string{"navidrome"}) {
		t.Fatalf("released = %v, want Shared access withdrawn on uninstall", host.released)
	}
}

func TestInstallIsRefusedWhileTheDataVolumeIsOffline(t *testing.T) {
	host := &recordingHost{}
	handler := appstoreapi.New(newStore(t), appstoreapi.DataSourceSimulated, appstoreapi.Options{
		Host: host, Logger: discard(),
		VolumeReady: func(context.Context) error { return errors.New("offline") },
	})
	local := http.Header{"Content-Type": {"application/json"}}
	var plan struct{ Digest string }
	decode(t, serve(handler, http.MethodGet, "/api/v1/apps/memos/plan", "", nil), http.StatusOK, &plan)
	recorder := serve(handler, http.MethodPost, "/api/v1/apps/memos/install", `{"digest":"`+plan.Digest+`"}`, local)
	if recorder.Code != http.StatusLocked || !strings.Contains(recorder.Body.String(), "volume_unavailable") {
		t.Fatalf("install while offline = %d %s", recorder.Code, recorder.Body)
	}
	if len(host.prepared) != 0 {
		t.Fatalf("an offline install prepared host folders: %+v", host.prepared)
	}
}

type preparation struct {
	id      string
	folders []string
	shared  bool
}

type recordingHost struct {
	appstoreapi.DevelopmentHost
	prepared []preparation
	released []string
}

func (h *recordingHost) AppIdentity(_ context.Context, id string) (appid.Identity, error) {
	return appid.Identity{Username: appid.Username(id), UID: 30042, GID: 30042}, nil
}

func (h *recordingHost) PrepareApp(_ context.Context, id string, folders []string, shared bool) error {
	h.prepared = append(h.prepared, preparation{id: id, folders: folders, shared: shared})
	return nil
}

func (h *recordingHost) ReleaseApp(_ context.Context, id string) error {
	h.released = append(h.released, id)
	return nil
}

func TestWritesAreGuardedAndErrorsUseStableCodes(t *testing.T) {
	handler := appstoreapi.New(newStore(t), appstoreapi.DataSourceSimulated, options())
	local := http.Header{"Content-Type": {"application/json"}}
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		handler http.Handler
		method  string
		path    string
		body    string
		headers http.Header
		want    int
		code    string
	}{
		{name: "cross-site form", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/install", body: `{"digest":"` + digest + `"}`, headers: http.Header{"Content-Type": {"text/plain"}}, want: http.StatusForbidden, code: "forbidden"},
		{name: "foreign origin", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/install", body: `{"digest":"` + digest + `"}`, headers: http.Header{"Content-Type": {"application/json"}, "Origin": {"http://evil.example"}}, want: http.StatusForbidden, code: "forbidden"},
		{name: "missing digest", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/install", body: `{}`, headers: local, want: http.StatusBadRequest, code: "invalid_request"},
		{name: "compose smuggling", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/install", body: `{"digest":"` + digest + `","compose":"services: {}"}`, headers: local, want: http.StatusBadRequest, code: "invalid_request"},
		{name: "stale plan", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/install", body: `{"digest":"` + digest + `"}`, headers: local, want: http.StatusConflict, code: "plan_changed"},
		{name: "not installed", handler: handler, method: http.MethodPost, path: "/api/v1/apps/memos/uninstall", body: `{}`, headers: local, want: http.StatusConflict, code: "not_installed"},
		{name: "unknown app", handler: handler, method: http.MethodGet, path: "/api/v1/apps/unknown/plan", want: http.StatusNotFound, code: "app_not_found"},
		{name: "path traversal", handler: handler, method: http.MethodGet, path: "/api/v1/apps/../plan", want: http.StatusNotFound, code: "not_found"},
		{name: "disabled", handler: appstoreapi.New(nil, appstoreapi.DataSourceLive, options()), method: http.MethodGet, path: "/api/v1/apps", want: http.StatusServiceUnavailable, code: "apps_disabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serve(test.handler, test.method, test.path, test.body, test.headers)
			var body struct{ Error struct{ Code string } }
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if recorder.Code != test.want || body.Error.Code != test.code {
				t.Fatalf("got %d %s, want %d %s", recorder.Code, recorder.Body, test.want, test.code)
			}
		})
	}
}

func TestIconsAreCacheableAndSandboxed(t *testing.T) {
	recorder := serve(appstoreapi.New(newStore(t), appstoreapi.DataSourceSimulated, options()), http.MethodGet, "/api/v1/apps/memos/icon", "", nil)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon = %d %s", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "sandbox") || !strings.Contains(recorder.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("icon headers = %v", recorder.Header())
	}
}

func serve(handler http.Handler, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "127.0.0.1:8080"
	for key, values := range headers {
		request.Header[key] = values
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, status int, into any) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), into); err != nil {
		t.Fatal(err)
	}
}

func options() appstoreapi.Options {
	return appstoreapi.Options{Host: appstoreapi.DevelopmentHost{}, Logger: discard()}
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
