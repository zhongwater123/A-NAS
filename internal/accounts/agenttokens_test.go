package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
)

type agentTokenFixture struct {
	service   *accounts.Service
	directory *sessiondir.Directory
	now       *time.Time
	admin     accounts.User
	alice     accounts.User
}

func newAgentTokenFixture(t *testing.T) agentTokenFixture {
	t.Helper()
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	service := accounts.NewService(store, &lifecycleHost{}, accounts.Options{PasswordCost: cheapPasswords, Now: func() time.Time { return now }})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	// The Host Agent reads the same database read-only (ADR 0040).
	directory := sessiondir.New(database)
	t.Cleanup(func() { _ = directory.Close() })
	return agentTokenFixture{service: service, directory: directory, now: &now, admin: admin, alice: alice}
}

// resolvable reports whether both the Product Service and the Host Agent
// accept value for the shared workspace.
func (f agentTokenFixture) resolvable(t *testing.T, value string) bool {
	t.Helper()
	ctx := context.Background()
	_, productErr := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace)
	_, hostErr := f.directory.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace)
	if (productErr == nil) != (hostErr == nil) {
		t.Fatalf("the Product Service (%v) and the Host Agent (%v) disagree about a token", productErr, hostErr)
	}
	for _, err := range []error{productErr, hostErr} {
		if err != nil && !errors.Is(err, accountid.ErrAgentTokenNotFound) {
			t.Fatalf("resolving a token failed: %v", err)
		}
	}
	return productErr == nil
}

func TestAgentTokensAreIssuedListedAndRevokedByTheirOwnerAlone(t *testing.T) {
	ctx := context.Background()
	f := newAgentTokenFixture(t)

	token, value, err := f.service.CreateAgentToken(ctx, f.alice, "  办公电脑 ", accounts.AgentClaudeCode)
	if err != nil {
		t.Fatalf("CreateAgentToken() error = %v", err)
	}
	if !strings.HasPrefix(value, accountid.AgentTokenPrefix) || !accountid.LooksLikeAgentToken(value) {
		t.Fatalf("token value %q has no agent token's shape", value)
	}
	if token.Name != "办公电脑" || token.Kind != accounts.AgentClaudeCode || token.Scope != accountid.AgentScopeWorkspace || token.LastUsedAt != nil {
		t.Fatalf("token = %+v", token)
	}

	caller, err := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace)
	if err != nil || caller.User.ID != f.alice.ID || caller.Token.ID != token.ID || caller.Token.LastUsedAt == nil {
		t.Fatalf("ResolveAgentToken() = %+v, %v", caller, err)
	}
	host, err := f.directory.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace)
	if err != nil || host.UserID != f.alice.ID || host.Username != "alice" || host.TokenID != token.ID || host.TokenName != "办公电脑" || host.TokenKind != "claude-code" {
		t.Fatalf("the Host Agent resolves %+v, %v", host, err)
	}
	if byID, err := f.directory.AgentTokenByID(ctx, token.ID, accountid.AgentScopeWorkspace); err != nil || byID.UserID != f.alice.ID {
		t.Fatalf("AgentTokenByID() = %+v, %v", byID, err)
	}
	listed, err := f.service.AgentTokens(ctx, f.alice)
	if err != nil || len(listed) != 1 || listed[0].ID != token.ID || listed[0].LastUsedAt == nil {
		t.Fatalf("AgentTokens() = %+v, %v", listed, err)
	}
	if others, err := f.service.AgentTokens(ctx, f.admin); err != nil || len(others) != 0 {
		t.Fatalf("an administrator lists another member's tokens: %+v, %v", others, err)
	}

	if err := f.service.RevokeAgentToken(ctx, f.admin, token.ID); !errors.Is(err, accounts.ErrAgentTokenNotFound) {
		t.Fatalf("an administrator revoked another member's token: %v", err)
	}
	if err := f.service.RevokeAgentToken(ctx, f.alice, token.ID); err != nil {
		t.Fatalf("RevokeAgentToken() error = %v", err)
	}
	if f.resolvable(t, value) {
		t.Fatal("a revoked token still resolves")
	}
	if _, err := f.directory.AgentTokenByID(ctx, token.ID, accountid.AgentScopeWorkspace); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("a revoked token is still good by its ID: %v", err)
	}
	if err := f.service.RevokeAgentToken(ctx, f.alice, token.ID); !errors.Is(err, accounts.ErrAgentTokenNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
}

func TestAgentTokensNeedANameAKindAndRoom(t *testing.T) {
	ctx := context.Background()
	f := newAgentTokenFixture(t)
	for _, c := range []struct {
		name string
		kind accounts.AgentKind
	}{
		{"   ", accounts.AgentCodex},
		{strings.Repeat("长", accounts.MaxAgentTokenName+1), accounts.AgentCodex},
		{"tab\there", accounts.AgentCodex},
		{"laptop", "cursor"},
	} {
		if _, _, err := f.service.CreateAgentToken(ctx, f.alice, c.name, c.kind); !errors.Is(err, accounts.ErrInvalidInput) {
			t.Fatalf("CreateAgentToken(%q, %q) error = %v, want invalid input", c.name, c.kind, err)
		}
	}
	for i := range accounts.MaxAgentTokens {
		if _, _, err := f.service.CreateAgentToken(ctx, f.alice, strings.Repeat("a", i+1), accounts.AgentOther); err != nil {
			t.Fatalf("token %d: %v", i+1, err)
		}
	}
	if _, _, err := f.service.CreateAgentToken(ctx, f.alice, "one more", accounts.AgentOther); !errors.Is(err, accounts.ErrAgentTokenLimit) {
		t.Fatalf("token %d: %v, want the limit", accounts.MaxAgentTokens+1, err)
	}
}

