// Package assistantapi exposes the AI assistant under /api/v1/assistant: the
// signed-in member's own Hermes instance, as Hermes Desktop uses it (ADR 0024
// decision 9). A WebSocket carries the allowed part of `hermes serve`'s
// JSON-RPC; REST routes list, read, rename and delete conversations; and the
// confirmation cards for 提交更改 are answered here, never by the window
// answering Hermes itself.
//
// It does not authenticate: the Product Service wraps it so every request
// carries the signed-in user (WithUser) and its session, and writes have passed
// CSRF checks. It never decides what the assistant may read or write; the
// instance's sandbox and the executor of changes do. It reaches only the
// caller's own instance, through the session gate (decision 10).
package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const PathPrefix = "/api/v1/assistant"

const (
	// maxInputBytes bounds one chat message; the model's context is the
	// real limit, this only keeps a request from being unbounded.
	maxInputBytes = 64 << 10
	// requestTimeout covers a cold start of the instance (seconds) plus the
	// call.
	requestTimeout = 90 * time.Second
	sessionLimit   = 100
	// maxTitleRunes is Hermes's own title limit (hermes_state MAX_TITLE_LENGTH).
	maxTitleRunes = 100
)

var accountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type Options struct {
	// Connector reaches the signed-in member's instance; nil leaves the
	// assistant off and the desktop keeps it as planned.
	Connector Connector
	// SocketDir holds <account>.sock, each member's instance socket; it only
	// tells the status whether the account has an assistant.
	SocketDir string
	// Changes executes 提交更改; without it every approval is denied.
	Changes ChangeStore
	Logger  *slog.Logger
}

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

type handler struct {
	options   Options
	logger    *slog.Logger
	mux       *http.ServeMux
	ctx       context.Context
	membersMu sync.Mutex
	members   map[string]*member
}

// New returns the assistant API.
func New(options Options) http.Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if options.Changes == nil {
		options.Changes = noChanges{}
	}
	h := &handler{options: options, logger: logger, mux: http.NewServeMux(), ctx: context.Background(), members: map[string]*member{}}
	routes := map[string]func(http.ResponseWriter, *http.Request, string){
		"GET /status":                  h.status,
		"GET /ws":                      h.gateway,
		"GET /sessions":                h.listSessions,
		"PATCH /sessions/{id}":         h.renameSession,
		"DELETE /sessions/{id}":        h.deleteSession,
		"GET /sessions/{id}/messages":  h.messages,
		"GET /approvals":               h.listApprovals,
		"POST /approvals/{id}/confirm": h.decideApproval(true),
		"POST /approvals/{id}/reject":  h.decideApproval(false),
		"GET /memory":                  h.getMemory,
		"GET /memory/items/{id}":       h.getMemoryItem,
		"DELETE /memory/items/{id}":    h.deleteMemoryItem,
		"POST /memory/clear":           h.clearMemory,
	}
	for pattern, route := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withAccount(route))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.options.Connector == nil {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not enabled on this device")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withAccount(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userKey{}).(accounts.User)
		if !ok || user.ID == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if !accountPattern.MatchString(user.Username) {
			writeError(w, http.StatusForbidden, "assistant_unavailable", "this account has no assistant")
			return
		}
		next(w, r, user.Username)
	}
}

func (h *handler) status(w http.ResponseWriter, _ *http.Request, account string) {
	available := true
	if h.options.SocketDir != "" {
		_, err := os.Stat(filepath.Join(h.options.SocketDir, account+".sock"))
		available = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": available})
}

// session is what the desktop needs of a stored Hermes session.
type session struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Preview      string   `json:"preview"`
	StartedAt    float64  `json:"startedAt"`
	LastActive   *float64 `json:"lastActive,omitempty"`
	MessageCount int      `json:"messageCount"`
}

type hermesSession struct {
	ID           string   `json:"id"`
	Title        *string  `json:"title"`
	Preview      *string  `json:"preview"`
	StartedAt    float64  `json:"started_at"`
	LastActive   *float64 `json:"last_active"`
	MessageCount int      `json:"message_count"`
	Hidden       flag     `json:"hidden"`
}

// flag is a session flag as Hermes lists it: SQLite's 0 or 1, or a boolean.
type flag bool

func (f *flag) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", "1":
		*f = true
	case "false", "0", "null":
		*f = false
	default:
		return fmt.Errorf("session flag %s is neither 0, 1 nor a boolean", data)
	}
	return nil
}

func (s hermesSession) view() session {
	view := session{ID: s.ID, StartedAt: s.StartedAt, LastActive: s.LastActive, MessageCount: s.MessageCount}
	if s.Title != nil {
		view.Title = *s.Title
	}
	if s.Preview != nil {
		view.Preview = *s.Preview
	}
	return view
}

