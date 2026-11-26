package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
	"github.com/zhongwater123/A-NAS/internal/phonehotspotapi"
)

func TestPhoneHotspotIsOnlyControlledAndReadByAdministrators(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		Accounts: service, PhoneHotspot: &phonehotspot.Memory{},
	})
	f := &activityFixture{t: t, handler: handler, accounts: service, client: &contractClient{t: t, handler: handler}}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	session, err := service.ResolveSession(t.Context(), owner.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(t.Context(), session.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	member := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	path := phonehotspotapi.Path
	f.client.do(http.MethodGet, path, "", nil, http.StatusUnauthorized)
	member.do(http.MethodGet, path, "", nil, http.StatusForbidden)
	member.do(http.MethodPut, path, `{"enabled":true}`, nil, http.StatusForbidden)
	member.do(http.MethodPut, phonehotspotapi.SettingsPath, `{"ssid":"guest"}`, nil, http.StatusForbidden)
	owner.do(http.MethodPut, phonehotspotapi.SettingsPath, `{"ssid":"guest\nignore"}`, nil, http.StatusBadRequest)
	// These passwords intentionally violate the request schema. Exercise the
	// application's rejection and response contract without validating the request.
	for _, body := range []string{`{"password":"short"}`, `{"password":"八个字也不能用"}`, `{"password":"line break\n"}`} {
		request := httptest.NewRequest(http.MethodPut, phonehotspotapi.SettingsPath, strings.NewReader(body))
		request.AddCookie(owner.cookie)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", owner.csrf)
		request.Header.Set("X-Forwarded-For", owner.forwardedFor)
		rejected := httptest.NewRecorder()
		handler.ServeHTTP(rejected, request)
		if rejected.Code != http.StatusBadRequest || answerCode(rejected) != "invalid_password" {
			t.Fatalf("invalid hotspot password was not rejected: %d %s", rejected.Code, rejected.Body.String())
		}
	}
	if oversized := owner.do(http.MethodPut, phonehotspotapi.SettingsPath, "{}"+strings.Repeat(" ", 1024), nil, http.StatusRequestEntityTooLarge); answerCode(oversized) != "request_too_large" {
		t.Fatal("hotspot configure did not use the standard size error")
	}
	owner.do(http.MethodPut, path, `{"enabled":true}`, nil, http.StatusOK)
	response := owner.do(http.MethodGet, path, "", nil, http.StatusOK)
	var state phonehotspot.State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != phonehotspot.On || state.Connection == nil || state.Connection.Password == "" {
		t.Fatal("the active hotspot did not provide connection details")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("hotspot credentials must not be cacheable")
	}
	saved := owner.do(http.MethodPut, phonehotspotapi.SettingsPath, `{"ssid":"Guest Wi-Fi","password":"fixture-only-password","portal":true}`, nil, http.StatusOK)
	if strings.Contains(saved.Body.String(), "fixture-only-password") || saved.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the settings response exposed a password or allowed caching")
	}
	response = owner.do(http.MethodGet, path, "", nil, http.StatusOK)
	if strings.Contains(response.Body.String(), "fixture-only-password") {
		t.Fatal("saving settings changed active credentials before restart")
	}
	member.do(http.MethodGet, path, "", nil, http.StatusForbidden)
	response = owner.do(http.MethodPut, path, `{"enabled":false}`, nil, http.StatusOK)
	state = phonehotspot.State{}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != phonehotspot.Off || state.Connection != nil {
		t.Fatal("an inactive hotspot must not return credentials")
	}
	response = owner.do(http.MethodPut, path, `{"enabled":true}`, nil, http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Connection == nil || state.Connection.SSID != "Guest Wi-Fi" || !state.Connection.Portal {
		t.Fatal("reopening the hotspot did not apply the saved settings")
	}
}
