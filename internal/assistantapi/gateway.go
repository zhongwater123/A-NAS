package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const (
	// maxBrowserFrame bounds one frame from the window; a message is at most
	// maxInputBytes.
	maxBrowserFrame = maxInputBytes + 16<<10
	// maxUpstreamBytes bounds one frame or REST reply from Hermes; resuming a
	// long conversation returns its messages.
	maxUpstreamBytes = 64 << 20
	// lingerLimit bounds how long a connection stays open for a turn after
	// its window left; Hermes itself interrupts a turn idle for 10 minutes.
	lingerLimit = 2 * time.Hour
)

// lingerCheck is how often a connection kept for a turn looks whether the
// turn ended; tests shorten it.
var lingerCheck = 5 * time.Second

// upstream is one connection to the member's `hermes serve` /api/ws, made
// through the session gate for one window. It can outlive the window while
// a turn runs, because the instance exits when no connection is left.
type upstream struct {
	h       *handler
	m       *member
	conn    *websocket.Conn
	session string // the member's session token, for the executor

	mu      sync.Mutex
	browser *websocket.Conn
	// forwarded maps the ids the Product Service sent Hermes to the window's
	// own ids; waiters are its own calls.
	forwarded map[string]forwardedCall
	waiters   map[string]chan frame
	next      int
	// serverRequests maps an approval's request id to the id of Hermes's
	// request, which request.cancel names.
	serverRequests map[string]string
	// watching are the live sessions the window opened. Hermes sends a
	// session's events only to the connection that opened it last; the other
	// windows of the member that have it open get them from here.
	watching  map[string]bool
	ready     json.RawMessage
	closed    chan struct{}
	closeOnce sync.Once
}

type forwardedCall struct {
	id     json.RawMessage
	method string
	params map[string]json.RawMessage
}

func (u *upstream) isClosed() bool {
	select {
	case <-u.closed:
		return true
	default:
		return false
	}
}

func (u *upstream) close() {
	u.closeOnce.Do(func() {
		close(u.closed)
		_ = u.conn.Close(websocket.StatusNormalClosure, "")
		u.m.mu.Lock()
		delete(u.m.upstreams, u)
		u.m.mu.Unlock()
		u.mu.Lock()
		for id, waiter := range u.waiters {
			close(waiter)
			delete(u.waiters, id)
		}
		u.mu.Unlock()
	})
}

func (u *upstream) send(f frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return u.conn.Write(ctx, websocket.MessageText, data)
}

func (u *upstream) toBrowser(f frame) {
	u.mu.Lock()
	browser := u.browser
	u.mu.Unlock()
	if browser == nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = browser.Write(ctx, websocket.MessageText, data)
}

