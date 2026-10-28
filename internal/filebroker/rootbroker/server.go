// Package rootbroker is the file broker inside the root Host Agent. It
// verifies each caller's session itself, runs file operations in workers as
// the caller's own Linux account, starts administrators' terminal shells, is
// the session gate to members' assistants and executes the assistants'
// changes (ADR 0008, ADR 0024). The Product Service reaches it through
// internal/filebroker over internal/filebroker/wire and links none of it
// (#221).
//
// The kernel's POSIX ACLs decide every access a worker makes. Opened and
// created files travel back as file descriptors over SCM_RIGHTS, so root
// reads and writes no file contents, except for the assistant's changes:
// their executor (internal/assistantchanges) copies a staged file through
// such descriptors, a block at a time, into its own store when the card is
// made and from there into place once approved.
package rootbroker

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

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"golang.org/x/sync/singleflight"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/assistantlogin"
)

// SessionResolver verifies a session token and returns its Linux identity.
type SessionResolver interface {
	ResolveSessionIdentity(context.Context, string) (accountid.Identity, error)
}

// Placer moves a process the broker started into a scope unit of its own
// (internal/systemdscope). what says what the process does, as "terminal" or
// "files", and username for whom.
type Placer interface {
	Place(ctx context.Context, pid int, what, username string) error
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
	// Scopes moves each file worker and terminal shell out of the Host
	// Agent's cgroup into a scope of its own (issue #166); nil leaves them in
	// the broker's. A shell waits until it is there.
	Scopes Placer

	// skipCredentials lets unprivileged tests run workers as the test user.
	skipCredentials bool
}

type Server struct {
	config Config
	// executable is the running binary, which also serves as the worker, the
	// probe and the terminal's gate.
	executable string
	mu         sync.Mutex
	pools      map[workerKey]*pool
	// starting starts one worker at a time per workerKey, outside mu, so an
	// account whose worker waits on systemd holds up no other account (#277).
	starting singleflight.Group
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
	role accountid.Role
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
	var executable string
	if config.Command == nil || config.ProbeCommand == nil || config.Scopes != nil {
		var err error
		if executable, err = os.Executable(); err != nil {
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
	server := &Server{config: config, executable: executable, pools: make(map[workerKey]*pool), assistants: make(map[*net.UnixConn]struct{})}
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
	var req wire.Request
	extra, err := wire.ReadMessage(conn, &req)
	wire.CloseFile(extra)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch req.Op {
	case wire.OpTerminal:
		s.serveTerminal(conn, req)
		return
	case wire.OpAssistant:
		s.serveAssistant(conn, req)
		return
	case wire.OpChangePrepare, wire.OpChangeDecide, wire.OpChangeGet:
		result := s.serveChange(req)
		if err := wire.WriteMessage(conn, result, nil); err != nil {
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
	s.dispatch(ctx, req, func(result wire.Response, file *os.File) error {
		err := wire.WriteMessage(conn, result, file)
		wire.CloseFile(file)
		if err != nil && ctx.Err() == nil {
			s.config.Logger.Warn("file broker reply failed", "op", req.Op, "error", err)
			cancel()
		}
		return err
	})
}

// dispatch runs one file operation and passes its reply to reply: a scan's
// pages as they arrive, then the final message.
func (s *Server) dispatch(ctx context.Context, req wire.Request, reply func(wire.Response, *os.File) error) {
	// This gate lives with the worker, so a Product Service crash cannot
	// release a publication that a restarted sharing service has fenced.
	if req.Op == wire.OpShareReset || req.Op == wire.OpShareFence || req.Op == wire.OpShareCheck {
		_ = reply(s.shareControl(req), nil)
		return
	}
	if req.ShareEpoch != "" || req.ShareID != "" {
		s.shareMu.Lock()
		defer s.shareMu.Unlock()
		if req.ShareEpoch == "" || req.ShareEpoch != s.shareEpoch || req.ShareID == "" || s.shareFenced[req.ShareID] {
			_ = reply(wire.Response{Error: &wire.Error{Code: wire.CodeForbidden}}, nil)
			return
		}
	}
	if !wire.IsFileOp(req.Op) {
		_ = reply(wire.Response{Error: &wire.Error{Code: wire.CodeInvalidRequest, Message: "unknown file operation"}}, nil)
		return
	}
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		_ = reply(wire.Response{Error: refused}, nil)
		return
	}
	if !s.config.VolumeReady() {
		_ = reply(wire.Response{Error: &wire.Error{Code: wire.CodeVolumeUnavailable}}, nil)
		return
	}
	req.Token = ""
	w, err := s.takeWorker(ctx, workerKey{uid: identity.UID, role: identity.Role}, identity)
	if err != nil {
		if ctx.Err() != nil {
			_ = reply(wire.Response{Error: encodeError(ctx.Err())}, nil)
			return
		}
		s.config.Logger.Error("file worker unavailable", "username", identity.Username, "error", err)
		_ = reply(wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable}}, nil)
		return
	}
	var relayErr error
	result, file, err := w.call(ctx, req, func(page wire.Response) {
		if relayErr == nil {
			relayErr = reply(page, nil)
		}
	})
	if err != nil {
		s.config.Logger.Error("file worker failed", "username", identity.Username, "op", req.Op, "error", err)
		s.dropWorker(w)
		_ = reply(wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable}}, nil)
		return
	}
	// The worker is free before the reply reaches the Product Service, so
	// the account's next call finds it.
	s.returnWorker(w)
	if relayErr != nil {
		wire.CloseFile(file)
		return
	}
	_ = reply(result, file)
}

