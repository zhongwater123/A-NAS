//go:build linux

package montageservice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// The montage center's assistant socket (docs/architecture/montage-center.md,
// MCP 服务器): systemd holds it as anas-montage-assistant.socket, named
// "assistant", open to every UID in a directory only root enters; it is
// bound only into members' cron units. The peer's UID, which the kernel
// reports, is the member; the Host Agent says which account it is.

// AccountResolver finds the account in use for a member's UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accountid.SessionUser, error)
}

type peerUIDKey struct{}

// withPeerUID records on each connection the UID of the process at the
// other end.
func withPeerUID(ctx context.Context, conn net.Conn) context.Context {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return ctx
	}
	return context.WithValue(ctx, peerUIDKey{}, int(cred.Uid))
}

// authenticatePeer makes the peer's account the request's user.
func authenticatePeer(resolver AccountResolver, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := r.Context().Value(peerUIDKey{}).(int)
		if !ok || uid <= 0 {
			logger.ErrorContext(r.Context(), "montage assistant socket peer is not a member", "uid", uid, "credentials", ok)
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "only members' assistants use this socket")
			return
		}
		user, err := resolver.ResolveAccountUID(r.Context(), uid)
		switch {
		case errors.Is(err, accountid.ErrUserNotFound):
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "account lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "montage_unavailable", "the montage center is not available")
			return
		}
		next.ServeHTTP(w, r.WithContext(accounts.WithUser(r.Context(), accounts.User{ID: user.UserID, Username: user.Username})))
	})
}

// assistantSocket is the listener systemd hands over by the name
// "assistant", or, in development and tests, one on socketPath made open
// to every UID in a directory that must keep out all but the sandboxes.
func assistantSocket(socketPath string) (net.Listener, bool, error) {
	if listeners, err := activation.ListenersWithNames(); err == nil && len(listeners["assistant"]) > 0 {
		return listeners["assistant"][0], true, nil
	}
	if socketPath == "" {
		return nil, false, nil
	}
	if info, err := os.Stat(filepath.Dir(socketPath)); err != nil || info.Mode().Perm()&0o077 != 0 {
		return nil, false, errors.New("the montage assistant socket's directory must be private")
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, false, err
	}
	return listener, false, os.Chmod(socketPath, 0o666)
}

// serveAssistant serves handler on listener until ctx ends.
func serveAssistant(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) {
	server := &http.Server{Handler: handler, ConnContext: withPeerUID,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.ErrorContext(ctx, "montage assistant socket stopped", "error", err)
	}
}