// call makes the Product Service's own request to Hermes.
func (u *upstream) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reply := make(chan frame, 1)
	u.mu.Lock()
	u.next++
	id := "a" + strconv.Itoa(u.next)
	u.waiters[id] = reply
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.waiters, id)
		u.mu.Unlock()
	}()
	if err := u.send(frame{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(id)), Method: method, Params: data}); err != nil {
		return nil, err
	}
	select {
	case answer, ok := <-reply:
		if !ok {
			return nil, errors.New("assistant connection closed")
		}
		if len(answer.Error) > 0 {
			return nil, fmt.Errorf("%s failed: %s", method, truncate(answer.Error))
		}
		return answer.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// answer replies to a request from Hermes with an approval choice.
func (u *upstream) answer(id json.RawMessage, choice string) {
	_ = u.send(rpcResult(id, map[string]string{"choice": choice}))
}

func (u *upstream) watch(session string) {
	if !runtimeIDPattern.MatchString(session) {
		return
	}
	u.mu.Lock()
	u.watching[session] = true
	u.mu.Unlock()
}

func (u *upstream) watches(session string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.watching[session]
}

// toWatchers passes a session's event to the member's other windows that
// have the session open.
func (u *upstream) toWatchers(session string, f frame) {
	if session == "" {
		return
	}
	u.m.mu.Lock()
	others := make([]*upstream, 0, len(u.m.upstreams))
	for other := range u.m.upstreams {
		if other != u {
			others = append(others, other)
		}
	}
	u.m.mu.Unlock()
	for _, other := range others {
		if other.watches(session) {
			other.toBrowser(f)
		}
	}
}

func (u *upstream) rememberServerRequest(requestID string, id json.RawMessage) {
	var srq string
	_ = json.Unmarshal(id, &srq)
	u.mu.Lock()
	u.serverRequests[requestID] = srq
	u.mu.Unlock()
}

func (u *upstream) serverRequestOf(requestID string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.serverRequests[requestID]
}

// gateway serves the window's WebSocket: the member's own instance through the
// session gate, with only the allowed part of `hermes serve` passed either way.
func (h *handler) gateway(w http.ResponseWriter, r *http.Request, account string) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, http.StatusUpgradeRequired, "upgrade_required", "the assistant gateway is a WebSocket")
		return
	}
	// Server read and write timeouts stay attached to hijacked connections.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	// Accept checks that the Origin is this host: the session cookie alone
	// must not let another site drive the assistant.
	browser, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.logger.Warn("assistant gateway upgrade rejected", "error", err)
		return
	}
	browser.SetReadLimit(maxBrowserFrame)
	token := accounts.SessionToken(r.Context())
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()

	conn, err := h.dialGateway(ctx, account)
	if err != nil {
		h.logger.Warn("assistant gateway unavailable", "account", account, "error", err)
		_ = browser.Close(closeStatus(err))
		return
	}
	m := h.member(account)
	u := &upstream{h: h, m: m, conn: conn, session: token, browser: browser,
		forwarded: map[string]forwardedCall{}, waiters: map[string]chan frame{}, serverRequests: map[string]string{},
		watching: map[string]bool{}, closed: make(chan struct{})}
	m.mu.Lock()
	m.upstreams[u] = struct{}{}
	m.mu.Unlock()
	go u.readHermes()
	if err := u.declare(ctx); err != nil {
		h.logger.Warn("assistant gateway did not start", "account", account, "error", err)
		u.close()
		_ = browser.Close(websocket.StatusTryAgainLater, "assistant_unavailable")
		return
	}
	u.readBrowser(ctx)
	u.mu.Lock()
	u.browser = nil
	u.mu.Unlock()
	u.linger(ctx)
	u.close()
}

// closeStatus tells the window why its gateway closed, in the error codes
// the REST routes use.
func closeStatus(err error) (websocket.StatusCode, string) {
	switch {
	case errors.Is(err, ErrSessionRejected):
		return websocket.StatusPolicyViolation, "authentication_required"
	case errors.Is(err, ErrNoAssistant):
		return websocket.StatusPolicyViolation, "assistant_unavailable"
	default:
		return websocket.StatusTryAgainLater, "assistant_unavailable"
	}
}

