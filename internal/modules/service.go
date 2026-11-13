package modules

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/coreos/go-systemd/v22/activation"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// SessionResolver confirms who stands behind a forwarded session and lists
// the active members: the Host Agent's lookup (sessionlookup.Client) in a
// module process, the Product Service's own accounts in development.
type SessionResolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}

// Authenticate trusts only the resolver's answer about the forwarded session,
// never the Product Service (ADR 0037 decision 3). The handler finds the
// caller with accounts.UserFrom; Status is set so that ActiveAdministrator
// holds for an administrator, since the lookup only names active accounts.
func Authenticate(sessions SessionResolver, unavailable string, logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(serviceproxy.SessionHeader)
		user, err := sessions.ResolveSessionUser(r.Context(), token)
		switch {
		case errors.Is(err, accounts.ErrSessionNotFound) || err == nil && user.UserID == "":
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.WarnContext(r.Context(), "module session lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, unavailable, "session lookup unavailable")
			return
		case user.MustChangePassword:
			httpjson.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		ctx := accounts.WithSessionToken(r.Context(), token)
		ctx = accounts.WithUser(ctx, accounts.User{
			ID: user.UserID, Username: user.Username, Role: user.Role, Status: accounts.UserStatusActive,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ErrNoListener reports a module started without its socket unit.
var ErrNoListener = errors.New("module started without the socket systemd should hand it")

// Listener returns the API socket systemd passes to the module (its
// anas-module-<id>.socket). In development and tests, when socketPath is not
// empty, the module listens there itself instead.
func Listener(socketPath string) (net.Listener, error) {
	if socketPath != "" {
		if info, err := os.Lstat(socketPath); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return nil, errors.New("module socket path is not a socket")
			}
			if err := os.Remove(socketPath); err != nil {
				return nil, err
			}
		}
		return net.Listen("unix", socketPath)
	}
	listeners, err := activation.Listeners()
	if err != nil {
		return nil, err
	}
	if len(listeners) != 1 || listeners[0] == nil {
		for _, listener := range listeners {
			if listener != nil {
				_ = listener.Close()
			}
		}
		return nil, ErrNoListener
	}
	return listeners[0], nil
}
