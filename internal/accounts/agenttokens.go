package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// AgentKind is which coding agent a token was issued for; the connect dialog
// writes the command for it and pages show it.
type AgentKind string

const (
	AgentClaudeCode AgentKind = "claude-code"
	AgentCodex      AgentKind = "codex"
	AgentOther      AgentKind = "other"
)

const (
	// MaxAgentTokens is how many tokens one account may hold.
	MaxAgentTokens = 20
	// MaxAgentTokenName bounds the name a member gives a token.
	MaxAgentTokenName = 40
	// agentTokenTouch is how stale a token's last use may get before a use is
	// written: an agent calls many times a minute, and control.db is not a
	// log.
	agentTokenTouch = 10 * time.Minute
)

var (
	// ErrAgentTokenLimit reports an account that already holds
	// MaxAgentTokens tokens.
	ErrAgentTokenLimit = errors.New("an account holds at most 20 agent tokens; revoke one first")
	// ErrAgentTokenNotFound reports a token that is not the actor's, or no
	// longer exists.
	ErrAgentTokenNotFound = errors.New("agent token not found")
	// ErrAgentTokenInvalid is what an agent learns about a token it cannot
	// use, whatever the reason (accountid.ErrAgentTokenNotFound).
	ErrAgentTokenInvalid = accountid.ErrAgentTokenNotFound
)

// AgentToken is one coding agent a member connected (ADR 0040). The token
// itself is only ever returned by CreateAgentToken.
type AgentToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       AgentKind  `json:"kind"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// AgentCaller is who calls with an agent token: the account and the token.
type AgentCaller struct {
	User  User
	Token AgentToken
}

// CreateAgentToken issues a token for one of the actor's agents and returns
// it with the token's value, which is not kept.
func (s *Service) CreateAgentToken(ctx context.Context, actor User, name string, kind AgentKind) (AgentToken, string, error) {
	if actor.Status != UserStatusActive || actor.MustChangePassword {
		return AgentToken{}, "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if !validAgentTokenName(name) || !validAgentKind(kind) {
		return AgentToken{}, "", fmt.Errorf("%w: name the agent in 1–%d characters and choose its kind", ErrInvalidInput, MaxAgentTokenName)
	}
	now := s.now().UTC()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToken{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteIdleAgentTokens(ctx, tx, actor.ID, now); err != nil {
		return AgentToken{}, "", err
	}
	var held int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_tokens WHERE user_id = ?", actor.ID).Scan(&held); err != nil {
		return AgentToken{}, "", err
	}
	if held >= MaxAgentTokens {
		return AgentToken{}, "", ErrAgentTokenLimit
	}
	value := accountid.AgentTokenPrefix + s.randomToken(32)
	token := AgentToken{ID: s.randomID("agent"), Name: name, Kind: kind, Scope: accountid.AgentScopeWorkspace, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens(id, token_hash, user_id, scope, name, kind, created_at) VALUES(?,?,?,?,?,?,?)`,
		token.ID, tokenHash(value), actor.ID, token.Scope, token.Name, string(token.Kind), formatTime(now)); err != nil {
		return AgentToken{}, "", err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.agent_token_created", "agent_token", token.ID, token.Name); err != nil {
		return AgentToken{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return AgentToken{}, "", err
	}
	return token, value, nil
}

