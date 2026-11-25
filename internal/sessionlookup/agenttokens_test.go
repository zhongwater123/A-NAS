package sessionlookup_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

// agentTokens is a directory's view of agent tokens: by value and by ID,
// each for one scope, as sessiondir.Directory answers.
type agentTokens struct {
	byValue map[string]accountid.AgentTokenUser
	scopes  []string
}

func (a *agentTokens) ResolveAgentToken(_ context.Context, token, scope string) (accountid.AgentTokenUser, error) {
	a.scopes = append(a.scopes, scope)
	if token == "broken" {
		return accountid.AgentTokenUser{}, errors.New("database is locked")
	}
	user, ok := a.byValue[token]
	if !ok {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	return user, nil
}

func (a *agentTokens) AgentTokenByID(_ context.Context, id, scope string) (accountid.AgentTokenUser, error) {
	a.scopes = append(a.scopes, scope)
	for _, user := range a.byValue {
		if user.TokenID == id {
			return user, nil
		}
	}
	return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
}

func serveAgentTokens(t *testing.T, tokens *agentTokens, scope string) *sessionlookup.Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "sessions.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := sessionlookup.NewHandler(sessions{}, nil, nil)
	if scope != "" {
		handler = sessionlookup.WithAgentTokens(handler, tokens, scope, nil)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return sessionlookup.NewClient(socket)
}

func TestAgentTokensResolveOnlyForTheSocketsScope(t *testing.T) {
	alice := accountid.AgentTokenUser{
		UserID: "user:alice", Username: "alice", Role: accountid.RoleMember,
		TokenID: "agent:1", TokenName: "laptop", TokenKind: "codex",
	}
	tokens := &agentTokens{byValue: map[string]accountid.AgentTokenUser{"alice-token": alice}}
	client := serveAgentTokens(t, tokens, accountid.AgentScopeWorkspace)
	ctx := context.Background()

	// The caller cannot ask for another scope: the socket's is used.
	if user, err := client.ResolveAgentToken(ctx, "alice-token", "photos"); err != nil || !reflect.DeepEqual(user, alice) {
		t.Fatalf("ResolveAgentToken() = %+v, %v", user, err)
	}
	if user, err := client.AgentTokenByID(ctx, "agent:1", accountid.AgentScopeWorkspace); err != nil || !reflect.DeepEqual(user, alice) {
		t.Fatalf("AgentTokenByID() = %+v, %v", user, err)
	}
	for _, scope := range tokens.scopes {
		if scope != accountid.AgentScopeWorkspace {
			t.Fatalf("the directory was asked for scope %q", scope)
		}
	}
	if _, err := client.ResolveAgentToken(ctx, "forged", ""); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("forged token error = %v", err)
	}
	if _, err := client.AgentTokenByID(ctx, "agent:2", ""); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("unknown ID error = %v", err)
	}
	if _, err := client.ResolveAgentToken(ctx, "broken", ""); err == nil || errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("lookup failure error = %v, want an outage rather than an invalid token", err)
	}
	// The socket's other lookups still answer.
	if _, err := client.ResolveSessionUser(ctx, "nobody"); !errors.Is(err, accountid.ErrSessionNotFound) {
		t.Fatalf("session lookup beside agent tokens: %v", err)
	}
}

func TestAgentTokensAreOffOnOtherServicesSockets(t *testing.T) {
	tokens := &agentTokens{byValue: map[string]accountid.AgentTokenUser{"alice-token": {TokenID: "agent:1"}}}
	client := serveAgentTokens(t, tokens, "")
	if _, err := client.ResolveAgentToken(context.Background(), "alice-token", accountid.AgentScopeWorkspace); err == nil || errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("error = %v, want the lookup to be unavailable", err)
	}
	if len(tokens.scopes) != 0 {
		t.Fatal("a socket without agent tokens asked the directory")
	}
}
