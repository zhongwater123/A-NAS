package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

func TestMembersConnectAndDisconnectTheirOwnAgents(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	handler := checkedProduct(t, httpapi.ProductDependencies{Accounts: service, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, admin, "alice", "member password for testing"); err != nil {
		t.Fatal(err)
	}
	alice := signIn(t, handler, "alice", "member password for testing")
	owner := signIn(t, handler, "owner", "correct horse battery staple")

	created := alice.call(t, http.MethodPost, "/api/v1/session/agent-tokens", `{"name":"办公电脑","kind":"claude-code"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var answer struct {
		AgentToken struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agentToken"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &answer); err != nil || !strings.HasPrefix(answer.Token, "anas_ws_") || answer.AgentToken.Name != "办公电脑" {
		t.Fatalf("created %s, %v", created.Body.String(), err)
	}

	listed := alice.call(t, http.MethodGet, "/api/v1/session/agent-tokens", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), answer.AgentToken.ID) || strings.Contains(listed.Body.String(), answer.Token) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	if others := owner.call(t, http.MethodGet, "/api/v1/session/agent-tokens", ""); strings.Contains(others.Body.String(), answer.AgentToken.ID) {
		t.Fatalf("an administrator lists a member's token: %s", others.Body.String())
	}

	for _, test := range []struct {
		body, code string
		status     int
	}{
		{`{"name":"","kind":"codex"}`, "validation_failed", http.StatusUnprocessableEntity},
		{`{"name":"laptop","kind":"cursor"}`, "validation_failed", http.StatusUnprocessableEntity},
	} {
		got := alice.call(t, http.MethodPost, "/api/v1/session/agent-tokens", test.body)
		if got.Code != test.status || !strings.Contains(got.Body.String(), test.code) {
			t.Fatalf("create %s = %d %s", test.body, got.Code, got.Body.String())
		}
	}

	if got := owner.call(t, http.MethodDelete, "/api/v1/session/agent-tokens/"+answer.AgentToken.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("an administrator revoked a member's token: %d %s", got.Code, got.Body.String())
	}
	if got := alice.call(t, http.MethodDelete, "/api/v1/session/agent-tokens/"+answer.AgentToken.ID, ""); got.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", got.Code, got.Body.String())
	}
	if _, err := service.ResolveAgentToken(ctx, answer.Token, "workspace"); err == nil {
		t.Fatal("a revoked token still resolves")
	}
}