// AgentTokens lists the actor's tokens, newest first; tokens idle past
// accountid.AgentTokenIdleLimit are gone.
func (s *Service) AgentTokens(ctx context.Context, actor User) ([]AgentToken, error) {
	if actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	if err := deleteIdleAgentTokens(ctx, s.store.db, actor.ID, s.now().UTC()); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, name, kind, scope, created_at, last_used_at FROM agent_tokens
WHERE user_id = ? ORDER BY created_at DESC, id`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []AgentToken{}
	for rows.Next() {
		token, err := scanAgentToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeAgentToken ends one of the actor's own tokens.
func (s *Service) RevokeAgentToken(ctx context.Context, actor User, tokenID string) error {
	if actor.Status != UserStatusActive {
		return ErrForbidden
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var name string
	err = tx.QueryRowContext(ctx, "DELETE FROM agent_tokens WHERE id = ? AND user_id = ? RETURNING name", tokenID, actor.ID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAgentTokenNotFound
	}
	if err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.agent_token_revoked", "agent_token", tokenID, name); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveAgentToken returns who calls with value for scope. It fails with
// ErrAgentTokenInvalid for a token that is unknown, of another scope, idle
// past the limit, or whose account is not active or must first choose a new
// password: the Product Service's guard refuses all of these alike. A use is
// recorded at most every agentTokenTouch.
func (s *Service) ResolveAgentToken(ctx context.Context, value, scope string) (AgentCaller, error) {
	if !accountid.LooksLikeAgentToken(value) {
		return AgentCaller{}, ErrAgentTokenInvalid
	}
	var caller AgentCaller
	var kind, createdAt string
	var lastUsed sql.NullString
	err := s.store.db.QueryRowContext(ctx, `
SELECT t.id, t.name, t.kind, t.scope, t.created_at, t.last_used_at,
       u.id, u.username, u.role, u.status, u.must_change_password, u.nickname, u.avatar_version
FROM agent_tokens t JOIN users u ON u.id = t.user_id
WHERE t.token_hash = ?`, tokenHash(value)).Scan(
		&caller.Token.ID, &caller.Token.Name, &kind, &caller.Token.Scope, &createdAt, &lastUsed,
		&caller.User.ID, &caller.User.Username, &caller.User.Role, &caller.User.Status, &caller.User.MustChangePassword,
		&caller.User.Nickname, &caller.User.AvatarVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentCaller{}, ErrAgentTokenInvalid
	}
	if err != nil {
		return AgentCaller{}, err
	}
	caller.Token.Kind = AgentKind(kind)
	caller.Token.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	lastUse := caller.Token.CreatedAt
	if lastUsed.Valid {
		used, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		caller.Token.LastUsedAt = &used
		lastUse = used
	}
	now := s.now().UTC()
	if accountid.AgentTokenIdle(lastUse, now) {
		_, _ = s.store.db.ExecContext(ctx, "DELETE FROM agent_tokens WHERE id = ?", caller.Token.ID)
		return AgentCaller{}, ErrAgentTokenInvalid
	}
	if caller.Token.Scope != scope || caller.User.Status != UserStatusActive || caller.User.MustChangePassword {
		return AgentCaller{}, ErrAgentTokenInvalid
	}
	if caller.Token.LastUsedAt == nil || now.Sub(*caller.Token.LastUsedAt) >= agentTokenTouch {
		if _, err := s.store.db.ExecContext(ctx, "UPDATE agent_tokens SET last_used_at = ? WHERE id = ?", formatTime(now), caller.Token.ID); err != nil {
			return AgentCaller{}, err
		}
		caller.Token.LastUsedAt = &now
	}
	return caller, nil
}

// deleteIdleAgentTokens forgets the account's tokens nobody used for
// accountid.AgentTokenIdleLimit.
func deleteIdleAgentTokens(ctx context.Context, db sqlExecutor, userID string, now time.Time) error {
	_, err := db.ExecContext(ctx, "DELETE FROM agent_tokens WHERE user_id = ? AND COALESCE(last_used_at, created_at) <= ?",
		userID, formatTime(now.Add(-accountid.AgentTokenIdleLimit)))
	return err
}

func scanAgentToken(rows *sql.Rows) (AgentToken, error) {
	var token AgentToken
	var kind, createdAt string
	var lastUsed sql.NullString
	if err := rows.Scan(&token.ID, &token.Name, &kind, &token.Scope, &createdAt, &lastUsed); err != nil {
		return AgentToken{}, err
	}
	token.Kind = AgentKind(kind)
	token.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsed.Valid {
		used, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		token.LastUsedAt = &used
	}
	return token, nil
}

func validAgentTokenName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > MaxAgentTokenName {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validAgentKind(kind AgentKind) bool {
	switch kind {
	case AgentClaudeCode, AgentCodex, AgentOther:
		return true
	}
	return false
}
