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
	"slices"
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
	config Config
	mu     sync.Mutex
	pools  map[workerKey]*pool
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

// workersPerUser bounds the workers one account's Web operations run in at
// once. A long copy, delete or scan holds one worker; listing, previews and
// new uploads go on in the others, and a burst of calls waits for a free
// worker instead of starting processes without limit.
const workersPerUser = 4

// pool holds an account's workers, each serving one call at a time.
type pool struct {
	workers []*worker
	// freed is closed, and replaced, when a worker becomes free or leaves,
	// waking the calls that wait for one.
	freed chan struct{}
}

func (p *pool) wake() {
	close(p.freed)
	p.freed = make(chan struct{})
}

type worker struct {
	key    workerKey
	cmd    *exec.Cmd
	conn   *net.UnixConn
	exited chan struct{}
	// busy and lastUsed are guarded by Server.mu.
	busy     bool
	lastUsed time.Time
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
	server := &Server{config: config, pools: make(map[workerKey]*pool), assistants: make(map[*net.UnixConn]struct{})}
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

// Close stops every worker, busy or not, and cuts every assistant connection.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	pools := s.pools
	s.pools = make(map[workerKey]*pool)
	for _, p := range pools {
		p.wake()
	}
	assistants := s.assistants
	s.assistants = make(map[*net.UnixConn]struct{})
	s.mu.Unlock()
	var stopping sync.WaitGroup
	for _, p := range pools {
		for _, w := range p.workers {
			stopping.Go(w.stop)
		}
	}
	stopping.Wait()
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
	// The Product Service sends nothing after its request, so a read returns
	// only when it closes the connection: it gave up, and so does the worker.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = conn.Read(make([]byte, 1))
		cancel()
	}()
	s.dispatch(ctx, req, func(result response, file *os.File) error {
		err := writeMessage(conn, result, file)
		closeFile(file)
		if err != nil && ctx.Err() == nil {
			s.config.Logger.Warn("file broker reply failed", "op", req.Op, "error", err)
			cancel()
		}
		return err
	})
}

// dispatch runs one file operation and passes its reply to reply: a scan's
// pages as they arrive, then the final message.
func (s *Server) dispatch(ctx context.Context, req request, reply func(response, *os.File) error) {
	// This gate lives with the worker, so a Product Service crash cannot
	// release a publication that a restarted sharing service has fenced.
	if req.Op == opShareReset || req.Op == opShareFence || req.Op == opShareCheck {
		_ = reply(s.shareControl(req), nil)
		return
	}
	if req.ShareEpoch != "" || req.ShareID != "" {
		s.shareMu.Lock()
		defer s.shareMu.Unlock()
		if req.ShareEpoch == "" || req.ShareEpoch != s.shareEpoch || req.ShareID == "" || s.shareFenced[req.ShareID] {
			_ = reply(response{Error: &wireError{Code: codeForbidden}}, nil)
			return
		}
	}
	if !knownOps[req.Op] {
		_ = reply(response{Error: &wireError{Code: codeInvalidRequest, Message: "unknown file operation"}}, nil)
		return
	}
	identity, err := s.config.Sessions.ResolveSessionIdentity(ctx, req.Token)
	if err != nil || !identity.Enabled {
		_ = reply(response{Error: &wireError{Code: codeUnauthenticated}}, nil)
		return
	}
	if !s.config.VolumeReady() {
		_ = reply(response{Error: &wireError{Code: codeVolumeUnavailable}}, nil)
		return
	}
	req.Token = ""
	w, err := s.takeWorker(ctx, workerKey{uid: identity.UID, role: identity.Role}, identity)
	if err != nil {
		if ctx.Err() != nil {
			_ = reply(response{Error: encodeError(ctx.Err())}, nil)
			return
		}
		s.config.Logger.Error("file worker unavailable", "username", identity.Username, "error", err)
		_ = reply(response{Error: &wireError{Code: codeUnavailable}}, nil)
		return
	}
	var relayErr error
	result, file, err := w.call(ctx, req, func(page response) {
		if relayErr == nil {
			relayErr = reply(page, nil)
		}
	})
	if err != nil {
		s.config.Logger.Error("file worker failed", "username", identity.Username, "op", req.Op, "error", err)
		s.dropWorker(w)
		_ = reply(response{Error: &wireError{Code: codeUnavailable}}, nil)
		return
	}
	// The worker is free before the reply reaches the Product Service, so
	// the account's next call finds it.
	s.returnWorker(w)
	if relayErr != nil {
		closeFile(file)
		return
	}
	_ = reply(result, file)
}

