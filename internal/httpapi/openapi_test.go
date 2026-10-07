package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	fakeappstore "github.com/zhongwater123/A-NAS/internal/appstore/fake"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	fakecontainers "github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/terminal"
)

func TestOpenAPIContractMatchesHTTPResponses(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(ctx); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		body       string
		path       string
		reader     *fake.Reader
		handler    http.Handler
		wantStatus int
	}{
		{name: "health", path: "/healthz", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system", path: "/api/v1/system", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "disks", path: "/api/v1/disks", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "host state", path: "/api/v1/host-state", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system unavailable", path: "/api/v1/system", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "disks unavailable", path: "/api/v1/disks", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "host state unavailable", path: "/api/v1/host-state", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "metrics", path: "/api/v1/metrics", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "metrics unavailable", path: "/api/v1/metrics", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "containers", path: "/api/v1/containers", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusOK},
		{name: "containers disabled", path: "/api/v1/containers", handler: containersapi.New(nil, containersapi.DataSourceLive, nil), wantStatus: http.StatusServiceUnavailable},
		{name: "container logs", path: "/api/v1/containers/" + fakecontainers.ID("jellyfin") + "/logs?tail=2", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusOK},
		{name: "container action", method: http.MethodPost, body: `{"action":"restart"}`, path: "/api/v1/containers/" + fakecontainers.ID("jellyfin") + "/actions", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusNoContent},
		{name: "container missing", method: http.MethodPost, body: `{"action":"start"}`, path: "/api/v1/containers/" + strings.Repeat("0", 64) + "/actions", handler: containerAPI(fakecontainers.New()), wantStatus: http.StatusNotFound},
		{name: "apps", path: "/api/v1/apps", handler: appAPI(t), wantStatus: http.StatusOK},
		{name: "app plan", path: "/api/v1/apps/memos/plan", handler: appAPI(t), wantStatus: http.StatusOK},
		{name: "app install stale", method: http.MethodPost, body: `{"digest":"` + strings.Repeat("a", 64) + `"}`, path: "/api/v1/apps/memos/install", handler: appAPI(t), wantStatus: http.StatusConflict},
		{name: "app uninstall missing", method: http.MethodPost, body: `{}`, path: "/api/v1/apps/memos/uninstall", handler: appAPI(t), wantStatus: http.StatusConflict},
		{name: "apps disabled", path: "/api/v1/apps", handler: appstoreapi.New(nil, appstoreapi.DataSourceLive, appstoreapi.Options{}), wantStatus: http.StatusServiceUnavailable},
		{name: "terminal status", path: terminal.StatusPath, handler: terminal.New(terminal.Config{}, nil), wantStatus: http.StatusOK},
		{name: "terminal disabled", path: terminal.SessionPath, handler: terminal.New(terminal.Config{}, nil), wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newRequest := func() *http.Request {
				method := test.method
				if method == "" {
					method = http.MethodGet
				}
				request := httptest.NewRequest(method, "http://127.0.0.1:8080"+test.path, strings.NewReader(test.body))
				request.AddCookie(&http.Cookie{Name: "anas_session", Value: "contract-test"})
				if test.body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				return request
			}
			// The handler consumes the body, so validation gets its own request.
			request := newRequest()
			recorder := httptest.NewRecorder()
			handler := test.handler
			if handler == nil {
				handler = newHandler(test.reader)
			}
			handler.ServeHTTP(recorder, newRequest())

			if got := recorder.Code; got != test.wantStatus {
				t.Fatalf("status = %d, want %d", got, test.wantStatus)
			}
			route, pathParams, err := router.FindRoute(request)
			if err != nil {
				t.Fatalf("find OpenAPI route: %v", err)
			}
			requestInput := &openapi3filter.RequestValidationInput{
				Request:    request,
				PathParams: pathParams,
				Route:      route,
				Options: &openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error {
					return nil
				}},
			}
			if err := openapi3filter.ValidateRequest(ctx, requestInput); err != nil {
				t.Fatalf("validate request: %v", err)
			}
			responseInput := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: requestInput,
				Status:                 recorder.Code,
				Header:                 recorder.Header(),
			}
			responseInput.SetBodyBytes(recorder.Body.Bytes())
			if err := openapi3filter.ValidateResponse(ctx, responseInput); err != nil {
				t.Fatalf("validate response: %v", err)
			}
		})
	}
}

func containerAPI(manager *fakecontainers.Manager) http.Handler {
	return containersapi.New(manager, containersapi.DataSourceSimulated, nil)
}

func appAPI(t *testing.T) http.Handler {
	store, err := fakeappstore.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return appstoreapi.New(store, appstoreapi.DataSourceSimulated, appstoreapi.Options{Host: appstoreapi.DevelopmentHost{}})
}
