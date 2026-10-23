package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

// routedProduct is a product with every API mounted, behind stand-ins that
// the guard never lets a request reach, and an administrator's session.
func routedProduct(t *testing.T) (http.Handler, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(signedInDirectly(ctx), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	unreached := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s passed the guard", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	product := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: service,
		Terminal: unreached, Containers: unreached, Apps: unreached, AppShortcuts: unreached, Photos: unreached,
		Shares: unreached, Media: unreached, Feedback: unreached, Transfers: unreached, Assistant: unreached,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return product, &http.Cookie{Name: "anas_session", Value: session.Token}
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	return document
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// Every operation in the specification has a route, and the guard answers
// for its posture before any handler runs: 401 without a session cookie, and
// 403 without the CSRF token for anything but a read (#192).
func TestEveryOperationIsRoutedBehindItsPosture(t *testing.T) {
	product, cookie := routedProduct(t)
	document := loadSpec(t)
	errorCode := func(recorder *httptest.ResponseRecorder) string {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return body.Error.Code
	}
	operations := 0
	for path, item := range document.Paths.Map() {
		target := "http://127.0.0.1:8080" + pathParameter.ReplaceAllString(path, "x")
		for method, operation := range item.Operations() {
			operations++
			name := method + " " + path
			if pattern := httpapi.RoutePattern(product, httptest.NewRequest(method, target, nil)); pattern == "/" {
				t.Errorf("%s has no route", name)
				continue
			}
			if operation.Security != nil && len(*operation.Security) == 0 {
				continue
			}
			recorder := httptest.NewRecorder()
			product.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
			if recorder.Code != http.StatusUnauthorized || errorCode(recorder) != "authentication_required" {
				t.Errorf("%s without a session = %d %s, want 401 authentication_required", name, recorder.Code, recorder.Body.String())
			}
			if apiroute.Safe(method) {
				continue
			}
			request := httptest.NewRequest(method, target, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(cookie)
			recorder = httptest.NewRecorder()
			product.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || errorCode(recorder) != "csrf_failed" {
				t.Errorf("%s without the CSRF token = %d %s, want 403 csrf_failed", name, recorder.Code, recorder.Body.String())
			}
		}
	}
	if operations < 200 {
		t.Fatalf("walked only %d operations", operations)
	}
}

// Every route the product registers is in the specification, and the routes
// that need no session are exactly the operations without security.
func TestEveryRouteIsSpecifiedWithItsPosture(t *testing.T) {
	product, _ := routedProduct(t)
	document := loadSpec(t)
	specified := map[string]*openapi3.Operation{}
	var paths []string
	for path, item := range document.Paths.Map() {
		paths = append(paths, path)
		for method, operation := range item.Operations() {
			specified[method+" "+pathParameter.ReplaceAllString(path, "{}")] = operation
		}
	}
	for _, route := range httpapi.Routes(product) {
		if route.Method == "" {
			// A mounted API serves the operations beneath its prefix.
			prefix := strings.TrimSuffix(route.Pattern, "/")
			if !slices.ContainsFunc(paths, func(path string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }) {
				t.Errorf("no operation is specified beneath %s", route.Pattern)
			}
			continue
		}
		key := route.Method + " " + pathParameter.ReplaceAllString(route.Pattern, "{}")
		operation, ok := specified[key]
		if !ok {
			t.Errorf("route %s %s is not in the specification", route.Method, route.Pattern)
			continue
		}
		public := operation.Security != nil && len(*operation.Security) == 0
		if public != (route.Posture == apiroute.Public) {
			t.Errorf("route %s %s is %s, but the specification says public = %v", route.Method, route.Pattern, route.Posture, public)
		}
	}
}
