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
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
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
