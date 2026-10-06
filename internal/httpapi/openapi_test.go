package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
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
		path       string
		reader     *fake.Reader
		wantStatus int
	}{
		{name: "health", path: "/healthz", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system", path: "/api/v1/system", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "disks", path: "/api/v1/disks", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "host state", path: "/api/v1/host-state", reader: fake.NewHealthy(), wantStatus: http.StatusOK},
		{name: "system unavailable", path: "/api/v1/system", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "disks unavailable", path: "/api/v1/disks", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
		{name: "host state unavailable", path: "/api/v1/host-state", reader: fake.NewUnavailable(), wantStatus: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+test.path, nil)
			recorder := httptest.NewRecorder()
			newHandler(test.reader).ServeHTTP(recorder, request)

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
