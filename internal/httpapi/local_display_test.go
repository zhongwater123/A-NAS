package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestOnlyAdministratorsChooseWhenTheLocalScreenSleeps(t *testing.T) {
	ctx := context.Background()
	f := newActivityFixture(t, func(*photos.Service) http.Handler { return nil })
	sleepAfter := func(recorder *httptest.ResponseRecorder) int {
		t.Helper()
		var display struct {
			SleepAfterMinutes int `json:"sleepAfterMinutes"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &display); err != nil {
			t.Fatalf("decode local display: %v; body=%s", err, recorder.Body.String())
		}
		return display.SleepAfterMinutes
	}

	// The sign-in page on the device's screen reads it before anyone signs in.
	signedOut := f.client
	if got := sleepAfter(signedOut.do(http.MethodGet, "/api/v1/local-display", "", nil, http.StatusOK)); got != 30 {
		t.Fatalf("default sleep = %d minutes", got)
	}

	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	admin, err := f.accounts.UserIDForUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.CreateMember(ctx, accounts.User{ID: admin, Role: accounts.RoleAdmin, Status: accounts.UserStatusActive}, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	alice := f.signIn("/api/v1/session", "alice", "alice password for testing", "192.168.1.20", http.StatusOK)

	alice.do(http.MethodPut, "/api/v1/local-display", `{"sleepAfterMinutes":0}`, nil, http.StatusForbidden)
	if got := sleepAfter(owner.do(http.MethodPut, "/api/v1/local-display", `{"sleepAfterMinutes":10}`, nil, http.StatusOK)); got != 10 {
		t.Fatalf("saved sleep = %d minutes", got)
	}
	if got := sleepAfter(signedOut.do(http.MethodGet, "/api/v1/local-display", "", nil, http.StatusOK)); got != 10 {
		t.Fatalf("sleep after saving = %d minutes", got)
	}

	// Requests the contract itself rules out are sent around the contract client.
	outsideContract := func(cookie *http.Cookie, csrf, body string) int {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/local-display", strings.NewReader(body))
		if cookie != nil {
			request.AddCookie(cookie)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for body, want := range map[string]int{
		`{"sleepAfterMinutes":15}`: http.StatusUnprocessableEntity,
		`{"sleepAfterMinutes":-1}`: http.StatusUnprocessableEntity,
		`{}`:                       http.StatusBadRequest,
		`{"sleepAfter":30}`:        http.StatusBadRequest,
	} {
		if got := outsideContract(owner.cookie, owner.csrf, body); got != want {
			t.Errorf("PUT %s = %d, want %d", body, got, want)
		}
	}
	if got := outsideContract(owner.cookie, "", `{"sleepAfterMinutes":60}`); got != http.StatusForbidden {
		t.Errorf("without the CSRF token = %d", got)
	}
	if got := outsideContract(nil, "", `{"sleepAfterMinutes":60}`); got != http.StatusUnauthorized {
		t.Errorf("signed out = %d", got)
	}
	if got := sleepAfter(signedOut.do(http.MethodGet, "/api/v1/local-display", "", nil, http.StatusOK)); got != 10 {
		t.Fatalf("refused requests changed the sleep to %d minutes", got)
	}
}