// identify verifies the session behind a token. A session that is gone,
// expired or whose user may not act is unauthenticated. A session directory
// that cannot be read right now, as while SQLite is busy, is unavailable
// instead, so a passing error never reads as a signed-out user (#166).
func (s *Server) identify(ctx context.Context, token string) (accountid.Identity, *wire.Error) {
	identity, err := s.config.Sessions.ResolveSessionIdentity(ctx, token)
	switch {
	case errors.Is(err, accountid.ErrSessionNotFound):
		return accountid.Identity{}, &wire.Error{Code: wire.CodeUnauthenticated}
	case err != nil:
		s.config.Logger.Warn("session directory unavailable", "error", err)
		return accountid.Identity{}, &wire.Error{Code: wire.CodeUnavailable, Message: "sessions cannot be verified right now"}
	case !identity.Enabled:
		return accountid.Identity{}, &wire.Error{Code: wire.CodeUnauthenticated}
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
func (s *Server) recheck(ctx context.Context, token string, unconfirmed *int, keep func(accountid.Identity) bool) bool {
	current, err := s.config.Sessions.ResolveSessionIdentity(ctx, token)
	if err != nil && !errors.Is(err, accountid.ErrSessionNotFound) {
		*unconfirmed++
		s.config.Logger.Warn("session cannot be confirmed", "error", err, "rechecks", *unconfirmed)
		return *unconfirmed < unconfirmedRechecks
	}
	*unconfirmed = 0
	return err == nil && current.Enabled && keep(current)
}

// takeWorker returns a free worker of key's pool, starting one while the
// pool is below workersPerUser, or waits for one. The caller gives it back
// with returnWorker, or with dropWorker when its connection broke.
func (s *Server) takeWorker(ctx context.Context, key workerKey, identity accountid.Identity) (*worker, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, errClosed
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
			s.mu.Unlock()
			// The new worker joins the pool free, and this call looks again:
			// another call may take it first, and then this one starts the
			// next or waits.
			if err := s.addWorker(ctx, key, identity); err != nil {
				return nil, err
			}
			continue
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

var errClosed = errors.New("file broker is closed")

// addWorker starts a worker for key's pool, unless the pool filled up
// meanwhile. Calls for the same key share one start, and its failure; ctx
// ending stops the wait, not the start, which the other calls may share.
func (s *Server) addWorker(ctx context.Context, key workerKey, identity accountid.Identity) error {
	started := s.starting.DoChan(fmt.Sprint(key), func() (any, error) {
		// Only this function adds workers, and never twice at once for a key,
		// so a pool below the limit here stays below it until the add.
		s.mu.Lock()
		full := s.pools[key] != nil && len(s.pools[key].workers) >= workersPerUser
		s.mu.Unlock()
		if full {
			return nil, nil
		}
		w, err := s.startWorker(key, identity)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			w.stop()
			return nil, errClosed
		}
		p := s.pools[key]
		if p == nil {
			p = &pool{freed: make(chan struct{})}
			s.pools[key] = p
		}
		p.workers = append(p.workers, w)
		p.wake()
		s.mu.Unlock()
		return nil, nil
	})
	select {
	case result := <-started:
		return result.Err
	case <-ctx.Done():
		return ctx.Err()
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

func (s *Server) startWorker(key workerKey, identity accountid.Identity) (*worker, error) {
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
	// The worker reads nothing but its socket until the first request, which
	// comes after this, so it does no work in the Host Agent's cgroup.
	s.place(w.cmd.Process.Pid, "files", identity.Username)
	return w, nil
}

// placeTimeout bounds asking systemd for a scope. Calls for the account
// whose worker or shell is being placed wait that long at most; others do not
// wait (#277).
const placeTimeout = 5 * time.Second

// place moves a worker or shell into a scope of its own. Web files and the
// terminal matter more than where they are counted, so a failure is logged
// and the process runs on in the Host Agent's cgroup.
func (s *Server) place(pid int, what, username string) {
	if s.config.Scopes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), placeTimeout)
	defer cancel()
	if err := s.config.Scopes.Place(ctx, pid, what, username); err != nil {
		s.config.Logger.Error("process stays in the Host Agent's cgroup", "what", what, "username", username, "pid", pid, "error", err)
	}
}

// credential maps an identity to the worker's UID and groups. Only accounts
// in the A-NAS range that the Host Agent provisioned under the same UID can
// get a worker; root and system accounts never can.
func (s *Server) credential(identity accountid.Identity) (*syscall.Credential, error) {
	if identity.UID < accountid.FirstUserUID || identity.UID > accountid.LastUserUID {
		return nil, fmt.Errorf("UID %d is outside the A-NAS range", identity.UID)
	}
	uid, err := s.config.LookupUID(identity.Username)
	if err != nil || uid != identity.UID {
		return nil, fmt.Errorf("Linux account %s is not provisioned with UID %d", identity.Username, identity.UID)
	}
	groups := []uint32{accountid.UsersGID}
	if identity.Role == accountid.RoleAdmin {
		groups = append(groups, accountid.AdminsGID)
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
	defer func() { _ = child.Close() }()
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
func (w *worker) call(ctx context.Context, req wire.Request, page func(wire.Response)) (wire.Response, *os.File, error) {
	if err := wire.WriteMessage(w.conn, req, nil); err != nil {
		return wire.Response{}, nil, err
	}
	// The cancel message is written before call returns, so it can never
	// reach the worker after its next request.
	finished := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			_ = wire.WriteMessage(w.conn, wire.Request{Op: wire.OpCancel}, nil)
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-watched
	}()
	for {
		var result wire.Response
		file, err := wire.ReadMessage(w.conn, &result)
		if err != nil {
			return wire.Response{}, nil, err
		}
		if !result.More {
			return result, file, nil
		}
		wire.CloseFile(file)
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
