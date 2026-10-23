package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

// contractChecked validates every exchange on a snapshot, version or removed
// item path against the OpenAPI document: responses always, requests when
// the server accepted them.
func contractChecked(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	ctx := context.Background()
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		if strings.Contains(r.URL.Path, "snapshot") || strings.HasSuffix(r.URL.Path, "/versions") || strings.HasSuffix(r.URL.Path, "/removed-entries") {
			request := httptest.NewRequest(r.Method, "http://127.0.0.1:8080"+r.URL.RequestURI(), bytes.NewReader(body))
			request.Header = r.Header.Clone()
			route, params, err := router.FindRoute(request)
			if err != nil {
				t.Errorf("find OpenAPI route for %s %s: %v", r.Method, r.URL.Path, err)
			} else {
				input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{
					AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil },
				}}
				if recorder.Code < 300 {
					if err := openapi3filter.ValidateRequest(ctx, input); err != nil {
						t.Errorf("validate request %s %s: %v", r.Method, r.URL.Path, err)
					}
				}
				response := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: recorder.Code, Header: recorder.Header()}
				response.SetBodyBytes(recorder.Body.Bytes())
				if err := openapi3filter.ValidateResponse(ctx, response); err != nil {
					t.Errorf("validate response %s %s: %v", r.Method, r.URL.Path, err)
				}
			}
		}
		maps.Copy(w.Header(), recorder.Header())
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
}

