package familyapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/family"
)

func TestTodoEndpointsAreServedBeneathTheirPrefix(t *testing.T) {
	endpoints := Endpoints()
	for _, want := range []apiroute.Endpoint{
		{Method: http.MethodGet, Pattern: TodosPrefix},
		{Method: http.MethodPost, Pattern: TodosPrefix},
		{Method: http.MethodPatch, Pattern: TodosPrefix + "/{todoID}"},
		{Method: http.MethodPut, Pattern: TodosPrefix + "/{todoID}/status"},
		{Method: http.MethodPut, Pattern: TodosPrefix + "/{todoID}/snooze"},
		{Method: http.MethodGet, Pattern: TodosPrefix + "/owners"},
		{Method: http.MethodGet, Pattern: TodosPrefix + "/reminders"},
		{Method: http.MethodPost, Pattern: TodosPrefix + "/reminders/seen"},
	} {
		if !slices.Contains(endpoints, want) {
			t.Errorf("%s %s is not served", want.Method, want.Pattern)
		}
	}
}

// serveAs sends a request to handler as the signed-in account id.
func serveAs(handler http.Handler, id, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if id != "" {
		request = request.WithContext(accounts.WithUser(request.Context(), accounts.User{ID: id, Username: id}))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestTodosAnswerEveryAccount(t *testing.T) {
	if got := serveAs(New(nil, nil, nil), "u-1", http.MethodGet, TodosPrefix, ""); got.Code != http.StatusServiceUnavailable ||
		!strings.Contains(got.Body.String(), "family_unavailable") {
		t.Fatalf("without a store = %d %s", got.Code, got.Body.String())
	}
	store, err := family.Open(t.TempDir(), family.Options{
		Accounts: func(context.Context) (map[string]family.Account, error) {
			return map[string]family.Account{"u-1": {ID: "u-1", Username: "u-1"}, "u-2": {ID: "u-2", Username: "u-2"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := New(store, nil, nil)
	if got := serveAs(handler, "", http.MethodGet, TodosPrefix, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", got.Code)
	}
	created := serveAs(handler, "u-1", http.MethodPost, TodosPrefix, `{"title":"交电费"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("add = %d %s", created.Code, created.Body.String())
	}
	var todo family.Todo
	if err := json.Unmarshal(created.Body.Bytes(), &todo); err != nil {
		t.Fatal(err)
	}
	// Neither account joined the family.
	for _, step := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, TodosPrefix, "", http.StatusOK},
		{http.MethodPatch, TodosPrefix + "/" + todo.ID, `{"title":"交水费","version":1}`, http.StatusOK},
		{http.MethodPatch, TodosPrefix + "/" + todo.ID, `{"title":"交水费","version":1}`, http.StatusConflict},
		{http.MethodPatch, TodosPrefix + "/" + todo.ID, `{"title":"交水费","version":2,"scope":"private"}`, http.StatusBadRequest},
		{http.MethodPut, TodosPrefix + "/" + todo.ID + "/snooze", `{"days":3}`, http.StatusOK},
		{http.MethodPut, TodosPrefix + "/" + todo.ID + "/status", `{"status":"done"}`, http.StatusOK},
		{http.MethodPut, TodosPrefix + "/" + todo.ID + "/snooze", `{"days":3}`, http.StatusConflict},
		{http.MethodGet, TodosPrefix + "/owners", "", http.StatusOK},
		{http.MethodGet, TodosPrefix + "/reminders", "", http.StatusOK},
		{http.MethodPost, TodosPrefix + "/reminders/seen", `{"keys":["todo:x"]}`, http.StatusNoContent},
		{http.MethodPost, TodosPrefix + "/reminders/seen", `{"keys":["join:x"]}`, http.StatusUnprocessableEntity},
		{http.MethodGet, TodosPrefix + "/nothing/here", "", http.StatusNotFound},
	} {
		if got := serveAs(handler, "u-2", step.method, step.path, step.body); got.Code != step.want {
			t.Errorf("%s %s = %d %s, want %d", step.method, step.path, got.Code, got.Body.String(), step.want)
		}
	}
}
