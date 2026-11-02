package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
	"github.com/zhongwater123/A-NAS/internal/websearch"
	"github.com/zhongwater123/A-NAS/internal/websearchapi"
)

// routedProduct is a product with every API mounted, behind stand-ins that
// the guard never lets a request reach, and an administrator's session.
func routedProduct(t *testing.T) (http.Handler, *http.Cookie) {
	t.Helper()
	product, owner, _ := routedSessions(t)
	return product, &http.Cookie{Name: "anas_session", Value: owner.Token}
}

// routedSessions is routedProduct with the sessions of an administrator and
// of a member.
func routedSessions(t *testing.T) (http.Handler, accounts.Session, accounts.Session) {
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
	owner, err := service.Authenticate(signedInDirectly(ctx), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, owner.User, "member", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	member, err := service.Authenticate(signedInDirectly(ctx), "member", "correct horse battery staple")
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
		Shares: unreached, Family: unreached, Media: unreached, Feedback: unreached, Transfers: unreached, Assistant: unreached,
		WebSearch: websearchapi.New(nil, websearch.Release{}, nil),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return product, owner, member
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// routeKey names an operation or route by its method and its path with
// every parameter left anonymous.
func routeKey(method, path string) string {
	return method + " " + pathParameter.ReplaceAllString(path, "{}")
}

// Every operation in the specification has a route, also beneath the
// prefix of a mounted API (#273), and the guard answers for its posture
// before any handler runs: 401 without a session cookie, and 403 without the
// CSRF token for anything but a read (#192).
func TestEveryOperationIsRoutedBehindItsPosture(t *testing.T) {
	product, cookie := routedProduct(t)
	document := openapitest.Document(t)
	routed := map[string]bool{}
	for _, route := range httpapi.Routes(product) {
		routed[routeKey(route.Method, route.Pattern)] = true
	}
	operations := 0
	for path, item := range document.Paths.Map() {
		target := "http://127.0.0.1:8080" + pathParameter.ReplaceAllString(path, "x")
		for method, operation := range item.Operations() {
			operations++
			name := method + " " + path
			if pattern := httpapi.RoutePattern(product, httptest.NewRequest(method, target, nil)); pattern == "/" || !routed[routeKey(method, path)] {
				t.Errorf("%s has no route", name)
				continue
			}
			if operation.Security != nil && len(*operation.Security) == 0 {
				continue
			}
			recorder := httptest.NewRecorder()
			product.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
			if recorder.Code != http.StatusUnauthorized || answerCode(recorder) != "authentication_required" {
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
			if recorder.Code != http.StatusForbidden || answerCode(recorder) != "csrf_failed" {
				t.Errorf("%s without the CSRF token = %d %s, want 403 csrf_failed", name, recorder.Code, recorder.Body.String())
			}
		}
	}
	if operations < 200 {
		t.Fatalf("walked only %d operations", operations)
	}
}

// Every route the product registers is in the specification, also each
// operation a mounted API serves (#273); the routes that need no session are
// exactly the operations without security, and the administrator's routes
// exactly those marked x-anas-posture: admin (#270).
func TestEveryRouteIsSpecifiedWithItsPosture(t *testing.T) {
	product, _ := routedProduct(t)
	document := openapitest.Document(t)
	specified := map[string]*openapi3.Operation{}
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			specified[routeKey(method, path)] = operation
		}
	}
	routes := httpapi.Routes(product)
	if len(routes) < 200 {
		t.Fatalf("expanded only %d routes", len(routes))
	}
	for _, route := range routes {
		operation, ok := specified[routeKey(route.Method, route.Pattern)]
		if !ok {
			t.Errorf("route %s %s is not in the specification", route.Method, route.Pattern)
			continue
		}
		public := operation.Security != nil && len(*operation.Security) == 0
		if public != (route.Posture == apiroute.Public) {
			t.Errorf("route %s %s is %s, but the specification says public = %v", route.Method, route.Pattern, route.Posture, public)
		}
		marker, marked := operation.Extensions["x-anas-posture"]
		if marked && marker != "admin" {
			t.Errorf("operation %s %s has x-anas-posture %v; the only value is admin", route.Method, route.Pattern, marker)
		}
		if marked != (route.Posture == apiroute.Admin) {
			t.Errorf("route %s %s is %s, but the specification says admin = %v", route.Method, route.Pattern, route.Posture, marked)
		}
	}
}

// A member is refused every administrator route before its handler runs,
// even with a valid CSRF token: one posture changed by mistake fails here,
// whatever the service behind it checks (#270).
func TestMembersAreRefusedEveryAdministratorRoute(t *testing.T) {
	product, _, member := routedSessions(t)
	checked := 0
	for _, route := range httpapi.Routes(product) {
		if route.Posture != apiroute.Admin {
			continue
		}
		checked++
		request := httptest.NewRequest(route.Method, "http://127.0.0.1:8080"+pathParameter.ReplaceAllString(route.Pattern, "x"), strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", member.CSRFToken)
		request.AddCookie(&http.Cookie{Name: "anas_session", Value: member.Token})
		recorder := httptest.NewRecorder()
		product.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || answerCode(recorder) != "forbidden" {
			t.Errorf("member %s %s = %d %s, want 403 forbidden", route.Method, route.Pattern, recorder.Code, recorder.Body.String())
		}
	}
	if checked < 20 {
		t.Fatalf("checked only %d administrator routes", checked)
	}
}
