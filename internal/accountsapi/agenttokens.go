package accountsapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// AgentTokens lets a member connect, list and disconnect the coding agents
// on their own computers (ADR 0040). Only the member's own tokens are
// reachable; an administrator does not see members' tokens.
type AgentTokens struct {
	accounts *accounts.Service
	logger   *slog.Logger
}

// NewAgentTokens serves the session's own agent tokens from service.
func NewAgentTokens(service *accounts.Service, logger *slog.Logger) *AgentTokens {
	return &AgentTokens{accounts: service, logger: logger}
}

// AgentTokensPath is where a member manages agent tokens.
const AgentTokensPath = "/api/v1/session/agent-tokens"

func (a *AgentTokens) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: AgentTokensPath, Posture: apiroute.Session, Handler: a.list},
		{Method: http.MethodPost, Pattern: AgentTokensPath, Posture: apiroute.Mutation, Handler: a.create},
		{Method: http.MethodDelete, Pattern: AgentTokensPath + "/{tokenID}", Posture: apiroute.Mutation, Handler: a.revoke},
	}
}

func (a *AgentTokens) list(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	tokens, err := a.accounts.AgentTokens(r.Context(), session.User)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": tokens})
}

func (a *AgentTokens) create(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name string             `json:"name"`
		Kind accounts.AgentKind `json:"kind"`
	}
	if !httpjson.Read(w, r, 1<<10, &request) {
		return
	}
	token, value, err := a.accounts.CreateAgentToken(r.Context(), session.User, request.Name, request.Kind)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, map[string]any{"agentToken": token, "token": value})
}

func (a *AgentTokens) revoke(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := a.accounts.RevokeAgentToken(r.Context(), session.User, r.PathValue("tokenID")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *AgentTokens) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, accounts.ErrAgentTokenLimit):
		httpjson.WriteError(w, http.StatusConflict, "agent_token_limit", err.Error())
	case errors.Is(err, accounts.ErrAgentTokenNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the agent token was not found")
	default:
		WriteError(w, r, a.logger, err)
	}
}
