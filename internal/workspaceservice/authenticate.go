// Package workspaceservice runs the shared workspace as its own service
// (anas-workspace.service, ADR 0040): the Product Service forwards
// /api/v1/workspace with the browser's session token or an agent's token,
// and the service confirms either with the Host Agent itself.
package workspaceservice

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/workspace"
	"github.com/zhongwater123/A-NAS/internal/workspaceapi"
)

// Directory is the Host Agent's lookup for the workspace: sessions, the
// active accounts, deletions, and agent tokens of the workspace's scope
// (sessionlookup.Client on a NAS, sessiondir.Directory in development).
type Directory interface {
	ResolveSessionUser(context.Context, string) (accountid.SessionUser, error)
	ActiveProfiles(context.Context) ([]accountid.Profile, error)
	AccountDeletion(context.Context, string) (accountid.AccountDeletion, error)
	ResolveAgentToken(ctx context.Context, token, scope string) (accountid.AgentTokenUser, error)
	AgentTokenByID(ctx context.Context, id, scope string) (accountid.AgentTokenUser, error)
}

// Authenticate confirms who calls before next runs, trusting only the
// directory's answer, never the Product Service's word (ADR 0011):
//
//   - an agent's request, which carries workspaceapi.AgentTokenHeader, by
//     its token;
//   - a one-time transfer link by nothing here: the link's handler uses it
//     up and checks the token that asked for it;
//   - every other request by the session check every store service uses
//     (modules.Authenticate).
//
// Names come from the accounts' profiles, read at most every 30 seconds.
func Authenticate(directory Directory, logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	names := &profileNames{directory: directory}
	member := modules.Authenticate(directory, "workspace", workspaceapi.Unavailable, logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := accounts.UserFrom(r.Context())
		next.ServeHTTP(w, r.WithContext(workspace.WithCaller(r.Context(), workspace.Caller{
			Actor: workspace.Actor{AccountID: user.ID, Name: names.name(r.Context(), user.ID, user.Username), Admin: user.Role == accountid.RoleAdmin},
			Via:   workspace.Web,
		})))
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(workspaceapi.AgentTokenHeader)
		switch {
		case strings.HasPrefix(r.URL.Path, workspaceapi.TransfersPrefix):
			next.ServeHTTP(w, r)
			return
		case token == "":
			member.ServeHTTP(w, r)
			return
		}
		user, err := directory.ResolveAgentToken(r.Context(), token, accountid.AgentScopeWorkspace)
		if errors.Is(err, accountid.ErrAgentTokenNotFound) {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "the agent token is invalid or revoked")
			return
		}
		if err != nil {
			logger.WarnContext(r.Context(), "workspace agent token lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, workspaceapi.Unavailable, "the account lookup is unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(workspace.WithCaller(r.Context(), workspace.Caller{
			Actor:  workspace.Actor{AccountID: user.UserID, Name: names.name(r.Context(), user.UserID, user.Username), Admin: user.Role == accountid.RoleAdmin},
			Via:    workspace.Via{Kind: viaKind(user.TokenKind), Name: user.TokenName, TokenID: user.TokenID},
			Origin: r.Header.Get(workspaceapi.OriginHeader),
		})))
	})
}

func viaKind(kind string) workspace.ViaKind {
	switch workspace.ViaKind(kind) {
	case workspace.ViaClaudeCode, workspace.ViaCodex:
		return workspace.ViaKind(kind)
	}
	return workspace.ViaOther
}

// profileNames remembers the names accounts are shown by.
type profileNames struct {
	directory Directory
	mu        sync.Mutex
	read      time.Time
	names     map[string]string
}

// name is the account's nickname, else its username. A failed read waits as
// long as a good one before the next try.
func (p *profileNames) name(ctx context.Context, accountID, username string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.read) > 30*time.Second {
		p.read = time.Now()
		if profiles, err := p.directory.ActiveProfiles(ctx); err == nil {
			p.names = map[string]string{}
			for _, profile := range profiles {
				if profile.Nickname != "" {
					p.names[profile.ID] = profile.Nickname
				}
			}
		}
	}
	if name := p.names[accountID]; name != "" {
		return name
	}
	return username
}
