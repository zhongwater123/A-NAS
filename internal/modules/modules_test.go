//go:build linux

package modules_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/modulemanifest"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

func TestProxyKnowsAModuleIsInstalledByItsSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "mod")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	handler, installed := modules.Proxy("scoring", dir, nil)
	get := func(cookie bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/modules/scoring/me", nil)
		if cookie {
			request.Header.Set("Cookie", "anas_session=secret")
		}
		request = request.WithContext(accounts.WithSessionToken(request.Context(), "token"))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if installed() || get(false).Code != http.StatusNotFound || !strings.Contains(get(false).Body.String(), "module_not_installed") {
		t.Fatal("a module without its socket is installed")
	}
	stale, err := net.Listen("unix", modulemanifest.SocketPath(dir, "scoring"))
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	if !installed() || get(false).Code != http.StatusServiceUnavailable {
		t.Fatal("a socket nobody serves is not module_unavailable")
	}
	_ = os.Remove(modulemanifest.SocketPath(dir, "scoring"))
	listener, err := net.Listen("unix", modulemanifest.SocketPath(dir, "scoring"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get(serviceproxy.SessionHeader)+"|"+r.Header.Get("Cookie"))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if body := get(true).Body.String(); body != "token|" {
		t.Fatalf("forwarded %q; the module gets the token and no cookie", body)
	}
	list := httptest.NewRecorder()
	modules.List([]modules.Module{{ID: "scoring", Installed: installed}, {ID: "other", Installed: func() bool { return false }}}).
		ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/modules", nil))
	if strings.TrimSpace(list.Body.String()) != `{"items":[{"id":"scoring"}]}` {
		t.Fatalf("list %s", list.Body)
	}
}

type resolver map[string]accounts.SessionUser

func (r resolver) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	user, ok := r[token]
	if !ok {
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
	return user, nil
}

func (resolver) ActiveProfiles(context.Context) ([]accounts.Profile, error) { return nil, nil }

func TestModulesAuthenticateWithTheHostAgentsAnswerOnly(t *testing.T) {
	sessions := resolver{
		"admin":   {UserID: "user:admin", Username: "admin", Role: accounts.RoleAdmin},
		"renewal": {UserID: "user:bob", Username: "bob", Role: accounts.RoleMember, MustChangePassword: true},
	}
	handler := modules.Authenticate(sessions, "scoring_unavailable", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := accounts.UserFrom(r.Context())
		if user.ActiveAdministrator() {
			_, _ = io.WriteString(w, "admin")
		}
	}))
	for token, want := range map[string]int{"": http.StatusUnauthorized, "forged": http.StatusUnauthorized, "renewal": http.StatusForbidden, "admin": http.StatusOK} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(serviceproxy.SessionHeader, token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want || token == "admin" && recorder.Body.String() != "admin" {
			t.Errorf("token %q: %d %s", token, recorder.Code, recorder.Body)
		}
	}
}
