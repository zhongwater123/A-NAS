package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

func TestResetMemberMustChangePasswordAndAdministratorCanViewReadOnly(t *testing.T) {
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
	fileService := files.NewLocalService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService, Files: fileService,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := accountService.ResetCredential(ctx, admin, alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}

	member := signIn(t, handler, "alice", "temporary password 1")
	if got := member.call(t, http.MethodGet, "/api/v1/spaces", ""); got.Code != http.StatusForbidden || !bytes.Contains(got.Body.Bytes(), []byte("password_change_required")) {
		t.Fatalf("spaces before changing the password = %d %s", got.Code, got.Body.String())
	}
	if got := member.call(t, http.MethodGet, "/api/v1/notifications", ""); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte("credential_reset")) {
		t.Fatalf("notifications = %d %s", got.Code, got.Body.String())
	}
	if got := member.call(t, http.MethodPost, "/api/v1/session/password", `{"currentPassword":"temporary password 1","newPassword":"alice chosen password"}`); got.Code != http.StatusNoContent {
		t.Fatalf("change password = %d %s", got.Code, got.Body.String())
	}
	if got := member.call(t, http.MethodGet, "/api/v1/spaces", ""); got.Code != http.StatusOK {
		t.Fatalf("spaces after changing the password = %d %s", got.Code, got.Body.String())
	}
	var aliceSpaces struct{ Items []accounts.Space }
	_ = json.Unmarshal(member.call(t, http.MethodGet, "/api/v1/spaces", "").Body.Bytes(), &aliceSpaces)
	var aliceSpace string
	for _, space := range aliceSpaces.Items {
		if space.Kind == accounts.SpaceKindPrivate {
			aliceSpace = space.ID
		}
	}
	if got := member.call(t, http.MethodPost, "/api/v1/spaces/"+aliceSpace+"/directories", `{"name":"docs"}`); got.Code != http.StatusCreated {
		t.Fatalf("alice creates a directory = %d %s", got.Code, got.Body.String())
	}

	administrator := signIn(t, handler, "owner", "correct horse battery staple")
	if got := administrator.call(t, http.MethodPost, "/api/v1/users/"+alice.ID+"/viewing", `{"password":"wrong password here","reason":"help"}`); got.Code != http.StatusForbidden {
		t.Fatalf("viewing with a wrong password = %d %s", got.Code, got.Body.String())
	}
	started := administrator.call(t, http.MethodPost, "/api/v1/users/"+alice.ID+"/viewing", `{"password":"correct horse battery staple","reason":"Alice asked for help"}`)
	if started.Code != http.StatusCreated {
		t.Fatalf("start viewing = %d %s", started.Code, started.Body.String())
	}
	var grant accounts.ViewingGrant
	_ = json.Unmarshal(started.Body.Bytes(), &grant)
	if got := administrator.call(t, http.MethodGet, "/api/v1/spaces/"+aliceSpace+"/entries", ""); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"docs"`)) {
		t.Fatalf("viewed entries = %d %s", got.Code, got.Body.String())
	}
	if got := administrator.call(t, http.MethodPost, "/api/v1/spaces/"+aliceSpace+"/directories", `{"name":"admin"}`); got.Code != http.StatusForbidden {
		t.Fatalf("writing while viewing = %d %s", got.Code, got.Body.String())
	}
	if got := administrator.call(t, http.MethodDelete, "/api/v1/viewing/"+grant.ID, ""); got.Code != http.StatusNoContent {
		t.Fatalf("end viewing = %d %s", got.Code, got.Body.String())
	}
	if got := administrator.call(t, http.MethodGet, "/api/v1/spaces/"+aliceSpace+"/entries", ""); got.Code != http.StatusForbidden {
		t.Fatalf("entries after ending viewing = %d %s", got.Code, got.Body.String())
	}
	var notifications struct{ Items []accounts.Notification }
	_ = json.Unmarshal(member.call(t, http.MethodGet, "/api/v1/notifications", "").Body.Bytes(), &notifications)
	var viewingNotice *accounts.Notification
	for i := range notifications.Items {
		if notifications.Items[i].Kind == accounts.NotificationAdminViewing {
			viewingNotice = &notifications.Items[i]
		}
	}
	if viewingNotice == nil || viewingNotice.Reason != "Alice asked for help" {
		t.Fatalf("alice notifications = %#v", notifications.Items)
	}
	if got := member.call(t, http.MethodPost, "/api/v1/notifications/"+viewingNotice.ID+"/acknowledge", "{}"); got.Code != http.StatusNoContent {
		t.Fatalf("acknowledge = %d %s", got.Code, got.Body.String())
	}
}

type signedIn struct {
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func signIn(t *testing.T, handler http.Handler, username, password string) signedIn {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewBufferString(`{"username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("sign in %s = %d %s", username, response.Code, response.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &session)
	return signedIn{handler: handler, cookie: response.Result().Cookies()[0], csrf: session.CSRFToken}
}

func (s signedIn) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.AddCookie(s.cookie)
	request.Header.Set("X-CSRF-Token", s.csrf)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	s.handler.ServeHTTP(response, request)
	return response
}

