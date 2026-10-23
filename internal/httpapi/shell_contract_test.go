package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// TestShellResponsesKeepToTheContract drives what the desktop shell reads
// before any window opens. The Web client refuses these responses at run
// time when they break the contract (web/src/api.ts), so each is validated
// here, with lists that hold something.
func TestShellResponsesKeepToTheContract(t *testing.T) {
	f := newActivityFixture(t, func(*photos.Service) http.Handler { return nil })
	f.client.do(http.MethodGet, "/api/v1/setup/status", "", nil, http.StatusOK)
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodGet, "/api/v1/setup/status", "", nil, http.StatusOK)
	created := owner.do(http.MethodPost, "/api/v1/users", `{"username":"alice","password":"alice password for testing"}`, nil, http.StatusCreated)
	var member struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &member); err != nil {
		t.Fatal(err)
	}
	// Resetting alice's password leaves her a notification.
	owner.do(http.MethodPatch, "/api/v1/users/"+member.ID+"/credential", `{"password":"a new password for alice"}`, nil, http.StatusNoContent)

	alice := f.signIn("/api/v1/session", "alice", "a new password for alice", "172.18.45.20", http.StatusOK)
	alice.do(http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	notifications := alice.do(http.MethodGet, "/api/v1/notifications", "", nil, http.StatusOK)
	if notifications.Body.Len() < len(`{"items":[{}]}`) {
		t.Fatalf("alice has no notification of the reset: %s", notifications.Body.String())
	}
	// Until she chooses her own password, that is all she reads.
	alice.do(http.MethodPost, "/api/v1/session/password", `{"currentPassword":"a new password for alice","newPassword":"alice chose this one herself"}`, nil, http.StatusNoContent)
	alice.do(http.MethodGet, "/api/v1/profiles", "", nil, http.StatusOK)
	alice.do(http.MethodGet, "/api/v1/wallpaper", "", nil, http.StatusOK)
	alice.do(http.MethodGet, "/api/v1/local-display", "", nil, http.StatusOK)
}
