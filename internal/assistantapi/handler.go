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
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
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
	// Notices keeps the notices of scheduled tasks' runs; without it the
	// member is not told of them.
	Notices TaskNotices
	Logger  *slog.Logger
	// Context ends what the API runs in the background: the instances kept
	// for signed-in members, the watch over their scheduled tasks, and the
	// gateway connections. Wait returns once the first two stopped. Without
	// it they last as long as the process.
	Context context.Context
}

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// Handler is the assistant API.
type Handler struct {
	options Options
	logger  *slog.Logger
	mux     *apiroute.Mux
	ctx     context.Context
	// background runs the kept instances and the watch over their tasks.
	background sync.WaitGroup
	membersMu  sync.Mutex
	members    map[string]*member
	// keeps holds the instances kept running for signed-in members, by account.
	keepsMu sync.Mutex
	keeps   map[string]*keep
	// waiting is set by Wait, under keepsMu, so that Keep starts nothing the
	// wait could miss.
	waiting bool
}

// New returns the assistant API.
func New(options Options) *Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if options.Changes == nil {
		options.Changes = noChanges{}
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	h := &Handler{options: options, logger: logger, mux: apiroute.NewMux(), ctx: ctx, members: map[string]*member{}, keeps: map[string]*keep{}}
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
		"POST /approvals/{id}/answer":  h.answerApproval,
		"GET /memory":                  h.getMemory,
		"GET /memory/items/{id}":       h.getMemoryItem,
		"DELETE /memory/items/{id}":    h.deleteMemoryItem,
		"POST /memory/clear":           h.clearMemory,
		"GET /tasks":                   h.listTasks,
		"GET /tasks/{id}/runs":         h.taskRuns,
		"POST /tasks/{id}/pause":       h.changeTask("pause"),
		"POST /tasks/{id}/resume":      h.changeTask("resume"),
		"POST /tasks/{id}/run":         h.runTask,
		"DELETE /tasks/{id}":           h.deleteTask,
	}
	for pattern, route := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withAccount(route))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the assistant API serves beneath PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(Options{}).mux.Endpoints()
}

