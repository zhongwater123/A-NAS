// Package terminal serves interactive shells for the Web desktop over WebSocket.
//
// In production the File Broker starts each shell as the signed-in
// administrator's own Linux account (ADR 0008) and hands back only the PTY;
// this package forwards bytes and never elevates privileges. Without a
// Spawner, development shells run as the Product Service user. Sessions are
// refused unless the feature is enabled, the peer is a loopback address, the
// Host names a loopback endpoint and the browser Origin matches that Host.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	StatusPath  = "/api/v1/terminal"
	SessionPath = "/api/v1/terminal/session"

	defaultMaxSessions = 4
	maxClientMessage   = 64 << 10
)

// Config controls whether and how shells are started.
type Config struct {
	Enabled bool
	// Spawner starts shells; nil starts local development shells with Shell
	// in Dir as the Product Service user.
	Spawner     Spawner
	Shell       string
	Dir         string
	MaxSessions int
}

// Handler serves the terminal status endpoint and WebSocket sessions.
type Handler struct {
	config Config
	logger *slog.Logger

	mu       sync.Mutex
	active   int
	ctx      context.Context
	shutdown context.CancelFunc
	sessions sync.WaitGroup
}

func New(config Config, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = defaultMaxSessions
	}
	if config.Shell == "" {
		config.Shell = defaultShell()
	}
	if config.Dir == "" {
		config.Dir, _ = os.UserHomeDir()
	}
	if config.Spawner == nil {
		config.Enabled = config.Enabled && localShellSupported
		config.Spawner = localSpawner{shell: config.Shell, dir: config.Dir}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Handler{config: config, logger: logger, ctx: ctx, shutdown: cancel}
}

// Shutdown ends every running session. Hijacked WebSocket connections are not
// tracked by http.Server.Shutdown, so the caller must invoke this explicitly.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.shutdown()
	done := make(chan struct{})
	go func() {
		h.sessions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != StatusPath && r.URL.Path != SessionPath {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if r.URL.Path == StatusPath {
		writeJSON(w, http.StatusOK, statusResponse{Enabled: h.config.Enabled})
		return
	}
	h.serveSession(w, r)
}

func (h *Handler) serveSession(w http.ResponseWriter, r *http.Request) {
	if !h.config.Enabled {
		writeError(w, http.StatusForbidden, "terminal_disabled", "terminal is disabled")
		return
	}
	if !isLoopbackPeer(r.RemoteAddr) || !isLoopbackHost(r.Host) {
		writeError(w, http.StatusForbidden, "terminal_forbidden", "terminal is only available on loopback")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, http.StatusUpgradeRequired, "upgrade_required", "terminal sessions require WebSocket")
		return
	}
	switch h.acquire() {
	case errShuttingDown:
		writeError(w, http.StatusServiceUnavailable, "terminal_unavailable", "server is shutting down")
		return
	case errBusy:
		writeError(w, http.StatusTooManyRequests, "terminal_busy", "too many terminal sessions")
		return
	}
	defer h.release()

	// Server read and write timeouts stay attached to hijacked connections.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.logger.WarnContext(r.Context(), "terminal upgrade rejected", "error", err)
		return
	}
	conn.SetReadLimit(maxClientMessage)

	// Keep the request's values, such as the session token the File Broker
	// verifies, but end with Shutdown rather than with the HTTP request.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()

	h.logger.InfoContext(ctx, "terminal session started")
	status, reason := runSession(ctx, conn, h.config.Spawner, h.logger)
	_ = conn.Close(status, reason)
	h.logger.InfoContext(ctx, "terminal session ended", "reason", reason)
}

var (
	errShuttingDown = errors.New("terminal handler is shutting down")
	errBusy         = errors.New("terminal session limit reached")
)

func (h *Handler) acquire() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return errShuttingDown
	}
	if h.active >= h.config.MaxSessions {
		return errBusy
	}
	h.active++
	h.sessions.Add(1)
	return nil
}

func (h *Handler) release() {
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	h.sessions.Done()
}

// controlMessage is the only text frame accepted from clients; binary frames
// carry raw terminal input.
type controlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (m controlMessage) validResize() bool {
	return m.Type == "resize" && m.Cols > 0 && m.Cols <= 1000 && m.Rows > 0 && m.Rows <= 1000
}

func shellEnvironment(shell string) []string {
	env := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"SHELL=" + shell,
		"PATH=" + valueOr(os.Getenv("PATH"), "/usr/local/bin:/usr/bin:/bin"),
		"LANG=" + valueOr(os.Getenv("LANG"), "C.UTF-8"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		env = append(env, "HOME="+home)
	}
	if current, err := user.Current(); err == nil {
		env = append(env, "USER="+current.Username, "LOGNAME="+current.Username)
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	return env
}

func defaultShell() string {
	if shell := os.Getenv("SHELL"); strings.HasPrefix(shell, "/") {
		return shell
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHost rejects DNS-rebound names that resolve to loopback but would
// otherwise satisfy the same-origin check.
func isLoopbackHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type statusResponse struct {
	Enabled bool `json:"enabled"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}
