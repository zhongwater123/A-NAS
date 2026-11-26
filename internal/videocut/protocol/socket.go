//go:build linux

package protocol

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// VideoCut's assistant socket (docs/architecture/montage-planning-session.md):
// systemd holds it as anas-videocut-assistant.socket, named "assistant", open
// to every UID in a directory only root enters; it is bound only into
// members' planning instances. The peer's UID, which the kernel reports, is
// the member; the Host Agent says which account it is. Root's planner, the
// one peer outside a sandbox, names the account it plans for instead
// (AccountHeader).

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

// AccountHeader names the account root's planner claims and reports a round
// for. Only a root peer is heard naming one: only root reaches the socket
// outside the sandboxes, whose processes all run as their members.
const AccountHeader = "X-A-NAS-Account"

// UIDOf is the Linux UID of the account name, from the system's user database.
func UIDOf(name string) (int, error) {
	found, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(found.Uid)
}

// authenticatePeer makes the peer's account the request's user: a member's
// by the peer's UID, or, for a root peer, the account it names, which must
// be one in use under that name. uidOf finds the UID of a name.
func authenticatePeer(resolver AccountResolver, uidOf func(string) (int, error), logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := r.Context().Value(peerUIDKey{}).(int)
		named := ""
		if ok && uid == 0 {
			named = r.Header.Get(AccountHeader)
			if !accountid.ValidAssistantAccount(named) {
				logger.ErrorContext(r.Context(), "root named no account on the VideoCut assistant socket")
				httpjson.WriteError(w, http.StatusForbidden, "forbidden", "root names the account it plans for")
				return
			}
			found, err := uidOf(named)
			if err != nil || found <= 0 {
				httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
				return
			}
			uid = found
		}
		if !ok || uid <= 0 {
			logger.ErrorContext(r.Context(), "VideoCut assistant socket peer is not a member", "uid", uid, "credentials", ok)
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "only members' assistants use this socket")
			return
		}
		account, err := resolver.ResolveAccountUID(r.Context(), uid)
		switch {
		case errors.Is(err, accountid.ErrUserNotFound), err == nil && named != "" && account.Username != named:
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "account lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "videocut_unavailable", "VideoCut is not available")
			return
		}
		next.ServeHTTP(w, r.WithContext(accounts.WithUser(r.Context(), accounts.User{ID: account.UserID, Username: account.Username})))
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
		return nil, false, errors.New("the VideoCut assistant socket's directory must be private")
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, false, err
	}
	return listener, false, os.Chmod(socketPath, 0o666)
}

// shutdownGrace is how long stopping waits for the assistant socket's open
// calls.
const shutdownGrace = 15 * time.Second

// serveAssistant serves handler on listener until ctx ends.
func serveAssistant(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) {
	server := &http.Server{Handler: handler, ConnContext: withPeerUID,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.ErrorContext(ctx, "VideoCut assistant socket stopped", "error", err)
	}
	<-stopped
}

// AssistantGate answers the assistant socket with the API of the store that
// is open, and unavailable while none is: the socket is systemd's and
// outlives every store. Taking a store out of service waits for the calls it
// is answering, so that none finds its database closed.
type AssistantGate struct {
	mu  sync.RWMutex
	api http.Handler
}

func (g *AssistantGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.api == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "VideoCut is not available")
		return
	}
	g.api.ServeHTTP(w, r)
}

// Serve makes api what the socket answers with; nil takes it away once the
// calls in progress are answered.
func (g *AssistantGate) Serve(api http.Handler) {
	g.mu.Lock()
	g.api = api
	g.mu.Unlock()
}

// ListenAssistant serves handler on the assistant socket, when the service
// resolves members by UID (accounts) and has a socket, until ctx ends; what
// it returns waits for that.
func ListenAssistant(ctx context.Context, accounts AccountResolver, socketPath string, logger *slog.Logger, handler http.Handler) (func(), error) {
	if accounts == nil {
		return func() {}, nil
	}
	listener, _, err := assistantSocket(socketPath)
	if err != nil || listener == nil {
		return func() {}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	served := make(chan struct{})
	go func() {
		defer close(served)
		serveAssistant(ctx, listener, authenticatePeer(accounts, UIDOf, logger, handler), logger)
	}()
	return func() {
		cancel()
		<-served
	}, nil
}