// takeWorker returns a free worker of key's pool, starting one while the
// pool is below workersPerUser, or waits for one. The caller gives it back
// with returnWorker, or with dropWorker when its connection broke.
func (s *Server) takeWorker(ctx context.Context, key workerKey, identity accounts.Identity) (*worker, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, errors.New("file broker is closed")
		}
		p := s.pools[key]
		if p == nil {
			p = &pool{freed: make(chan struct{})}
		}
		p.workers = slices.DeleteFunc(p.workers, func(w *worker) bool {
			if w.busy || !w.hasExited() {
				return false
			}
			_ = w.conn.Close()
			return true
		})
		for _, w := range p.workers {
			if !w.busy {
				w.busy = true
				s.mu.Unlock()
				return w, nil
			}
		}
		if len(p.workers) < workersPerUser {
			w, err := s.startWorker(key, identity)
			if err == nil {
				w.busy = true
				p.workers = append(p.workers, w)
				s.pools[key] = p
			}
			s.mu.Unlock()
			return w, err
		}
		freed := p.freed
		s.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// returnWorker makes w free for its account's next call.
func (s *Server) returnWorker(w *worker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.busy = false
	w.lastUsed = time.Now()
	if p := s.pools[w.key]; p != nil {
		p.wake()
	}
}

// dropWorker stops a worker whose connection broke, freeing its place.
func (s *Server) dropWorker(w *worker) {
	s.mu.Lock()
	removed := false
	if p := s.pools[w.key]; p != nil {
		count := len(p.workers)
		p.workers = slices.DeleteFunc(p.workers, func(other *worker) bool { return other == w })
		if removed = len(p.workers) < count; removed {
			p.wake()
		}
		if len(p.workers) == 0 {
			delete(s.pools, w.key)
		}
	}
	s.mu.Unlock()
	// Otherwise Close took the pool and stops the worker itself.
	if removed {
		go w.stop()
	}
}

func (s *Server) startWorker(key workerKey, identity accounts.Identity) (*worker, error) {
	credential, err := s.credential(identity)
	if err != nil {
		return nil, err
	}
	var env []string
	if key.confined {
		env = []string{ConfineVariable + "=" + strings.Join(s.writablePlaces(identity.Username), ":")}
	}
	return s.spawn(key, credential, env)
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

// call sends req to the worker and returns the final message of its reply,
// passing the pages of a scan before it to page as they arrive. When ctx ends
// the worker is told to stop, and the rest of the reply is still read, so the
// connection is ready for the next call. An error means the connection broke
// and the worker must be dropped.
func (w *worker) call(ctx context.Context, req request, page func(response)) (response, *os.File, error) {
	if err := writeMessage(w.conn, req, nil); err != nil {
		return response{}, nil, err
	}
	// The cancel message is written before call returns, so it can never
	// reach the worker after its next request.
	finished := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			_ = writeMessage(w.conn, request{Op: opCancel}, nil)
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-watched
	}()
	for {
		var result response
		file, err := readMessage(w.conn, &result)
		if err != nil {
			return response{}, nil, err
		}
		if !result.More {
			return result, file, nil
		}
		closeFile(file)
		page(result)
	}
}

func (w *worker) hasExited() bool {
	select {
	case <-w.exited:
		return true
	default:
		return false
	}
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

// reapIdle stops the workers that have been free for IdleTimeout; a busy
// worker is never stopped, however long its call runs.
func (s *Server) reapIdle(now time.Time) {
	s.mu.Lock()
	var idle []*worker
	for key, p := range s.pools {
		p.workers = slices.DeleteFunc(p.workers, func(w *worker) bool {
			if w.busy || now.Sub(w.lastUsed) < s.config.IdleTimeout {
				return false
			}
			idle = append(idle, w)
			return true
		})
		if len(p.workers) == 0 {
			delete(s.pools, key)
		}
	}
	s.mu.Unlock()
	for _, w := range idle {
		w.stop()
	}
}
