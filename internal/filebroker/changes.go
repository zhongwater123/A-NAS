package filebroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// The executor of 提交更改 (ADR 0024 decision 4). The Product Service
// prepares, approves, rejects and expires changes with the member's session,
// which the broker checks itself (decision 10); the assistant's MCP tool
// applies them on the assistant socket, where the member is the caller's UID.
// Every file is read and written by a worker running as the member and
// confined by Landlock to the member's own places.
const (
	opChangePrepare = "change_prepare"
	opChangeDecide  = "change_decide"
	opChangeGet     = "change_get"
)

// AccountResolver names the active account behind a Linux UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accounts.SessionUser, error)
}

// ConfineVariable carries the places a confined worker may write, separated by
// colons; everything else on the data volume it may only read.
const ConfineVariable = "ANAS_FILE_WORKER_WRITABLE"

// writablePlaces are where the assistant's changes for a member may write:
// the member's space (with its recycle bin), their Agent空间 folder and their
// recycle bin in the shared space.
func (s *Server) writablePlaces(username string) []string {
	return []string{
		filepath.Join(s.config.VolumeRoot, "spaces", "private", username),
		filepath.Join(s.config.VolumeRoot, "spaces", "shared", "Agent空间", username),
		filepath.Join(s.config.VolumeRoot, "spaces", "shared", ".a-nas-trash", username),
	}
}

// memberFiles is the member's file system through their confined worker.
func (s *Server) memberFiles(identity accounts.Identity) (files.FileSystem, error) {
	if !s.config.VolumeReady() {
		return nil, files.ErrVolumeUnavailable
	}
	if !assistantAccount.MatchString(identity.Username) {
		return nil, files.ErrForbidden
	}
	key := workerKey{uid: identity.UID, role: identity.Role, confined: true}
	w, err := s.takeWorker(context.Background(), key, identity)
	if err != nil {
		return nil, err
	}
	s.returnWorker(w)
	return &fileOps{call: func(_ context.Context, req request) (response, *os.File, error) {
		// A change goes on to its end even if the call applying it goes
		// away: stopping it halfway would leave it half applied.
		ctx := context.Background()
		w, err := s.takeWorker(ctx, key, identity)
		if err != nil {
			return response{}, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		var entries pages
		result, file, err := w.call(ctx, req, entries.add)
		if err != nil {
			s.dropWorker(w)
			return response{}, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		s.returnWorker(w)
		if result.Error != nil {
			closeFile(file)
			return response{}, nil, result.Error
		}
		return entries.complete(result), file, nil
	}}, nil
}

// serveChange carries out the Product Service's request for the member whose
// session it carries.
func (s *Server) serveChange(req request) response {
	if s.changes == nil || req.Change == nil {
		return response{Error: &wireError{Code: codeUnavailable, Message: "the assistant's changes are not enabled"}}
	}
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		return response{Error: refused}
	}
	var card json.RawMessage
	var err error
	switch req.Op {
	case opChangePrepare:
		var member files.FileSystem
		if member, err = s.memberFiles(identity); err == nil {
			card, err = s.changes.Prepare(ctx, identity.Username, member, req.Change.Arguments, req.Change.SessionID, req.Change.RequestID)
		}
	case opChangeDecide:
		card, err = s.changes.Decide(identity.Username, req.Change.ID, req.Change.Decision)
	default:
		card, err = s.changes.Get(identity.Username, req.Change.ID)
	}
	switch {
	case errors.Is(err, assistantchanges.ErrInvalid):
		return response{Error: &wireError{Code: codeChangeInvalid, Message: err.Error()}}
	case errors.Is(err, assistantchanges.ErrDecided):
		return response{Error: &wireError{Code: codeChangeDecided}}
	case errors.Is(err, assistantchanges.ErrNotFound):
		return response{Error: &wireError{Code: codeNotFound}}
	case err != nil:
		s.config.Logger.Error("assistant change failed", "username", identity.Username, "op", req.Op, "error", err)
		return response{Error: encodeError(err)}
	}
	return response{Change: card}
}

type peerKey struct{}

// ServeAssistantChanges answers the assistants' MCP tool on listener, a socket
// bound into every assistant's sandbox: the member is the caller's UID.
func (s *Server) ServeAssistantChanges(listener net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apply", s.withAssistant(func(w http.ResponseWriter, r *http.Request, identity accounts.Identity) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "请求读取失败", http.StatusBadRequest)
			return
		}
		member, err := s.memberFiles(identity)
		if err != nil {
			s.config.Logger.Error("assistant change worker unavailable", "username", identity.Username, "error", err)
			writeText(w, "暂时无法执行更改，什么都没有改变。")
			return
		}
		writeText(w, s.changes.Apply(r.Context(), identity.Username, member, body))
	}))
	mux.HandleFunc("GET /recent", s.withAssistant(func(w http.ResponseWriter, _ *http.Request, identity accounts.Identity) {
		writeText(w, s.changes.Recent(identity.Username))
	}))
	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if unixConn, ok := conn.(*net.UnixConn); ok {
				if raw, err := unixConn.SyscallConn(); err == nil {
					var uid = -1
					_ = raw.Control(func(fd uintptr) {
						if cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); err == nil {
							uid = int(cred.Uid)
						}
					})
					return context.WithValue(ctx, peerKey{}, uid)
				}
			}
			return ctx
		},
	}
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// withAssistant finds the member behind the calling UID: an active account in
// the A-NAS range, never root or a system account.
func (s *Server) withAssistant(next func(http.ResponseWriter, *http.Request, accounts.Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(peerKey{}).(int)
		if s.changes == nil || s.config.Accounts == nil || uid < accounts.FirstUserUID || uid > accounts.LastUserUID {
			http.Error(w, "not available", http.StatusForbidden)
			return
		}
		user, err := s.config.Accounts.ResolveAccountUID(r.Context(), uid)
		switch {
		case errors.Is(err, accounts.ErrUserNotFound):
			http.Error(w, "not available", http.StatusForbidden)
			return
		case err != nil:
			s.config.Logger.Warn("account directory unavailable", "error", err)
			http.Error(w, "accounts cannot be verified right now", http.StatusServiceUnavailable)
			return
		}
		next(w, r, accounts.Identity{Username: user.Username, Role: user.Role, UID: uid, Enabled: true})
	}
}

func writeText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, text)
}

// PrepareChange asks the executor to prepare a call of 提交更改 for the
// member signed in on the context's session; it returns the card.
func (c *Client) PrepareChange(ctx context.Context, sessionID, requestID string, arguments json.RawMessage) (json.RawMessage, error) {
	return c.change(ctx, opChangePrepare, &changeRequest{SessionID: sessionID, RequestID: requestID, Arguments: arguments})
}

// DecideChange approves, rejects or expires one of the member's changes.
func (c *Client) DecideChange(ctx context.Context, id, decision string) (json.RawMessage, error) {
	return c.change(ctx, opChangeDecide, &changeRequest{ID: id, Decision: decision})
}

// GetChange returns one of the member's changes as its card shows it.
func (c *Client) GetChange(ctx context.Context, id string) (json.RawMessage, error) {
	return c.change(ctx, opChangeGet, &changeRequest{ID: id})
}

func (c *Client) change(ctx context.Context, op string, change *changeRequest) (json.RawMessage, error) {
	result, file, err := c.call(ctx, request{Op: op, Change: change})
	closeFile(file)
	if err != nil {
		return nil, err
	}
	return result.Change, nil
}
