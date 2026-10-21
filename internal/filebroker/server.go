package filebroker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/assistantlogin"
)

// SessionResolver verifies a session token and returns its Linux identity.
type SessionResolver interface {
	ResolveSessionIdentity(context.Context, string) (accounts.Identity, error)
}

type Config struct {
	Sessions   SessionResolver
	VolumeRoot string
	// VolumeReady must report whether the intended data volume is mounted;
	// the broker refuses every operation otherwise.
	VolumeReady func() bool
	IdleTimeout time.Duration
	Logger      *slog.Logger
	// Command builds a worker process. The default re-executes the running
	// binary with WorkerArgument.
	Command func(volumeRoot string) *exec.Cmd
	// ProbeCommand builds the identity switch probe. The default re-executes
	// the running binary with ProbeArgument.
	ProbeCommand func() *exec.Cmd
	// LookupUID resolves a username to its UID so a worker never starts for an
	// account the Host Agent did not provision. The default uses os/user.
	LookupUID func(username string) (int, error)
	// ShellCommand builds the administrator's terminal shell; the default is
	// a login bash, or sh where bash is missing.
	ShellCommand func() *exec.Cmd
	// TerminalRecheck is how often a running terminal's session, or an open
	// connection to an assistant, is verified.
	TerminalRecheck time.Duration
	// AssistantSockets holds each member's assistant socket, <username>.sock,
	// which only root may connect to; empty turns the assistant off (ADR 0024).
	AssistantSockets string
	// AssistantTokens holds <username>.env with the instance's
	// HERMES_DASHBOARD_SESSION_TOKEN, readable by root alone.
	AssistantTokens string
	// AssistantChanges holds what the executor of the assistants' changes
	// keeps, readable by root alone; empty turns the executor off.
	AssistantChanges string
	// Accounts names the account behind an assistant's UID.
	Accounts AccountResolver

	// skipCredentials lets unprivileged tests run workers as the test user.
	skipCredentials bool
}

type Server struct {
	config  Config
	mu      sync.Mutex
	workers map[workerKey]*worker
	// assistants are the broker's copies of the assistant connections it
	// handed out, cut when the broker stops.
	assistants map[*net.UnixConn]struct{}
	// logins keeps each member's access token to their assistant.
	logins assistantlogin.Cache
	// changes executes the assistants' changes; nil when off.
	changes     *assistantchanges.Executor
	closed      bool
	shareMu     sync.Mutex
	shareEpoch  string
	shareFenced map[string]bool
}

type workerKey struct {
	uid  int
	role accounts.Role
	// confined workers apply the assistant's changes: Landlock lets them write
	// only the member's own places (ADR 0024 decision 4).
	confined bool
}

type worker struct {
	key      workerKey
	cmd      *exec.Cmd
	conn     *net.UnixConn
	mu       sync.Mutex
	lastUsed time.Time
	exited   chan struct{}
}

func NewServer(config Config) (*Server, error) {
	if config.Sessions == nil || config.VolumeReady == nil || config.VolumeRoot == "" {
		return nil, errors.New("file broker needs sessions, a volume root, and a volume check")
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 5 * time.Minute
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Command == nil || config.ProbeCommand == nil {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate file worker binary: %w", err)
		}
		if config.Command == nil {
			config.Command = func(volumeRoot string) *exec.Cmd {
				return exec.Command(executable, WorkerArgument, volumeRoot)
			}
		}
		if config.ProbeCommand == nil {
			config.ProbeCommand = func() *exec.Cmd { return exec.Command(executable, ProbeArgument) }
		}
	}
	if config.ShellCommand == nil {
		config.ShellCommand = func() *exec.Cmd {
			if _, err := os.Stat("/bin/bash"); err == nil {
				return exec.Command("/bin/bash", "-l")
			}
			return exec.Command("/bin/sh", "-l")
		}
	}
	if config.TerminalRecheck <= 0 {
		config.TerminalRecheck = 30 * time.Second
	}
	if config.LookupUID == nil {
		config.LookupUID = func(username string) (int, error) {
			account, err := user.Lookup(username)
			if err != nil {
				return 0, err
			}
			return strconv.Atoi(account.Uid)
		}
	}
	server := &Server{config: config, workers: make(map[workerKey]*worker), assistants: make(map[*net.UnixConn]struct{})}
	if config.AssistantChanges != "" {
		server.changes = &assistantchanges.Executor{Root: config.AssistantChanges, Volume: config.VolumeRoot}
	}
	return server, nil
}

