// Package openapitest gives contract tests the product API contract. They
// load the bundle that the Web client's types come from (api.Bundle), so a
// server that passes them answers what the Web client compiles against. The
// split source would need kin-openapi to resolve references across files the
// way Redocly CLI does; the bundle has none, and `make check` keeps it equal
// to the source.
package openapitest

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	"github.com/zhongwater123/A-NAS/api"
)

// The contract is parsed and validated once per test binary: tests only read
// it, and kin-openapi keeps its compiled patterns in a sync.Map.
var (
	contract = sync.OnceValues(func() (*openapi3.T, error) {
		document, err := openapi3.NewLoader().LoadFromData(api.Bundle)
		if err != nil {
			return nil, err
		}
		return document, document.Validate(context.Background())
	})
	router = sync.OnceValues(func() (routers.Router, error) {
		document, err := contract()
		if err != nil {
			return nil, err
		}
		return legacyrouter.NewRouter(document)
	})
)

// Document returns the contract, validated.
func Document(t testing.TB) *openapi3.T {
	t.Helper()
	document, err := contract()
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	return document
}

// Router returns a router that finds the contract's operation for a request.
func Router(t testing.TB) routers.Router {
	t.Helper()
	found, err := router()
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}
	return found
}

// Check fails the test unless request and the response it got keep to the
// contract. Serving consumes a request's body, so request is a second one
// made the same way.
func Check(t testing.TB, request *http.Request, recorder *httptest.ResponseRecorder) {
	t.Helper()
	route, params, err := Router(t).FindRoute(request)
	if err != nil {
		t.Fatalf("find OpenAPI route for %s %s: %v", request.Method, request.URL.Path, err)
	}
	input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{
		AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil },
	}}
	if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
		t.Fatalf("validate %s %s request: %v", request.Method, request.URL.Path, err)
	}
	// A status the contract does not declare for the operation fails too.
	response := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: recorder.Code, Header: recorder.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	response.SetBodyBytes(recorder.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), response); err != nil {
		t.Fatalf("validate %s %s response: %v", request.Method, request.URL.Path, err)
	}
	Record(route, recorder.Code)
}

// checked holds, per operation, the statuses of the exchanges checked
// against the contract in this test binary.
var checked = struct {
	sync.Mutex
	statuses map[string]map[int]bool
}{statuses: map[string]map[int]bool{}}

// Record notes that an exchange on route answered status and kept to the
// contract.
func Record(route *routers.Route, status int) {
	operation := route.Method + " " + route.Path
	checked.Lock()
	defer checked.Unlock()
	if checked.statuses[operation] == nil {
		checked.statuses[operation] = map[int]bool{}
	}
	checked.statuses[operation][status] = true
}

// WithoutSuccess lists, sorted by path, the contract's operations that no
// checked exchange answered with a 2xx status (#273).
func WithoutSuccess() ([]string, error) {
	document, err := contract()
	if err != nil {
		return nil, err
	}
	checked.Lock()
	defer checked.Unlock()
	var missing []string
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			operation := method + " " + path
			if !slices.ContainsFunc(slices.Collect(maps.Keys(checked.statuses[operation])), func(status int) bool { return status >= 200 && status < 300 }) {
				missing = append(missing, operation)
			}
		}
	}
	slices.SortFunc(missing, func(a, b string) int {
		return strings.Compare(a[strings.IndexByte(a, ' '):], b[strings.IndexByte(b, ' '):])
	})
	return missing, nil
}