func TestAgentTokensReachOnlyTheirScopeAndEndWhenIdle(t *testing.T) {
	ctx := context.Background()
	f := newAgentTokenFixture(t)
	token, value, err := f.service.CreateAgentToken(ctx, f.alice, "laptop", accounts.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ResolveAgentToken(ctx, value, "photos"); !errors.Is(err, accounts.ErrAgentTokenInvalid) {
		t.Fatalf("a workspace token resolved for photos: %v", err)
	}
	if _, err := f.directory.ResolveAgentToken(ctx, value, "photos"); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("the Host Agent resolved a workspace token for photos: %v", err)
	}
	for _, forged := range []string{"", value[:len(value)-1], strings.ToUpper(value), "Bearer " + value} {
		if f.resolvable(t, forged) {
			t.Fatalf("%q resolved", forged)
		}
	}

	// Uses are written at most every ten minutes.
	first, err := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(9 * time.Minute)
	if again, _ := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace); !again.Token.LastUsedAt.Equal(*first.Token.LastUsedAt) {
		t.Fatalf("a use nine minutes later was written: %v, then %v", first.Token.LastUsedAt, again.Token.LastUsedAt)
	}
	*f.now = f.now.Add(2 * time.Minute)
	if again, _ := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace); !again.Token.LastUsedAt.Equal(*f.now) {
		t.Fatalf("a use eleven minutes later was not written: %v", again.Token.LastUsedAt)
	}

	// 89 days without a use keep the token; 90 end it, and it is forgotten.
	*f.now = f.now.Add(89 * 24 * time.Hour)
	if _, err := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace); err != nil {
		t.Fatalf("a token used 89 days ago: %v", err)
	}
	*f.now = f.now.Add(accountid.AgentTokenIdleLimit)
	if _, err := f.service.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace); !errors.Is(err, accounts.ErrAgentTokenInvalid) {
		t.Fatalf("a token idle for 90 days: %v", err)
	}
	if listed, err := f.service.AgentTokens(ctx, f.alice); err != nil || len(listed) != 0 {
		t.Fatalf("an idle token is still listed: %+v, %v", listed, err)
	}
	if _, err := f.directory.AgentTokenByID(ctx, token.ID, accountid.AgentScopeWorkspace); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("an idle token is good by its ID: %v", err)
	}
}

func TestTheHostAgentEndsAnIdleTokenOnItsOwnClock(t *testing.T) {
	ctx := context.Background()
	f := newAgentTokenFixture(t)
	// The Product Service issued the token 91 days before the Host Agent's
	// clock, and nobody used it since.
	*f.now = time.Now().UTC().Add(-91 * 24 * time.Hour)
	_, value, err := f.service.CreateAgentToken(ctx, f.alice, "laptop", accounts.AgentClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ResolveAgentToken(ctx, value, accountid.AgentScopeWorkspace); !errors.Is(err, accountid.ErrAgentTokenNotFound) {
		t.Fatalf("the Host Agent accepts a token idle for 91 days: %v", err)
	}
}

func TestDisablingOrResettingAnAccountEndsItsAgentTokensForGood(t *testing.T) {
	ctx := context.Background()
	f := newAgentTokenFixture(t)

	_, disabledToken, err := f.service.CreateAgentToken(ctx, f.alice, "laptop", accounts.AgentClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.DisableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if f.resolvable(t, disabledToken) {
		t.Fatal("a disabled account's token resolves")
	}
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if f.resolvable(t, disabledToken) {
		t.Fatal("enabling the account brought its token back")
	}

	session, err := f.service.Authenticate(ctx, "alice", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	_, resetToken, err := f.service.CreateAgentToken(ctx, session.User, "desktop", accounts.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary passphrase 1"); err != nil {
		t.Fatal(err)
	}
	if f.resolvable(t, resetToken) {
		t.Fatal("a token resolves after the administrator reset the password")
	}
	reset, err := f.service.Authenticate(ctx, "alice", "temporary passphrase 1")
	if err != nil || !reset.User.MustChangePassword {
		t.Fatalf("signing in with the reset password: %+v, %v", reset.User, err)
	}
	if _, _, err := f.service.CreateAgentToken(ctx, reset.User, "sneaky", accounts.AgentOther); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("a member who must choose a password issued a token: %v", err)
	}
	if err := f.service.ChangePassword(ctx, reset.User, "temporary passphrase 1", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if f.resolvable(t, resetToken) {
		t.Fatal("choosing a new password brought the token back")
	}
}
