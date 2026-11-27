package sessionlookup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

const (
	// AgentTokenPath names the account behind an agent token (ADR 0040).
	//nolint:gosec // G101: a route, not a credential.
	AgentTokenPath = "/v1/agent-tokens/resolve"
	// AgentTokenIDPath confirms that a token, named by its ID, is still good.
	//nolint:gosec // G101: a route, not a credential.
	AgentTokenIDPath = "/v1/agent-tokens/check"
)

// AgentTokenResolver is the Host Agent's read-only view of agent tokens
// (sessiondir.Directory).
type AgentTokenResolver interface {
	ResolveAgentToken(ctx context.Context, token, scope string) (accountid.AgentTokenUser, error)
	AgentTokenByID(ctx context.Context, id, scope string) (accountid.AgentTokenUser, error)
}

type agentTokenRequest struct {
	Token string `json:"token,omitempty"`
	ID    string `json:"id,omitempty"`
}

// WithAgentTokens serves AgentTokenPath and AgentTokenIDPath for the tokens
// of scope, and hands every other request to next. The Host Agent adds it
// only on the socket of the one service the scope belongs to: the shared
// workspace's lookup resolves workspace tokens and nothing else.
func WithAgentTokens(next http.Handler, tokens AgentTokenResolver, scope string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("/", next)
	answer := func(w http.ResponseWriter, r *http.Request, user accountid.AgentTokenUser, err error) {
		switch {
		case errors.Is(err, accountid.ErrAgentTokenNotFound):
			httpjson.Write(w, http.StatusUnauthorized, errorResponse{Error: "agent_token_invalid"})
		case err != nil:
			logger.ErrorContext(r.Context(), "agent token lookup failed", "error", err)
			httpjson.Write(w, http.StatusServiceUnavailable, errorResponse{Error: "agent_tokens_unavailable"})
		default:
			httpjson.Write(w, http.StatusOK, user)
		}
	}
	read := func(w http.ResponseWriter, r *http.Request) (agentTokenRequest, bool) {
		var request agentTokenRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			httpjson.Write(w, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
			return request, false
		}
		return request, true
	}
	mux.HandleFunc("POST "+AgentTokenPath, func(w http.ResponseWriter, r *http.Request) {
		if request, ok := read(w, r); ok {
			user, err := tokens.ResolveAgentToken(r.Context(), request.Token, scope)
			answer(w, r, user, err)
		}
	})
	mux.HandleFunc("POST "+AgentTokenIDPath, func(w http.ResponseWriter, r *http.Request) {
		if request, ok := read(w, r); ok {
			user, err := tokens.AgentTokenByID(r.Context(), request.ID, scope)
			answer(w, r, user, err)
		}
	})
	return mux
}

// ResolveAgentToken returns accountid.ErrAgentTokenNotFound for a token the
// lookup does not accept. The scope is the socket's: the argument names what
// the caller expects and is not sent.
func (c *Client) ResolveAgentToken(ctx context.Context, token, _ string) (accountid.AgentTokenUser, error) {
	return lookup(ctx, c, AgentTokenPath, agentTokenRequest{Token: token}, accountid.ErrAgentTokenNotFound, namesToken)
}

// AgentTokenByID answers as ResolveAgentToken does for the token with id.
func (c *Client) AgentTokenByID(ctx context.Context, id, _ string) (accountid.AgentTokenUser, error) {
	return lookup(ctx, c, AgentTokenIDPath, agentTokenRequest{ID: id}, accountid.ErrAgentTokenNotFound, namesToken)
}

func namesToken(user accountid.AgentTokenUser) bool { return user.UserID != "" && user.TokenID != "" }
