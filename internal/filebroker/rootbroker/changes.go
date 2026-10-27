package rootbroker

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

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// AccountResolver names the active account behind a Linux UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accountid.SessionUser, error)
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
func (s *Server) memberFiles(identity accountid.Identity) (filesystem.FileSystem, error) {
	if !s.config.VolumeReady() {
		return nil, filesystem.ErrVolumeUnavailable
	}
	if !accountid.ValidAssistantAccount(identity.Username) {
		return nil, filesystem.ErrForbidden
	}
	key := workerKey{uid: identity.UID, role: identity.Role, confined: true}
	w, err := s.takeWorker(context.Background(), key, identity)
	if err != nil {
		return nil, err
	}
	s.returnWorker(w)
	return &wire.FileOps{Call: func(_ context.Context, req wire.Request) (wire.Response, *os.File, error) {
		// A change goes on to its end even if the call applying it goes
		// away: stopping it halfway would leave it half applied.
		ctx := context.Background()
		w, err := s.takeWorker(ctx, key, identity)
		if err != nil {
			return wire.Response{}, nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
		}
		var entries wire.Pages
		result, file, err := w.call(ctx, req, entries.Add)
		if err != nil {
			s.dropWorker(w)
			return wire.Response{}, nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
		}
		s.returnWorker(w)
		if result.Error != nil {
			wire.CloseFile(file)
			return wire.Response{}, nil, result.Error
		}
		return entries.Complete(result), file, nil
	}}, nil
}

// serveChange carries out the Product Service's request for the member whose
// session it carries.
func (s *Server) serveChange(req wire.Request) wire.Response {
	if s.changes == nil || req.Change == nil {
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the assistant's changes are not enabled"}}
	}
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		return wire.Response{Error: refused}
	}
	var card json.RawMessage
	var err error
	switch req.Op {
	case wire.OpChangePrepare:
		var member filesystem.FileSystem
		if member, err = s.memberFiles(identity); err == nil {
			card, err = s.changes.Prepare(ctx, identity.Username, member, req.Change.Arguments, req.Change.SessionID, req.Change.RequestID)
		}
	case wire.OpChangeDecide:
		card, err = s.changes.Decide(identity.Username, req.Change.ID, req.Change.Decision)
	default:
		card, err = s.changes.Get(identity.Username, req.Change.ID)
	}
	switch {
	case errors.Is(err, assistantchanges.ErrInvalid):
		return wire.Response{Error: &wire.Error{Code: wire.CodeChangeInvalid, Message: assistantchanges.Reasons(err)}}
	case errors.Is(err, assistantchanges.ErrDecided):
		return wire.Response{Error: &wire.Error{Code: wire.CodeChangeDecided}}
	case errors.Is(err, assistantchanges.ErrNotFound):
		return wire.Response{Error: &wire.Error{Code: wire.CodeNotFound}}
	case err != nil:
		s.config.Logger.Error("assistant change failed", "username", identity.Username, "op", req.Op, "error", err)
		return wire.Response{Error: encodeError(err)}
	}
	return wire.Response{Change: card}
}

type peerKey struct{}

// ServeAssistantChanges answers the assistants' MCP tool on listener, a socket
// bound into every assistant's sandbox: the member is the caller's UID.
func (s *Server) ServeAssistantChanges(listener net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apply", s.withAssistant(func(w http.ResponseWriter, r *http.Request, identity accountid.Identity) {
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
	mux.HandleFunc("GET /recent", s.withAssistant(func(w http.ResponseWriter, _ *http.Request, identity accountid.Identity) {
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
func (s *Server) withAssistant(next func(http.ResponseWriter, *http.Request, accountid.Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(peerKey{}).(int)
		if s.changes == nil || s.config.Accounts == nil || uid < accountid.FirstUserUID || uid > accountid.LastUserUID {
			http.Error(w, "not available", http.StatusForbidden)
			return
		}
		user, err := s.config.Accounts.ResolveAccountUID(r.Context(), uid)
		switch {
		case errors.Is(err, accountid.ErrUserNotFound):
			http.Error(w, "not available", http.StatusForbidden)
			return
		case err != nil:
			s.config.Logger.Warn("account directory unavailable", "error", err)
			http.Error(w, "accounts cannot be verified right now", http.StatusServiceUnavailable)
			return
		}
		next(w, r, accountid.Identity{Username: user.Username, Role: user.Role, UID: uid, Enabled: true})
	}
}

func writeText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, text)
}
