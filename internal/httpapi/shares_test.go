package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
)

func TestShareRoutesRequireSessionAndCSRFPreserveOriginalIdentity(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	owner, err := users.SetupAdministrator(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		actor, ok := sharesapi.UserFrom(r.Context())
		if !ok || actor.ID != owner.ID || accounts.SessionToken(r.Context()) == "" {
			t.Errorf("lost original caller: %+v", actor)
		}
		w.WriteHeader(204)
	})
	handler := httpapi.NewProduct(httpapi.ProductDependencies{Accounts: users, Shares: probe})
	account := signIn(t, handler, "owner", "correct horse battery staple")
	for _, path := range []string{"/api/v1/shares", "/api/v1/shares/id/messages", "/api/v1/shares/id/save", "/api/v1/shares/id/revoke"} {
		anonymous := httptest.NewRecorder()
		handler.ServeHTTP(anonymous, httptest.NewRequest("POST", path, nil))
		if anonymous.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, anonymous.Code)
		}
		request := httptest.NewRequest("POST", path, nil)
		request.AddCookie(account.cookie)
		forged := httptest.NewRecorder()
		handler.ServeHTTP(forged, request)
		if forged.Code != 403 {
			t.Fatalf("missing CSRF %s: %d", path, forged.Code)
		}
		if response := account.call(t, "POST", path, ""); response.Code != 204 {
			t.Fatalf("authorized %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if calls != 4 {
		t.Fatalf("unauthorized calls reached sharing: %d", calls)
	}
}