// declare waits for Hermes's gateway.ready, says that this connection answers
// the server's requests (without it Hermes withdraws every approval at once),
// and passes the ready event on.
func (u *upstream) declare(ctx context.Context) error {
	deadline := time.NewTimer(requestTimeout)
	defer deadline.Stop()
	u.mu.Lock()
	ready := u.ready
	u.mu.Unlock()
	for ready == nil {
		select {
		case <-u.closed:
			return errors.New("instance closed the gateway")
		case <-deadline.C:
			return errors.New("instance did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
		u.mu.Lock()
		ready = u.ready
		u.mu.Unlock()
	}
	if _, err := u.call(ctx, "client.capabilities", map[string]bool{"server_requests": true}); err != nil {
		return err
	}
	u.toBrowser(frame{JSONRPC: "2.0", Method: "event", Params: ready})
	return nil
}

// readHermes handles everything Hermes sends on this connection.
func (u *upstream) readHermes() {
	defer u.close()
	for {
		_, data, err := u.conn.Read(context.Background())
		if err != nil {
			u.mu.Lock()
			browser := u.browser
			u.mu.Unlock()
			if browser != nil {
				// The gate cut the connection (the session ended) or the
				// instance stopped; the window reconnects or learns why.
				_ = browser.Close(websocket.StatusTryAgainLater, "assistant_unavailable")
			}
			return
		}
		var f frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch {
		case f.isEvent():
			u.hermesEvent(f)
		case f.isServerRequest():
			if f.Method == "approval" && serverIDPattern.MatchString(unquote(f.ID)) {
				u.h.approval(u.m, u, f)
			} else {
				// Hermes takes an error as "nobody answered": the tool fails
				// at once instead of waiting out its timeout.
				_ = u.send(rpcError(f.ID, codeMethodNotFound, "not available in A-NAS"))
			}
		case f.isResponse():
			u.hermesResponse(f)
		}
	}
}

func (u *upstream) hermesEvent(f frame) {
	var ev event
	if json.Unmarshal(f.Params, &ev) != nil {
		return
	}
	if ev.Type == "gateway.ready" {
		u.mu.Lock()
		first := u.ready == nil
		u.ready = f.Params
		u.mu.Unlock()
		if first {
			return // declare passes it on
		}
	}
	u.h.observe(u.m, u, ev)
	if !forwardedEvents[ev.Type] {
		return
	}
	if ev.Type == "session.info" {
		var info map[string]json.RawMessage
		if json.Unmarshal(ev.Payload, &info) == nil {
			if sanitizeInfo(info) {
				u.bypassed(ev.SessionID)
				return
			}
			ev.Payload, _ = json.Marshal(info)
			f.Params, _ = json.Marshal(ev)
		}
	}
	u.toBrowser(f)
	u.toWatchers(ev.SessionID, f)
}

// bypassed reacts to a session whose approvals were switched off despite the
// managed policy: nothing more of this connection reaches the window.
func (u *upstream) bypassed(session string) {
	u.h.logger.Error("assistant session runs without approvals; closing its gateway", "account", u.m.account, "session", session)
	u.mu.Lock()
	browser := u.browser
	u.mu.Unlock()
	if browser != nil {
		_ = browser.Close(websocket.StatusPolicyViolation, "assistant_unavailable")
	}
	u.close()
}

func (u *upstream) hermesResponse(f frame) {
	id := unquote(f.ID)
	u.mu.Lock()
	if waiter, ok := u.waiters[id]; ok {
		delete(u.waiters, id)
		u.mu.Unlock()
		waiter <- f
		return
	}
	call, ok := u.forwarded[id]
	delete(u.forwarded, id)
	u.mu.Unlock()
	if !ok {
		return
	}
	f.ID = call.id
	if len(f.Error) == 0 && len(f.Result) > 0 {
		result, ok := u.adjustResult(call, f.Result)
		if !ok {
			u.toBrowser(rpcError(call.id, codeUnavailable, "the assistant is unavailable"))
			return
		}
		f.Result = result
	}
	u.toBrowser(f)
}

// adjustResult follows the sessions a result names and removes what the window
// must not see: hidden info, and requests from the server, which are the
// Product Service's to answer.
func (u *upstream) adjustResult(call forwardedCall, raw json.RawMessage) (json.RawMessage, bool) {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return raw, true
	}
	var session string
	_ = json.Unmarshal(call.params["session_id"], &session)
	switch call.method {
	case "session.create", "session.resume", "session.activate":
		var live, stored string
		_ = json.Unmarshal(result["session_id"], &live)
		if json.Unmarshal(result["stored_session_id"], &stored) != nil || stored == "" {
			_ = json.Unmarshal(result["session_key"], &stored)
		}
		if call.method == "session.resume" && stored == "" {
			stored = unquote(call.params["session_id"])
		}
		u.m.bind(live, stored, u)
		u.watch(live)
		var running bool
		if json.Unmarshal(result["running"], &running) == nil && running {
			u.m.openTurn(live, u)
		}
		delete(result, "open_requests")
		delete(result, "pending_connection")
		delete(result, "pending_approval")
	case "prompt.submit":
		u.m.openTurn(session, u)
	case "session.interrupt":
		u.m.stopping(session)
	case "session.events.since":
		var events []json.RawMessage
		_ = json.Unmarshal(result["events"], &events)
		kept := make([]json.RawMessage, 0, len(events))
		for _, raw := range events {
			var ev event
			if json.Unmarshal(raw, &ev) != nil || !forwardedEvents[ev.Type] || ev.Type == "gateway.ready" {
				continue
			}
			kept = append(kept, raw)
		}
		result["events"], _ = json.Marshal(kept)
		delete(result, "open_requests")
	}
	if rawInfo, ok := result["info"]; ok {
		var info map[string]json.RawMessage
		if json.Unmarshal(rawInfo, &info) == nil {
			if sanitizeInfo(info) {
				u.bypassed(session)
				return nil, false
			}
			result["info"], _ = json.Marshal(info)
		}
	}
	adjusted, err := json.Marshal(result)
	if err != nil {
		return raw, true
	}
	return adjusted, true
}

// readBrowser passes the window's allowed requests to Hermes until it leaves.
func (u *upstream) readBrowser(ctx context.Context) {
	u.mu.Lock()
	browser := u.browser
	u.mu.Unlock()
	for {
		_, data, err := browser.Read(ctx)
		if err != nil {
			return
		}
		var f frame
		if json.Unmarshal(data, &f) != nil || f.Method == "" || len(f.ID) == 0 {
			// Answers to requests from the server are never the window's to
			// give, and anything else is not a request.
			continue
		}
		if localMethods[f.Method] {
			u.toBrowser(u.local(f))
			continue
		}
		params, err := checkParams(f.Method, f.Params)
		if err != nil {
			code := codeInvalidParams
			if _, known := clientMethods[f.Method]; !known {
				code = codeMethodNotFound
			}
			u.toBrowser(rpcError(f.ID, code, err.Error()))
			continue
		}
		u.mu.Lock()
		u.next++
		id := "b" + strconv.Itoa(u.next)
		u.forwarded[id] = forwardedCall{id: f.ID, method: f.Method, params: params}
		u.mu.Unlock()
		clean, _ := json.Marshal(params)
		if err := u.send(frame{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(id)), Method: f.Method, Params: clean}); err != nil {
			u.mu.Lock()
			delete(u.forwarded, id)
			u.mu.Unlock()
			u.toBrowser(rpcError(f.ID, codeUnavailable, "the assistant is unavailable"))
		}
	}
}

