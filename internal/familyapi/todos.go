package familyapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// todoRoutes are the to-do operations beneath TodosPrefix. The store decides
// who sees and changes what; the handlers only translate.
func (h *handler) todoRoutes() map[string]route {
	return map[string]route{
		"GET ":                 h.listTodos,
		"POST ":                h.addTodo,
		"PATCH /{todoID}":      h.updateTodo,
		"PUT /{todoID}/status": h.setTodoStatus,
		"PUT /{todoID}/snooze": h.snoozeTodo,
		"GET /owners":          h.todoOwners,
		"GET /reminders":       h.todoReminders,
		"POST /reminders/seen": h.acknowledgeTodoReminders,
	}
}

// todoOwners lists the accounts a to-do can be given to; every account may
// read it, like the to-dos.
func (h *handler) todoOwners(w http.ResponseWriter, r *http.Request, _ family.Actor) {
	owners, err := h.store.TodoOwners(r.Context())
	h.reply(w, r, http.StatusOK, items[family.TodoOwner]{Items: owners}, err)
}

func (h *handler) listTodos(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	todos, err := h.store.Todos(r.Context(), actor)
	h.reply(w, r, http.StatusOK, items[family.Todo]{Items: todos}, err)
}

func (h *handler) addTodo(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var fields family.TodoFields
	if !httpjson.Read(w, r, maxJSONBytes, &fields) {
		return
	}
	todo, err := h.store.AddTodo(r.Context(), actor, fields)
	h.reply(w, r, http.StatusCreated, todo, err)
}

func (h *handler) updateTodo(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var edit family.TodoEdit
	if !httpjson.Read(w, r, maxJSONBytes, &edit) {
		return
	}
	todo, err := h.store.UpdateTodo(r.Context(), actor, r.PathValue("todoID"), edit)
	h.reply(w, r, http.StatusOK, todo, err)
}

func (h *handler) setTodoStatus(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Status string `json:"status"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	todo, err := h.store.SetTodoStatus(r.Context(), actor, r.PathValue("todoID"), request.Status)
	h.reply(w, r, http.StatusOK, todo, err)
}

func (h *handler) snoozeTodo(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Days int `json:"days"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	todo, err := h.store.SnoozeTodo(r.Context(), actor, r.PathValue("todoID"), request.Days)
	h.reply(w, r, http.StatusOK, todo, err)
}

func (h *handler) todoReminders(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	reminders, err := h.store.TodoReminders(r.Context(), actor)
	h.reply(w, r, http.StatusOK, items[family.TodoReminder]{Items: reminders}, err)
}

func (h *handler) acknowledgeTodoReminders(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Keys []string `json:"keys"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	h.reply(w, r, http.StatusNoContent, nil, h.store.AcknowledgeTodoReminders(r.Context(), actor, request.Keys))
}