func TestAccountErrorsNameTheRuleThatFailed(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Accounts: accountService, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	owner := signIn(t, handler, "owner", "correct horse battery staple")
	for _, test := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodPost, "/api/v1/users", `{"username":"小明","password":"member password for testing"}`, http.StatusUnprocessableEntity, "invalid_username"},
		{http.MethodPost, "/api/v1/users", `{"username":"shared","password":"member password for testing"}`, http.StatusConflict, "username_unavailable"},
		{http.MethodPost, "/api/v1/users", `{"username":"alice","password":"too short"}`, http.StatusUnprocessableEntity, "invalid_password"},
		{http.MethodPost, "/api/v1/users", `{"username":"alice","password":"short\nshort\nthen any text"}`, http.StatusUnprocessableEntity, "invalid_password"},
		{http.MethodPost, "/api/v1/users", `{"username":"alice","password":"member password for testing"}`, http.StatusCreated, ""},
		{http.MethodPost, "/api/v1/users", `{"username":"Alice","password":"member password for testing"}`, http.StatusConflict, "username_unavailable"},
		{http.MethodPatch, "/api/v1/users/" + admin.ID + "/credential", `{"password":"administrator new password"}`, http.StatusForbidden, "forbidden"},
		{http.MethodPost, "/api/v1/session/password", `{"currentPassword":"correct horse battery staple","newPassword":"correct horse battery staple"}`, http.StatusUnprocessableEntity, "password_unchanged"},
	} {
		got := owner.call(t, test.method, test.path, test.body)
		if got.Code != test.status || (test.code != "" && errorCode(t, got) != test.code) {
			t.Errorf("%s %s %s = %d %s, want %d %s", test.method, test.path, test.body, got.Code, got.Body.String(), test.status, test.code)
		}
	}
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", bytes.NewBufferString(`{"username":"second","password":"correct horse battery staple"}`))
	setup.Header.Set("Content-Type", "application/json")
	fromLocalConsole(setup)
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, setup)
	if got.Code != http.StatusConflict || errorCode(t, got) != "setup_complete" {
		t.Fatalf("second setup = %d %s, want 409 setup_complete", got.Code, got.Body.String())
	}
}

func TestOnlyTheLocalConsoleCanSetUpTheDevice(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Accounts: accountService, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	lan := func(r *http.Request) { // as Caddy forwards a LAN browser (ADR 0012)
		fromLocalConsole(r)
		r.Header.Set("X-Forwarded-For", "192.0.2.20")
	}
	send := func(method, path, body string, prepare func(*http.Request)) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		prepare(request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	credentials := `{"username":"owner","password":"correct horse battery staple"}`
	if got := send(http.MethodGet, "/api/v1/setup/status", "", lan).Body.String(); !strings.Contains(got, `"setupAllowed":false`) || !strings.Contains(got, `"setupRequired":true`) {
		t.Fatalf("LAN setup status = %s", got)
	}
	if got := send(http.MethodPost, "/api/v1/setup/admin", credentials, lan); got.Code != http.StatusForbidden || errorCode(t, got) != "setup_local_only" {
		t.Fatalf("LAN setup = %d %s, want 403 setup_local_only", got.Code, got.Body.String())
	}
	if required, err := accountService.SetupRequired(ctx); err != nil || !required {
		t.Fatalf("SetupRequired after LAN setup = %v, %v; want still required", required, err)
	}
	if got := send(http.MethodGet, "/api/v1/setup/status", "", fromLocalConsole).Body.String(); !strings.Contains(got, `"setupAllowed":true`) {
		t.Fatalf("local setup status = %s", got)
	}
	if got := send(http.MethodPost, "/api/v1/setup/admin", credentials, fromLocalConsole); got.Code != http.StatusCreated {
		t.Fatalf("local setup = %d %s", got.Code, got.Body.String())
	}
}

func TestMembersKeepSeeingWhoResetTheirPassword(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Accounts: accountService, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(signIn(t, handler, "alice", "alice password for testing").call(t, http.MethodGet, "/api/v1/session", "").Body.String(), "credentialReset") {
		t.Fatal("a never-reset account reports a reset")
	}
	if err := accountService.ResetCredential(ctx, admin, alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}
	// Whoever signs in with the temporary password chooses the next one and
	// acknowledges the notification; the session still names the reset.
	member := signIn(t, handler, "alice", "temporary password 1")
	member.call(t, http.MethodPost, "/api/v1/session/password", `{"currentPassword":"temporary password 1","newPassword":"someone else's choice"}`)
	notifications, err := accountService.Notifications(ctx, alice)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications = %#v, %v", notifications, err)
	}
	if err := accountService.AcknowledgeNotification(ctx, alice, notifications[0].ID); err != nil {
		t.Fatal(err)
	}
	var session struct {
		CredentialReset *struct {
			ActorUsername string `json:"actorUsername"`
			At            string `json:"at"`
		} `json:"credentialReset"`
	}
	got := member.call(t, http.MethodGet, "/api/v1/session", "")
	if err := json.Unmarshal(got.Body.Bytes(), &session); err != nil || session.CredentialReset == nil || session.CredentialReset.ActorUsername != "owner" || session.CredentialReset.At == "" {
		t.Fatalf("session after the takeover = %s", got.Body.String())
	}
}
