package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// TestTodosKeepToTheContract drives 家庭待办 through the Product Service as
// development runs it, with the family store in process. Nobody joins the
// family: every active account uses the to-dos.
func TestTodosKeepToTheContract(t *testing.T) {
	f := newActivityFixture(t, func(*photos.Service, *accounts.Service) http.Handler { return nil })
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	var alice struct {
		ID string `json:"id"`
	}
	decode(t, owner.do(http.MethodPost, "/api/v1/users", `{"username":"alice","password":"alice password for testing"}`, nil, http.StatusCreated).Body, &alice)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	aliceClient := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)

	// Long overdue, so the stage holds even if the test runs across midnight.
	due := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	add := `{"requestKey":"k1","title":"交电费","category":"bill","due":"` + due + `","amountCents":12345}`
	var todo family.Todo
	decode(t, owner.do(http.MethodPost, "/api/v1/todos", add, nil, http.StatusCreated).Body, &todo)
	var repeated family.Todo
	decode(t, owner.do(http.MethodPost, "/api/v1/todos", add, nil, http.StatusCreated).Body, &repeated)
	if repeated.ID != todo.ID || todo.Scope != family.TodoShared {
		t.Fatalf("repeated = %+v, first = %+v", repeated, todo)
	}
	owner.do(http.MethodPost, "/api/v1/todos", `{"title":"换证","ownerId":"nobody"}`, nil, http.StatusUnprocessableEntity)
	owner.do(http.MethodPost, "/api/v1/todos", `{"title":"换证","due":"2026-02-30"}`, nil, http.StatusUnprocessableEntity)

	var list struct {
		Items []family.Todo `json:"items"`
	}
	decode(t, bob.do(http.MethodGet, "/api/v1/todos", "", nil, http.StatusOK).Body, &list)
	if len(list.Items) != 1 || list.Items[0].ID != todo.ID {
		t.Fatalf("bob's to-dos = %+v", list.Items)
	}

	todoPath := "/api/v1/todos/" + todo.ID
	edit := `{"title":"交电费","category":"bill","due":"` + due + `","ownerId":"` + alice.ID + `","amountCents":12345,"version":1}`
	aliceClient.do(http.MethodPatch, todoPath, edit, nil, http.StatusOK)
	bob.do(http.MethodPatch, todoPath, edit, nil, http.StatusConflict)
	bob.do(http.MethodPatch, todoPath, `{"title":"交电费","ownerId":"nobody","version":2}`, nil, http.StatusUnprocessableEntity)
	bob.do(http.MethodPatch, "/api/v1/todos/todo_missing", edit, nil, http.StatusNotFound)

	// Each is reminded and acknowledges for themselves.
	var reminders struct {
		Items []family.TodoReminder `json:"items"`
	}
	decode(t, bob.do(http.MethodGet, "/api/v1/todos/reminders", "", nil, http.StatusOK).Body, &reminders)
	if len(reminders.Items) != 1 || reminders.Items[0].Stage != family.StageOverdue || reminders.Items[0].OwnerID != alice.ID {
		t.Fatalf("bob's reminders = %+v", reminders.Items)
	}
	keys, _ := json.Marshal(map[string][]string{"keys": {reminders.Items[0].Key, "todo:gone:0:soon"}})
	bob.do(http.MethodPost, "/api/v1/todos/reminders/seen", string(keys), nil, http.StatusNoContent)
	// The contract counts characters, the store bytes.
	bob.do(http.MethodPost, "/api/v1/todos/reminders/seen", `{"keys":["todo:`+strings.Repeat("交", 70)+`"]}`, nil, http.StatusUnprocessableEntity)
	decode(t, bob.do(http.MethodGet, "/api/v1/todos/reminders", "", nil, http.StatusOK).Body, &reminders)
	if len(reminders.Items) != 0 {
		t.Fatalf("bob is told again: %+v", reminders.Items)
	}
	decode(t, aliceClient.do(http.MethodGet, "/api/v1/todos/reminders", "", nil, http.StatusOK).Body, &reminders)
	if len(reminders.Items) != 1 {
		t.Fatalf("alice's reminders = %+v", reminders.Items)
	}

	bob.do(http.MethodPut, todoPath+"/snooze", `{"days":7}`, nil, http.StatusOK)
	bob.do(http.MethodPut, "/api/v1/todos/todo_missing/snooze", `{"days":7}`, nil, http.StatusNotFound)
	aliceClient.do(http.MethodPut, todoPath+"/status", `{"status":"done"}`, nil, http.StatusOK)
	bob.do(http.MethodPut, todoPath+"/snooze", `{"days":7}`, nil, http.StatusConflict)
	bob.do(http.MethodPut, "/api/v1/todos/todo_missing/status", `{"status":"done"}`, nil, http.StatusNotFound)
	decode(t, bob.do(http.MethodPut, todoPath+"/status", `{"status":"open"}`, nil, http.StatusOK).Body, &todo)
	if todo.Status != family.TodoOpen || todo.RemindRound != 2 || todo.Version != 2 {
		t.Fatalf("opened again = %+v", todo)
	}
}