func TestBackupCenterAPIManagesSchedulesSearchesAndRestoresFolders(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	var mu sync.Mutex
	now := time.Date(2026, 10, 12, 10, 30, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, Now: clock, Location: time.UTC,
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := contractChecked(t, httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		Accounts: accountService, Files: fileService, Logger: logger,
	}))

	admin := &apiClient{t: t, handler: handler}
	admin.call(http.MethodPost, "/api/v1/setup/admin", map[string]string{"username": "owner", "password": "correct horse battery staple"}, http.StatusCreated, nil)
	var alice accounts.User
	admin.call(http.MethodPost, "/api/v1/users", map[string]string{"username": "alice", "password": "alice password for testing"}, http.StatusCreated, &alice)
	member := &apiClient{t: t, handler: handler}
	member.call(http.MethodPost, "/api/v1/session", map[string]string{"username": "alice", "password": "alice password for testing"}, http.StatusOK, nil)
	adminSession, err := accountService.ResolveSession(ctx, admin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	var spaces struct {
		Items []accounts.Space `json:"items"`
	}
	admin.call(http.MethodGet, "/api/v1/spaces", nil, http.StatusOK, &spaces)
	var private, shared accounts.Space
	for _, space := range spaces.Items {
		if space.Kind == accounts.SpaceKindShared {
			shared = space
		} else {
			private = space
		}
	}
	member.call(http.MethodGet, "/api/v1/spaces", nil, http.StatusOK, &spaces)
	var alicePrivate accounts.Space
	for _, space := range spaces.Items {
		if space.Kind == accounts.SpaceKindPrivate {
			alicePrivate = space
		}
	}
	folder, err := fileService.CreateDirectory(ctx, adminSession.User, private.ID, "", "Docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileService.Upload(ctx, adminSession.User, private.ID, folder.ID, "plan.txt", strings.NewReader("the plan")); err != nil {
		t.Fatal(err)
	}

	var snapshot files.Snapshot
	admin.call(http.MethodPost, "/api/v1/spaces/"+private.ID+"/snapshots", map[string]string{"name": "before"}, http.StatusCreated, &snapshot)
	if snapshot.Origin != files.SnapshotOriginManual || snapshot.FileCount != 1 || snapshot.SizeBytes != int64(len("the plan")) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	var root, inside struct {
		Items []files.SnapshotEntry `json:"items"`
	}
	admin.call(http.MethodGet, "/api/v1/snapshots/"+snapshot.ID+"/entries", nil, http.StatusOK, &root)
	admin.call(http.MethodGet, "/api/v1/snapshots/"+snapshot.ID+"/entries?parentId="+url.QueryEscape(root.Items[0].ID), nil, http.StatusOK, &inside)
	if len(inside.Items) != 1 || inside.Items[0].ModifiedAt.IsZero() {
		t.Fatalf("folder entries = %+v", inside.Items)
	}
	download := httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/"+snapshot.ID+"/entries/"+inside.Items[0].ID+"/content", nil)
	download.AddCookie(admin.cookie)
	downloaded := httptest.NewRecorder()
	handler.ServeHTTP(downloaded, download)
	if downloaded.Code != http.StatusOK || downloaded.Body.String() != "the plan" || !strings.Contains(downloaded.Header().Get("Content-Disposition"), "plan.txt") {
		t.Fatalf("download = %d %q %v", downloaded.Code, downloaded.Body.String(), downloaded.Header())
	}
	admin.call(http.MethodGet, "/api/v1/snapshots/"+snapshot.ID+"/entries/"+root.Items[0].ID+"/content", nil, http.StatusUnprocessableEntity, nil)

	var found struct {
		Items     []files.SnapshotMatch `json:"items"`
		Truncated bool                  `json:"truncated"`
	}
	admin.call(http.MethodGet, "/api/v1/spaces/"+private.ID+"/snapshot-search?q=plan", nil, http.StatusOK, &found)
	if len(found.Items) != 1 || found.Items[0].Path != "Docs/plan.txt" || !found.Items[0].InSpace {
		t.Fatalf("search = %+v", found)
	}
	admin.call(http.MethodGet, "/api/v1/spaces/"+private.ID+"/snapshot-search?q="+url.QueryEscape("   "), nil, http.StatusUnprocessableEntity, nil)

	if _, err := fileService.DeleteByID(ctx, adminSession.User, folder.ID); err != nil {
		t.Fatal(err)
	}
	admin.call(http.MethodGet, "/api/v1/snapshots/"+snapshot.ID+"/entries", nil, http.StatusOK, &root)
	if root.Items[0].Change != files.ChangeRemoved {
		t.Fatalf("deleted folder in the snapshot = %+v", root.Items[0])
	}
	var removed struct {
		Items     []files.RemovedEntry `json:"items"`
		Truncated bool                 `json:"truncated"`
	}
	admin.call(http.MethodGet, "/api/v1/spaces/"+private.ID+"/removed-entries", nil, http.StatusOK, &removed)
	if len(removed.Items) != 1 || removed.Items[0].Path != "Docs" || removed.Items[0].FileCount != 1 || removed.Truncated {
		t.Fatalf("removed entries = %+v", removed)
	}
	member.call(http.MethodGet, "/api/v1/spaces/"+private.ID+"/removed-entries", nil, http.StatusForbidden, nil)
	var restored files.Entry
	admin.call(http.MethodPost, "/api/v1/snapshots/"+snapshot.ID+"/entries/"+root.Items[0].ID+"/restore", map[string]string{"name": "Docs"}, http.StatusCreated, &restored)
	if restored.Kind != files.EntryKindDirectory {
		t.Fatalf("restored = %+v", restored)
	}
	admin.call(http.MethodPost, "/api/v1/snapshots/"+snapshot.ID+"/entries/"+root.Items[0].ID+"/restore", map[string]string{"name": "Docs"}, http.StatusConflict, nil)

	// The restored plan has the snapshot's version and can be put back over itself.
	restoredFiles, err := fileService.List(ctx, adminSession.User, private.ID, restored.ID)
	if err != nil || len(restoredFiles) != 1 {
		t.Fatalf("restored folder = %+v, %v", restoredFiles, err)
	}
	var versions files.SnapshotMatch
	admin.call(http.MethodGet, "/api/v1/files/"+restoredFiles[0].ID+"/versions", nil, http.StatusOK, &versions)
	if versions.Path != "Docs/plan.txt" || len(versions.Versions) != 1 || versions.Versions[0].SnapshotID != snapshot.ID {
		t.Fatalf("versions = %+v", versions)
	}
	member.call(http.MethodGet, "/api/v1/files/"+restoredFiles[0].ID+"/versions", nil, http.StatusForbidden, nil)
	var replaced files.Entry
	admin.call(http.MethodPost, "/api/v1/snapshots/"+snapshot.ID+"/entries/"+inside.Items[0].ID+"/restore",
		map[string]any{"parentId": restored.ID, "name": "plan.txt", "replace": true}, http.StatusCreated, &replaced)
	if replaced.Name != "plan.txt" || replaced.ID == restoredFiles[0].ID {
		t.Fatalf("replaced = %+v", replaced)
	}
	var whole files.Entry
	admin.call(http.MethodPost, "/api/v1/snapshots/"+snapshot.ID+"/restore", map[string]string{"name": "before 的全部文件"}, http.StatusCreated, &whole)
	if whole.Kind != files.EntryKindDirectory || whole.Name != "before 的全部文件" {
		t.Fatalf("whole snapshot restored = %+v", whole)
	}
	admin.call(http.MethodPost, "/api/v1/snapshots/"+snapshot.ID+"/restore", map[string]string{"name": "before 的全部文件"}, http.StatusConflict, nil)

	schedulePath := "/api/v1/spaces/" + private.ID + "/snapshot-schedule"
	var schedule files.SnapshotSchedule
	admin.call(http.MethodGet, schedulePath, nil, http.StatusOK, &schedule)
	if !schedule.Enabled || !schedule.CanManage || schedule.Keep != 14 || schedule.Time != "03:00" {
		t.Fatalf("default schedule = %+v", schedule)
	}
	tiered := map[string]any{"enabled": true, "frequency": "daily", "time": "03:00", "weekday": 0, "keep": 14,
		"retention": "tiered", "tiers": map[string]int{"hourly": 0, "daily": 7, "weekly": 4, "monthly": 6}}
	admin.call(http.MethodPut, schedulePath, tiered, http.StatusOK, &schedule)
	if schedule.Retention != files.RetentionTiered || schedule.Tiers != (files.RetentionTiers{Daily: 7, Weekly: 4, Monthly: 6}) {
		t.Fatalf("tiered schedule = %+v", schedule)
	}
	tiered["tiers"] = map[string]int{"hourly": 24, "daily": 7, "weekly": 4, "monthly": 6}
	admin.call(http.MethodPut, schedulePath, tiered, http.StatusUnprocessableEntity, nil)
	hourly := map[string]any{"enabled": true, "frequency": "hourly", "time": "00:00", "weekday": 0, "keep": 24}
	admin.call(http.MethodPut, schedulePath, hourly, http.StatusOK, &schedule)
	if !schedule.NextRunAt.Equal(time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("hourly schedule = %+v", schedule)
	}
	admin.call(http.MethodPut, schedulePath, map[string]any{"enabled": true, "frequency": "monthly", "time": "00:00", "weekday": 0, "keep": 24}, http.StatusUnprocessableEntity, nil)
	member.call(http.MethodPut, "/api/v1/spaces/"+shared.ID+"/snapshot-schedule", hourly, http.StatusForbidden, nil)
	member.call(http.MethodGet, "/api/v1/spaces/"+shared.ID+"/snapshot-schedule", nil, http.StatusOK, &schedule)
	if schedule.CanManage {
		t.Fatal("a member may manage the shared schedule")
	}
	member.call(http.MethodPost, "/api/v1/spaces/"+shared.ID+"/snapshots", map[string]string{"name": "mine"}, http.StatusForbidden, nil)

	mu.Lock()
	now = time.Date(2026, 10, 12, 11, 1, 0, 0, time.UTC)
	mu.Unlock()
	if err := fileService.RunDueSnapshots(ctx, accountService, logger); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []files.Snapshot `json:"items"`
	}
	admin.call(http.MethodGet, "/api/v1/spaces/"+private.ID+"/snapshots", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].Origin != files.SnapshotOriginScheduled || list.Items[0].CreatedBy != "system" {
		t.Fatalf("snapshots after the schedule ran = %+v", list.Items)
	}
	var kept files.Snapshot
	admin.call(http.MethodPost, "/api/v1/snapshots/"+list.Items[0].ID+"/keep", nil, http.StatusOK, &kept)
	if kept.Origin != files.SnapshotOriginManual {
		t.Fatalf("kept = %+v", kept)
	}

	// Administrative Viewing Mode shows a member's space read-only: its
	// snapshots can be browsed but not taken, deleted or scheduled.
	admin.call(http.MethodPost, "/api/v1/users/"+alice.ID+"/viewing",
		map[string]string{"password": "correct horse battery staple", "reason": "help alice find a file", "scope": "space"}, http.StatusCreated, nil)
	admin.call(http.MethodGet, "/api/v1/spaces/"+alicePrivate.ID+"/snapshots", nil, http.StatusOK, nil)
	admin.call(http.MethodPost, "/api/v1/spaces/"+alicePrivate.ID+"/snapshots", map[string]string{"name": "peek"}, http.StatusForbidden, nil)
	admin.call(http.MethodPut, "/api/v1/spaces/"+alicePrivate.ID+"/snapshot-schedule", hourly, http.StatusForbidden, nil)
	admin.call(http.MethodGet, "/api/v1/spaces/"+alicePrivate.ID+"/snapshot-schedule", nil, http.StatusOK, &schedule)
	if schedule.CanManage {
		t.Fatal("a viewing administrator may manage the member's schedule")
	}
}