// Wait keeps anything new from starting and returns once the background work
// already running stopped. Call it after Options.Context ended; requests may
// still be arriving.
func (h *Handler) Wait() {
	h.keepsMu.Lock()
	h.waiting = true
	h.keepsMu.Unlock()
	h.background.Wait()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.options.Connector == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not enabled on this device")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) withAccount(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userKey{}).(accounts.User)
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if !accountPattern.MatchString(user.Username) {
			httpjson.WriteError(w, http.StatusForbidden, "assistant_unavailable", "this account has no assistant")
			return
		}
		next(w, r, user.Username)
	}
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request, account string) {
	available := true
	if h.options.SocketDir != "" {
		_, err := os.Stat(filepath.Join(h.options.SocketDir, account+".sock"))
		available = err == nil
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"available": available})
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

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request, account string) {
	var page struct {
		Sessions []hermesSession `json:"sessions"`
	}
	// Conversations show once they have a message: Hermes stores a new one at
	// its first prompt. Runs of scheduled tasks show under their task, as
	// Hermes Desktop lists them (tasks.go).
	query := url.Values{"limit": {fmt.Sprint(sessionLimit)}, "order": {"recent"}, "archived": {"exclude"}, "min_messages": {"1"},
		"exclude_sources": {"cron"}}
	if !h.call(w, r, account, http.MethodGet, "/api/sessions?"+query.Encode(), nil, &page) {
		return
	}
	sessions := make([]session, 0, len(page.Sessions))
	for _, item := range page.Sessions {
		if !bool(item.Hidden) && storedIDPattern.MatchString(item.ID) {
			sessions = append(sessions, item.view())
		}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *Handler) renameSession(w http.ResponseWriter, r *http.Request, account string) {
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
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_title", "a title has 1 to 100 characters")
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title})
	var updated struct {
		Title string `json:"title"`
	}
	if !h.call(w, r, account, http.MethodPatch, "/api/sessions/"+id, payload, &updated) {
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"id": id, "title": updated.Title})
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request, account string) {
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
func (h *Handler) messages(w http.ResponseWriter, r *http.Request, account string) {
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
	httpjson.Write(w, http.StatusOK, map[string]any{"messages": page.Messages})
}

func (h *Handler) listApprovals(w http.ResponseWriter, _ *http.Request, account string) {
	httpjson.Write(w, http.StatusOK, map[string]any{"approvals": h.member(account).list()})
}

func (h *Handler) decideApproval(confirm bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, account string) {
		id := r.PathValue("id")
		if !requestIDPattern.MatchString(id) {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_approval", "invalid approval id")
			return
		}
		// A command may be allowed for the rest of its conversation.
		var body struct {
			ForSession bool `json:"forSession"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		c, err := h.decide(ctx, h.member(account), id, confirm, body.ForSession)
		h.decided(w, account, c, err)
	}
}

// answerApproval answers a question card; no answers skips the questions.
func (h *Handler) answerApproval(w http.ResponseWriter, r *http.Request, account string) {
	id := r.PathValue("id")
	if !requestIDPattern.MatchString(id) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_approval", "invalid approval id")
		return
	}
	var body struct {
		Answers map[string]*string `json:"answers"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Answers == nil {
		body.Answers = map[string]*string{}
	}
	c, err := h.answerQuestion(h.member(account), id, body.Answers)
	h.decided(w, account, c, err)
}

// decided answers a decision on a card.
func (h *Handler) decided(w http.ResponseWriter, account string, c card, err error) {
	switch {
	case errors.Is(err, ErrChangeNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "approval_not_found", "no such approval")
	case errors.Is(err, ErrChangeDecided):
		httpjson.WriteError(w, http.StatusConflict, "approval_decided", "this change was already decided")
	case errors.Is(err, errApprovalGone):
		httpjson.WriteError(w, http.StatusConflict, "approval_expired", "the assistant no longer waits for this answer")
	case errors.Is(err, ErrChangeSession):
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "sign in again to answer")
	case errors.Is(err, errNotAnApproval), errors.Is(err, errNotAQuestion), errors.Is(err, errInvalidAnswer):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_answer", err.Error())
	case err != nil:
		h.logger.Warn("assistant card not decided", "account", account, "error", err)
		httpjson.WriteError(w, http.StatusBadGateway, "changes_unavailable", "the change could not be decided")
	default:
		h.logger.Info("assistant card decided", "account", account, "kind", c.Kind, "status", c.Status)
		httpjson.Write(w, http.StatusOK, map[string]any{"approval": c})
	}
}

// call makes a JSON request to the account's instance and decodes the reply
// into into (when not nil). On failure it has already answered the client.
func (h *Handler) call(w http.ResponseWriter, r *http.Request, account, method, path string, body []byte, into any) bool {
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
		httpjson.WriteError(w, http.StatusBadGateway, "assistant_unavailable", "the assistant answered unexpectedly")
		return false
	}
	return true
}

func (h *Handler) unavailable(w http.ResponseWriter, account string, err error) {
	if errors.Is(err, ErrSessionRejected) {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	}
	if !errors.Is(err, ErrNoAssistant) {
		h.logger.Warn("assistant unreachable", "account", account, "error", err)
	}
	httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not reachable")
}

// relayError turns an instance error into ours; a missing session stays 404.
func (h *Handler) relayError(w http.ResponseWriter, status int, detail []byte) {
	var failure struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(detail, &failure)
	switch {
	case status == http.StatusNotFound:
		httpjson.WriteError(w, http.StatusNotFound, "session_not_found", "no such conversation")
	case status == http.StatusBadRequest && strings.Contains(failure.Detail, "already in use"):
		httpjson.WriteError(w, http.StatusConflict, "title_in_use", "another conversation already has this title")
	case status == http.StatusConflict, status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable:
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_busy", "the assistant is busy, try again shortly")
	default:
		h.logger.Warn("assistant request failed", "status", status, "detail", strings.TrimSpace(truncate(detail)))
		httpjson.WriteError(w, http.StatusBadGateway, "assistant_failed", "the assistant could not complete the request")
	}
}

func sessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !storedIDPattern.MatchString(id) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_session", "invalid conversation id")
		return "", false
	}
	return id, true
}

// readJSON reads an optional body: without one, into keeps its zero value.
func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	if err := httpjson.Decode(w, r, maxInputBytes+1024, into); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_request", "the request body is not valid JSON")
		return false
	}
	return true
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