// Serve accepts Product Service connections until the listener closes.
func (s *Server) Serve(listener net.Listener) error {
	stop := make(chan struct{})
	defer close(stop)
	go s.reapIdleWorkers(stop)
	for {
		connection, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		conn, ok := connection.(*net.UnixConn)
		if !ok {
			_ = connection.Close()
			continue
		}
		go s.handle(conn)
	}
}

// Close stops every worker and cuts every assistant connection.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	workers := s.workers
	s.workers = make(map[workerKey]*worker)
	assistants := s.assistants
	s.assistants = make(map[*net.UnixConn]struct{})
	s.mu.Unlock()
	for _, w := range workers {
		w.stop()
	}
	for instance := range assistants {
		cut(instance)
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var req request
	extra, err := readMessage(conn, &req)
	closeFile(extra)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch req.Op {
	case opTerminal:
		s.serveTerminal(conn, req)
		return
	case opAssistant:
		s.serveAssistant(conn, req)
		return
	case opChangePrepare, opChangeDecide, opChangeGet:
		result := s.serveChange(req)
		if err := writeMessage(conn, result, nil); err != nil {
			s.config.Logger.Warn("file broker reply failed", "op", req.Op, "error", err)
		}
		return
	}
	result, file := s.dispatch(context.Background(), req)
	if err := writeMessage(conn, result, file); err != nil {
		s.config.Logger.Warn("file broker reply failed", "op", req.Op, "error", err)
	}
	closeFile(file)
}

func (s *Server) dispatch(ctx context.Context, req request) (response, *os.File) {
	// This gate lives with the worker, so a Product Service crash cannot
	// release a publication that a restarted sharing service has fenced.
	if req.Op == opShareReset || req.Op == opShareFence || req.Op == opShareCheck {
		return s.shareControl(req), nil
	}
	if req.ShareEpoch != "" || req.ShareID != "" {
		s.shareMu.Lock()
		defer s.shareMu.Unlock()
		if req.ShareEpoch == "" || req.ShareEpoch != s.shareEpoch || req.ShareID == "" || s.shareFenced[req.ShareID] {
			return response{Error: &wireError{Code: codeForbidden}}, nil
		}
	}
	if !knownOps[req.Op] {
		return response{Error: &wireError{Code: codeInvalidRequest, Message: "unknown file operation"}}, nil
	}
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		return response{Error: refused}, nil
	}
	if !s.config.VolumeReady() {
		return response{Error: &wireError{Code: codeVolumeUnavailable}}, nil
	}
	req.Token = ""
	w, err := s.worker(identity)
	if err != nil {
		s.config.Logger.Error("file worker unavailable", "username", identity.Username, "error", err)
		return response{Error: &wireError{Code: codeUnavailable}}, nil
	}
	result, file, err := w.call(req)
	if err != nil {
		s.config.Logger.Error("file worker failed", "username", identity.Username, "op", req.Op, "error", err)
		s.drop(w)
		return response{Error: &wireError{Code: codeUnavailable}}, nil
	}
	return result, file
}

// identify verifies the session behind a token. A session that is gone,
// expired or whose user may not act is unauthenticated. A session directory
// that cannot be read right now, as while SQLite is busy, is unavailable
// instead, so a passing error never reads as a signed-out user (#166).
func (s *Server) identify(ctx context.Context, token string) (accounts.Identity, *wireError) {
	identity, err := s.config.Sessions.ResolveSessionIdentity(ctx, token)
	switch {
	case errors.Is(err, accounts.ErrSessionNotFound):
		return accounts.Identity{}, &wireError{Code: codeUnauthenticated}
	case err != nil:
		s.config.Logger.Warn("session directory unavailable", "error", err)
		return accounts.Identity{}, &wireError{Code: codeUnavailable, Message: "sessions cannot be verified right now"}
	case !identity.Enabled:
		return accounts.Identity{}, &wireError{Code: codeUnauthenticated}
	}
	return identity, nil
}

// unconfirmedRechecks is how many rechecks in a row may fail to read the
// session directory before a terminal or assistant connection ends: two
// minutes at the default interval.
const unconfirmedRechecks = 4