func (h *handler) listSessions(w http.ResponseWriter, r *http.Request, account string) {
	var page struct {
		Sessions []hermesSession `json:"sessions"`
	}
	// Conversations show once they have a message: Hermes stores a new one at
	// its first prompt.
	query := url.Values{"limit": {fmt.Sprint(sessionLimit)}, "order": {"recent"}, "archived": {"exclude"}, "min_messages": {"1"}}
	if !h.call(w, r, account, http.MethodGet, "/api/sessions?"+query.Encode(), nil, &page) {
		return
	}
	sessions := make([]session, 0, len(page.Sessions))
	for _, item := range page.Sessions {
		if !bool(item.Hidden) && storedIDPattern.MatchString(item.ID) {
			sessions = append(sessions, item.view())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *handler) renameSession(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len([]rune(title)) > maxTitleRunes {
		writeError(w, http.StatusBadRequest, "invalid_title", "a title has 1 to 100 characters")
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title})
	var updated struct {
		Title string `json:"title"`
	}
	if !h.call(w, r, account, http.MethodPatch, "/api/sessions/"+id, payload, &updated) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "title": updated.Title})
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	if h.call(w, r, account, http.MethodDelete, "/api/sessions/"+id, nil, nil) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// messages returns the transcript in Hermes's own message form, which the
// desktop turns into chat messages with Hermes Desktop's transcript model.
func (h *handler) messages(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	var page struct {
		Messages json.RawMessage `json:"messages"`
	}
	if !h.call(w, r, account, http.MethodGet, "/api/sessions/"+id+"/messages?inline_images=false", nil, &page) {
		return
	}
	if len(page.Messages) == 0 {
		page.Messages = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": page.Messages})
}

func (h *handler) listApprovals(w http.ResponseWriter, _ *http.Request, account string) {
	writeJSON(w, http.StatusOK, map[string]any{"approvals": h.member(account).list()})
}

func (h *handler) decideApproval(confirm bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, account string) {
		id := r.PathValue("id")
		if !requestIDPattern.MatchString(id) {
			writeError(w, http.StatusBadRequest, "invalid_approval", "invalid approval id")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		c, err := h.decide(ctx, h.member(account), id, confirm)
		switch {
		case errors.Is(err, ErrChangeNotFound):
			writeError(w, http.StatusNotFound, "approval_not_found", "no such approval")
		case errors.Is(err, ErrChangeDecided):
			writeError(w, http.StatusConflict, "approval_decided", "this change was already decided")
		case errors.Is(err, errApprovalGone):
			writeError(w, http.StatusConflict, "approval_expired", "the assistant no longer waits for this answer")
		case errors.Is(err, ErrChangeSession):
			writeError(w, http.StatusUnauthorized, "authentication_required", "sign in again to answer")
		case err != nil:
			h.logger.Warn("assistant approval not decided", "account", account, "confirm", confirm, "error", err)
			writeError(w, http.StatusBadGateway, "changes_unavailable", "the change could not be decided")
		default:
			h.logger.Info("assistant change decided", "account", account, "confirm", confirm)
			writeJSON(w, http.StatusOK, map[string]any{"approval": c})
		}
	}
}

// call makes a JSON request to the account's instance and decodes the reply
// into into (when not nil). On failure it has already answered the client.
func (h *handler) call(w http.ResponseWriter, r *http.Request, account, method, path string, body []byte, into any) bool {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	status, data, err := h.rest(ctx, account, method, path, body)
	if err != nil {
		h.unavailable(w, account, err)
		return false
	}
	if status >= 300 {
		h.relayError(w, status, data)
		return false
	}
	if into == nil {
		return true
	}
	if err := json.Unmarshal(data, into); err != nil {
		h.logger.Warn("assistant reply undecodable", "account", account, "error", err)
		writeError(w, http.StatusBadGateway, "assistant_unavailable", "the assistant answered unexpectedly")
		return false
	}
	return true
}

func (h *handler) unavailable(w http.ResponseWriter, account string, err error) {
	if errors.Is(err, ErrSessionRejected) {
		writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	}
	if !errors.Is(err, ErrNoAssistant) {
		h.logger.Warn("assistant unreachable", "account", account, "error", err)
	}
	writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not reachable")
}

// relayError turns an instance error into ours; a missing session stays 404.
func (h *handler) relayError(w http.ResponseWriter, status int, detail []byte) {
	var failure struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(detail, &failure)
	switch {
	case status == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "session_not_found", "no such conversation")
	case status == http.StatusBadRequest && strings.Contains(failure.Detail, "already in use"):
		writeError(w, http.StatusConflict, "title_in_use", "another conversation already has this title")
	case status == http.StatusConflict, status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable:
		writeError(w, http.StatusServiceUnavailable, "assistant_busy", "the assistant is busy, try again shortly")
	default:
		h.logger.Warn("assistant request failed", "status", status, "detail", strings.TrimSpace(truncate(detail)))
		writeError(w, http.StatusBadGateway, "assistant_failed", "the assistant could not complete the request")
	}
}

func sessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !storedIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_session", "invalid conversation id")
		return "", false
	}
	return id, true
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxInputBytes+1024))
	if err := decoder.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "response could not be encoded")
		return
	}
	writeRaw(w, status, data)
}

func writeRaw(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// noChanges stands in when no executor is configured: nothing can be
// prepared, so every approval is denied.
type noChanges struct{}

var errNoExecutor = errors.New("no executor of changes is configured")

func (noChanges) Prepare(context.Context, string, Submission) (Change, error) {
	return Change{}, errNoExecutor
}
func (noChanges) Decide(context.Context, string, string, Decision) (Change, error) {
	return Change{}, errNoExecutor
}
func (noChanges) Get(context.Context, string, string) (Change, error) { return Change{}, errNoExecutor }