// local answers the methods that are the Product Service's own.
func (u *upstream) local(f frame) frame {
	switch f.Method {
	case "client.capabilities":
		return rpcResult(f.ID, map[string]any{"server_requests": []string{}})
	case "approval.pending":
		var params struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(f.Params, &params)
		pending := []card{}
		for _, c := range u.m.list() {
			if c.SessionID == params.SessionID {
				pending = append(pending, c)
			}
		}
		return rpcResult(f.ID, map[string]any{"approvals": pending})
	default: // approval.received: the card is A-NAS's, nothing to tell Hermes
		return rpcResult(f.ID, map[string]bool{"acknowledged": true})
	}
}

// linger keeps the connection open after the window left while a turn runs on
// it, so the instance does not exit idle under the turn. The session gate
// still cuts it when the member's session ends.
func (u *upstream) linger(ctx context.Context) {
	limit := time.NewTimer(lingerLimit)
	defer limit.Stop()
	ticker := time.NewTicker(lingerCheck)
	defer ticker.Stop()
	for u.m.busy(u) {
		select {
		case <-u.closed:
			return
		case <-ctx.Done():
			return
		case <-limit.C:
			u.h.logger.Warn("assistant turn outlived its window for too long; letting the instance go", "account", u.m.account)
			return
		case <-ticker.C:
		}
	}
}

func unquote(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}
