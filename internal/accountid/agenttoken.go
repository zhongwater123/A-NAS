package accountid

import (
	"errors"
	"strings"
	"time"
)

// Agent tokens are the credentials members give the coding agents on their
// own computers (ADR 0040). The Product Service issues them into control.db;
// the Host Agent resolves them read-only for the one service that may take
// them, and only for its scope.
const (
	// AgentScopeWorkspace is the shared workspace's MCP endpoint, the only
	// thing an agent token reaches.
	AgentScopeWorkspace = "workspace"
	// AgentTokenPrefix starts every agent token, so that secret scanners and
	// people can tell one from a session token.
	AgentTokenPrefix = "anas_ws_"
	// AgentTokenIdleLimit ends a token nobody used for this long.
	AgentTokenIdleLimit = 90 * 24 * time.Hour
)

// ErrAgentTokenNotFound reports a token that is unknown, revoked, idle past
// AgentTokenIdleLimit, of another scope, or whose account may not use it now.
var ErrAgentTokenNotFound = errors.New("agent token is invalid or revoked")

// AgentTokenUser is the account behind an agent token and the token itself.
type AgentTokenUser struct {
	SessionUser
	TokenID   string `json:"tokenId"`
	TokenName string `json:"tokenName"`
	TokenKind string `json:"tokenKind"`
}

// LooksLikeAgentToken reports whether value has an agent token's shape; it
// saves a lookup for what cannot be one.
func LooksLikeAgentToken(value string) bool {
	rest, ok := strings.CutPrefix(value, AgentTokenPrefix)
	if !ok || len(rest) != 64 {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// AgentTokenIdle reports whether a token last used (or created, if never
// used) at lastUse has gone unused past AgentTokenIdleLimit at now.
func AgentTokenIdle(lastUse, now time.Time) bool {
	return !now.Before(lastUse.Add(AgentTokenIdleLimit))
}