// recheck reports whether a terminal or an assistant connection may go on.
// A session that is gone, or no longer passes keep, ends it at once. One the
// directory cannot confirm ends it only after unconfirmedRechecks such
// rechecks in a row, which unconfirmed counts: a passing error does not hang
// up a working shell, and a directory that stays unreadable does not keep a
// revoked one alive.
func (s *Server) recheck(ctx context.Context, token string, unconfirmed *int, keep func(accounts.Identity) bool) bool {
	current, err := s.config.Sessions.ResolveSessionIdentity(ctx, token)
	if err != nil && !errors.Is(err, accounts.ErrSessionNotFound) {
		*unconfirmed++
		s.config.Logger.Warn("session cannot be confirmed", "error", err, "rechecks", *unconfirmed)
		return *unconfirmed < unconfirmedRechecks
	}
	*unconfirmed = 0
	return err == nil && current.Enabled && keep(current)
}

func (s *Server) worker(identity accounts.Identity) (*worker, error) {
	return s.workerFor(workerKey{uid: identity.UID, role: identity.Role}, identity)
}

func (s *Server) workerFor(key workerKey, identity accounts.Identity) (*worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("file broker is closed")
	}
	if current := s.workers[key]; current != nil {
		select {
		case <-current.exited:
			delete(s.workers, key)
		default:
			return current, nil
		}
	}
	credential, err := s.credential(identity)
	if err != nil {
		return nil, err
	}
	var env []string
	if key.confined {
		env = []string{ConfineVariable + "=" + strings.Join(s.writablePlaces(identity.Username), ":")}
	}
	w, err := s.spawn(key, credential, env)
	if err != nil {
		return nil, err
	}
	s.workers[key] = w
	return w, nil
}

// credential maps an identity to the worker's UID and groups. Only accounts
// in the A-NAS range that the Host Agent provisioned under the same UID can
// get a worker; root and system accounts never can.
func (s *Server) credential(identity accounts.Identity) (*syscall.Credential, error) {
	if identity.UID < accounts.FirstUserUID || identity.UID > accounts.LastUserUID {
		return nil, fmt.Errorf("UID %d is outside the A-NAS range", identity.UID)
	}
	uid, err := s.config.LookupUID(identity.Username)
	if err != nil || uid != identity.UID {
		return nil, fmt.Errorf("Linux account %s is not provisioned with UID %d", identity.Username, identity.UID)
	}
	groups := []uint32{accounts.UsersGID}
	if identity.Role == accounts.RoleAdmin {
		groups = append(groups, accounts.AdminsGID)
	}
	return &syscall.Credential{Uid: uint32(identity.UID), Gid: uint32(identity.UID), Groups: groups}, nil
}

func (s *Server) spawn(key workerKey, credential *syscall.Credential, env []string) (*worker, error) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "file-broker")
	child := os.NewFile(uintptr(pair[1]), "file-worker")
	defer child.Close()
	cmd := s.config.Command(s.config.VolumeRoot)
	cmd.ExtraFiles = []*os.File{child}
	cmd.Env = append([]string{}, env...)
	cmd.Dir = "/"
	cmd.Stderr = os.Stderr
	if s.config.skipCredentials {
		credential = nil
	}
	cmd.SysProcAttr = launchAttributes(credential)
	if err := cmd.Start(); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("start file worker: %w", err)
	}
	connection, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	w := &worker{key: key, cmd: cmd, conn: connection.(*net.UnixConn), lastUsed: time.Now(), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(w.exited)
	}()
	return w, nil
}

func (w *worker) call(req request) (response, *os.File, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastUsed = time.Now()
	if err := writeMessage(w.conn, req, nil); err != nil {
		return response{}, nil, err
	}
	var result response
	file, err := readMessage(w.conn, &result)
	w.lastUsed = time.Now()
	return result, file, err
}

func (w *worker) stop() {
	_ = w.conn.Close()
	select {
	case <-w.exited:
	case <-time.After(2 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.exited
	}
}

func (s *Server) drop(w *worker) {
	s.mu.Lock()
	if s.workers[w.key] == w {
		delete(s.workers, w.key)
	}
	s.mu.Unlock()
	go w.stop()
}

func (s *Server) reapIdleWorkers(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.reapIdle(time.Now())
		}
	}
}

func (s *Server) reapIdle(now time.Time) {
	s.mu.Lock()
	var idle []*worker
	for key, w := range s.workers {
		if !w.mu.TryLock() {
			continue
		}
		if now.Sub(w.lastUsed) >= s.config.IdleTimeout {
			idle = append(idle, w)
			delete(s.workers, key)
		}
		w.mu.Unlock()
	}
	s.mu.Unlock()
	for _, w := range idle {
		w.stop()
	}
}
