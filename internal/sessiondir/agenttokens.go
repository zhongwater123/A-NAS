package sessiondir

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// ResolveAgentToken returns the account behind an agent token of scope
// (ADR 0040). It answers accountid.ErrAgentTokenNotFound for a token that is
// unknown, of another scope, idle past accountid.AgentTokenIdleLimit, or
// whose account is not active or must first choose a new password. Like
// sessions it only reads: the Product Service records uses and forgets idle
// tokens.
func (d *Directory) ResolveAgentToken(ctx context.Context, token, scope string) (accountid.AgentTokenUser, error) {
	if !accountid.LooksLikeAgentToken(token) {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	return d.agentToken(ctx, "t.token_hash = ?", accountid.TokenHash(token), scope)
}

// AgentTokenByID confirms that the token with id still lets its account use
// scope, for what the token started earlier, such as a one-time transfer
// link (ADR 0040 decision 6). It answers as ResolveAgentToken does.
func (d *Directory) AgentTokenByID(ctx context.Context, id, scope string) (accountid.AgentTokenUser, error) {
	if id == "" {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	return d.agentToken(ctx, "t.id = ?", id, scope)
}

func (d *Directory) agentToken(ctx context.Context, where, key, scope string) (accountid.AgentTokenUser, error) {
	db, err := d.open()
	if err != nil {
		return accountid.AgentTokenUser{}, err
	}
	var user accountid.AgentTokenUser
	var tokenScope, createdAt string
	var lastUsed sql.NullString
	var status accountid.UserStatus
	err = db.QueryRowContext(ctx, `
SELECT t.id, t.name, t.kind, t.scope, t.created_at, t.last_used_at,
       u.id, u.username, u.role, u.status, u.must_change_password
FROM agent_tokens t JOIN users u ON u.id = t.user_id
WHERE `+where, key).Scan(&user.TokenID, &user.TokenName, &user.TokenKind, &tokenScope, &createdAt, &lastUsed,
		&user.UserID, &user.Username, &user.Role, &status, &user.MustChangePassword)
	if errors.Is(err, sql.ErrNoRows) {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	if err != nil {
		return accountid.AgentTokenUser{}, fmt.Errorf("read agent tokens: %w", err)
	}
	lastUse, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	if lastUsed.Valid {
		if used, err := time.Parse(time.RFC3339Nano, lastUsed.String); err == nil {
			lastUse = used
		}
	}
	if tokenScope != scope || status != accountid.UserStatusActive || user.MustChangePassword || accountid.AgentTokenIdle(lastUse, d.now().UTC()) {
		return accountid.AgentTokenUser{}, accountid.ErrAgentTokenNotFound
	}
	return user, nil
}
