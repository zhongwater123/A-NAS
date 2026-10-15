// Package assistantapi exposes the AI assistant under /api/v1/assistant: the
// signed-in member's conversations with their own Hermes instance, and the
// pending changes it submitted (ADR 0022).
//
// It does not authenticate: the Product Service wraps it so every request
// carries the signed-in user (WithUser) and writes have passed CSRF checks. It
// never decides what the assistant may read or write; the instance's sandbox
// and the pending-change store do. It only reaches the caller's own instance,
// through that account's Unix socket.
package assistantapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	// call; streamed turns are bounded by the client instead.
	requestTimeout = 90 * time.Second
	sessionLimit   = 100
	// maxTitleRunes is Hermes's own title limit (hermes_state MAX_TITLE_LENGTH).
	maxTitleRunes = 100
)

var (
	sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	changeIDPattern  = regexp.MustCompile(`^[0-9]{1,18}$`)
	accountPattern   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// ChangeStore keeps what an assistant submitted for its member to confirm.
// In A-NAS the Product Service owns it and the File Broker applies a
// confirmed change; the UI prototype uses the spike's change broker.
type ChangeStore interface {
	List(ctx context.Context, account string) (json.RawMessage, error)
	Decide(ctx context.Context, account, id string, confirm bool) (json.RawMessage, error)
	// Attach records the conversation a submission was made in: the newest
	// change of the account with exactly this summary and these operations
	// and no conversation yet. ErrChangeNotFound when none matches, as when
	// the submission was refused.
	Attach(ctx context.Context, account, session string, submission json.RawMessage) error
}

// ErrChangeNotFound and ErrChangeDecided are returned by a ChangeStore.
var (
	ErrChangeNotFound = errors.New("change not found")
	ErrChangeDecided  = errors.New("change already decided")
)

type Options struct {
	// SocketDir holds <account>.sock, each member's assistant API socket.
	SocketDir string
	// KeyDir holds <account>.key, each instance's API Server key.
	KeyDir  string
	Changes ChangeStore
	Logger  *slog.Logger
}

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

type handler struct {
	options Options
	logger  *slog.Logger
	mux     *http.ServeMux
	clients sync.Map // account -> *http.Client
}

// New returns the assistant API. An empty SocketDir answers 503
// assistant_unavailable, so the desktop keeps the assistant as planned.
func New(options Options) http.Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{options: options, logger: logger, mux: http.NewServeMux()}
	routes := map[string]func(http.ResponseWriter, *http.Request, string){
		"GET /status":                 h.status,
		"GET /sessions":               h.listSessions,
		"POST /sessions":              h.createSession,
		"PATCH /sessions/{id}":        h.renameSession,
		"DELETE /sessions/{id}":       h.deleteSession,
		"GET /sessions/{id}/messages": h.messages,
		"POST /sessions/{id}/chat":    h.chat,
		"GET /changes":                h.listChanges,
		"POST /changes/{id}/confirm":  h.decide(true),
		"POST /changes/{id}/reject":   h.decide(false),
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
	if h.options.SocketDir == "" {
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
	_, err := os.Stat(filepath.Join(h.options.SocketDir, account+".sock"))
	writeJSON(w, http.StatusOK, map[string]any{"available": err == nil, "changes": h.options.Changes != nil})
}

// session is what the desktop needs of a Hermes session.
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
	Hidden       bool     `json:"hidden"`
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
		Data []hermesSession `json:"data"`
	}
	query := url.Values{"limit": {fmt.Sprint(sessionLimit)}, "source": {"api_server"}}
	if !h.call(w, r, account, http.MethodGet, "/api/sessions?"+query.Encode(), nil, &page) {
		return
	}
	sessions := make([]session, 0, len(page.Data))
	for _, item := range page.Data {
		if !item.Hidden {
			sessions = append(sessions, item.view())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request, account string) {
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if len([]rune(title)) > maxTitleRunes {
		writeError(w, http.StatusBadRequest, "invalid_title", "a title has at most 100 characters")
		return
	}
	var created struct {
		Session hermesSession `json:"session"`
	}
	payload, _ := json.Marshal(map[string]string{"title": title})
	if !h.call(w, r, account, http.MethodPost, "/api/sessions", payload, &created) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": created.Session.view()})
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
		Session hermesSession `json:"session"`
	}
	if !h.call(w, r, account, http.MethodPatch, "/api/sessions/"+id, payload, &updated) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": updated.Session.view()})
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
		Data json.RawMessage `json:"data"`
	}
	if !h.call(w, r, account, http.MethodGet, "/api/sessions/"+id+"/messages?inline_images=false", nil, &page) {
		return
	}
	if len(page.Data) == 0 {
		page.Data = json.RawMessage("[]")
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": page.Data})
}

// chat runs one turn and relays Hermes's server-sent events as they come.
func (h *handler) chat(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Input string `json:"input"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInputBytes)
	if !readJSON(w, r, &body) {
		return
	}
	input := strings.TrimSpace(body.Input)
	if input == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "the message is empty")
		return
	}
	payload, _ := json.Marshal(map[string]string{"input": input})
	upstream, err := h.request(r.Context(), account, http.MethodPost, "/api/sessions/"+id+"/chat/stream", payload)
	if err != nil {
		h.unavailable(w, account, err)
		return
	}
	response, err := h.client(account).Do(upstream)
	if err != nil {
		h.unavailable(w, account, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		h.relayError(w, response)
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var watcher submissionWatcher
	// After the turn the submission has been recorded; the member's stop
	// cancels the request, so the attaching uses its own deadline.
	defer h.attach(account, r.PathValue("id"), &watcher)
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			watcher.observe(buffer[:n])
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && r.Context().Err() == nil {
				h.logger.Warn("assistant stream ended early", "account", account, "error", readErr)
			}
			return
		}
	}
}

// submitTool is the A-NAS MCP tool "提交更改" as Hermes names it.
const submitTool = "mcp__anas__submit_changes"

// submissionWatcher reads the relayed events for the assistant's calls to
// 提交更改: tool.started carries the call's arguments, the summary and the
// operations the change store recorded.
type submissionWatcher struct {
	pending     []byte
	submissions []json.RawMessage
}

func (s *submissionWatcher) observe(chunk []byte) {
	s.pending = append(s.pending, chunk...)
	for {
		end := bytes.Index(s.pending, []byte("\n\n"))
		if end < 0 {
			break
		}
		s.event(s.pending[:end])
		s.pending = s.pending[end+2:]
	}
	if len(s.pending) > maxInputBytes<<4 {
		s.pending = nil
	}
}

func (s *submissionWatcher) event(block []byte) {
	var name string
	var data []byte
	for _, line := range bytes.Split(block, []byte("\n")) {
		if value, ok := bytes.CutPrefix(line, []byte("event:")); ok {
			name = string(bytes.TrimSpace(value))
		} else if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = append(data, bytes.TrimSpace(value)...)
		}
	}
	if name != "tool.started" {
		return
	}
	var started struct {
		ToolName string          `json:"tool_name"`
		Args     json.RawMessage `json:"args"`
	}
	if json.Unmarshal(data, &started) == nil && started.ToolName == submitTool && len(started.Args) > 0 && string(started.Args) != "null" {
		s.submissions = append(s.submissions, started.Args)
	}
}

// attach records the conversation of each change submitted in the turn, so
// its card shows in that conversation only.
func (h *handler) attach(account, session string, watcher *submissionWatcher) {
	if h.options.Changes == nil || len(watcher.submissions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, submission := range watcher.submissions {
		err := h.options.Changes.Attach(ctx, account, session, submission)
		if err != nil && !errors.Is(err, ErrChangeNotFound) {
			h.logger.Warn("attach pending change to conversation", "account", account, "session", session, "error", err)
		}
	}
}

func (h *handler) listChanges(w http.ResponseWriter, r *http.Request, account string) {
	if h.options.Changes == nil {
		writeError(w, http.StatusServiceUnavailable, "changes_unavailable", "pending changes are unavailable")
		return
	}
	changes, err := h.options.Changes.List(r.Context(), account)
	if err != nil {
		h.logger.Warn("list pending changes", "account", account, "error", err)
		writeError(w, http.StatusBadGateway, "changes_unavailable", "pending changes could not be read")
		return
	}
	writeRaw(w, http.StatusOK, changes)
}

func (h *handler) decide(confirm bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, account string) {
		if h.options.Changes == nil {
			writeError(w, http.StatusServiceUnavailable, "changes_unavailable", "pending changes are unavailable")
			return
		}
		id := r.PathValue("id")
		if !changeIDPattern.MatchString(id) {
			writeError(w, http.StatusBadRequest, "invalid_change", "invalid change id")
			return
		}
		change, err := h.options.Changes.Decide(r.Context(), account, id, confirm)
		switch {
		case errors.Is(err, ErrChangeNotFound):
			writeError(w, http.StatusNotFound, "change_not_found", "no such change")
		case errors.Is(err, ErrChangeDecided):
			writeError(w, http.StatusConflict, "change_decided", "this change was already confirmed or rejected")
		case err != nil:
			h.logger.Warn("decide pending change", "account", account, "change", id, "confirm", confirm, "error", err)
			writeError(w, http.StatusBadGateway, "changes_unavailable", "the change could not be decided")
		default:
			h.logger.Info("pending change decided", "account", account, "change", id, "confirm", confirm)
			writeRaw(w, http.StatusOK, change)
		}
	}
}

// call makes a JSON request to the account's instance and decodes the reply
// into into (when not nil). On failure it has already answered the client.
func (h *handler) call(w http.ResponseWriter, r *http.Request, account, method, path string, body []byte, into any) bool {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	request, err := h.request(ctx, account, method, path, body)
	if err != nil {
		h.unavailable(w, account, err)
		return false
	}
	response, err := h.client(account).Do(request)
	if err != nil {
		h.unavailable(w, account, err)
		return false
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		h.relayError(w, response)
		return false
	}
	if into == nil {
		return true
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(into); err != nil {
		h.logger.Warn("assistant reply undecodable", "account", account, "path", path, "error", err)
		writeError(w, http.StatusBadGateway, "assistant_unavailable", "the assistant answered unexpectedly")
		return false
	}
	return true
}

func (h *handler) request(ctx context.Context, account, method, path string, body []byte) (*http.Request, error) {
	key, err := os.ReadFile(filepath.Join(h.options.KeyDir, account+".key"))
	if err != nil {
		return nil, fmt.Errorf("read instance key: %w", err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://assistant"+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(key)))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func (h *handler) client(account string) *http.Client {
	if client, ok := h.clients.Load(account); ok {
		return client.(*http.Client)
	}
	socket := filepath.Join(h.options.SocketDir, account+".sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	}}
	actual, _ := h.clients.LoadOrStore(account, client)
	return actual.(*http.Client)
}

func (h *handler) unavailable(w http.ResponseWriter, account string, err error) {
	h.logger.Warn("assistant unreachable", "account", account, "error", err)
	writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is not reachable")
}

// relayError turns an instance error into ours; a missing session stays 404.
func (h *handler) relayError(w http.ResponseWriter, response *http.Response) {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(detail, &failure)
	switch {
	case response.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "session_not_found", "no such conversation")
	case failure.Error.Code == "invalid_title":
		// Length is checked here first, so what Hermes still refuses is a
		// title another conversation of this member already has.
		writeError(w, http.StatusConflict, "title_in_use", "another conversation already has this title")
	case response.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "assistant_busy", "the assistant is busy, try again shortly")
	default:
		h.logger.Warn("assistant request failed", "status", response.StatusCode, "detail", strings.TrimSpace(string(detail)))
		writeError(w, http.StatusBadGateway, "assistant_failed", "the assistant could not complete the request")
	}
}

func sessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !sessionIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_session", "invalid conversation id")
		return "", false
	}
	return url.PathEscape(id), true
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
