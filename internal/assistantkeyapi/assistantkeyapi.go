// Package assistantkeyapi serves the administrator's page for the AI assistant's model key
// (设置 → VNAS, docs/decisions/2026-11-17-assistant-v1-cloud-model.md): the administrator
// pastes the household's DeepSeek key once, after confirming that members' conversations and
// what VNAS reads for them go to DeepSeek's servers, and can then replace or delete it but
// never see it again. The Host Agent keeps it (internal/assistantkey); nothing here stores or
// logs it.
package assistantkeyapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/assistantkey"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// Path is the key's resource.
const Path = "/api/v1/assistant-key"

// API serves keeper; a nil keeper answers assistant_unavailable.
type API struct {
	keeper assistantkey.Keeper
	logger *slog.Logger
}

func New(keeper assistantkey.Keeper, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{keeper: keeper, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: Path, Posture: apiroute.Admin, Handler: a.ready(a.state)},
		{Method: http.MethodPut, Pattern: Path, Posture: apiroute.Admin, Handler: a.ready(a.save)},
		{Method: http.MethodDelete, Pattern: Path, Posture: apiroute.Admin, Handler: a.ready(a.remove)},
	}
}

// Request saves a key. Acknowledged is the administrator's confirmation that members'
// conversations and what VNAS reads for them go to DeepSeek; without it nothing is saved.
type Request struct {
	Key          string `json:"key"`
	Acknowledged bool   `json:"acknowledged"`
}

func (a *API) ready(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		w.Header().Set("Cache-Control", "no-store")
		if a.keeper == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not available on this device")
			return
		}
		next(w, r, session)
	}
}

func (a *API) state(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	state, err := a.keeper.AssistantKey(r.Context())
	a.answer(w, r, state, err)
}

func (a *API) save(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request Request
	if !httpjson.Read(w, r, 4<<10, &request) {
		return
	}
	if !request.Acknowledged {
		httpjson.WriteError(w, http.StatusBadRequest, "acknowledgement_required", "confirm that VNAS sends what it handles to DeepSeek")
		return
	}
	key := strings.TrimSpace(request.Key)
	if !assistantkey.ValidKey(key) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_key", "the key is not a DeepSeek API key")
		return
	}
	state, err := a.keeper.SetAssistantKey(r.Context(), key)
	if err == nil {
		a.logger.InfoContext(r.Context(), "assistant key saved", "by", session.User.Username)
	}
	a.answer(w, r, state, err)
}

func (a *API) remove(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	state, err := a.keeper.DeleteAssistantKey(r.Context())
	if err == nil {
		a.logger.InfoContext(r.Context(), "assistant key deleted", "by", session.User.Username)
	}
	a.answer(w, r, state, err)
}

func (a *API) answer(w http.ResponseWriter, r *http.Request, state assistantkey.State, err error) {
	if err != nil {
		a.logger.ErrorContext(r.Context(), "assistant key operation failed", "method", r.Method, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the Host Agent could not do it; try again")
		return
	}
	httpjson.Write(w, http.StatusOK, state)
}
