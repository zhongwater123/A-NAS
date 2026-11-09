package assistantapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// The companion tools of members' assistants (#412) reach the Product Service
// on a socket of their own, which systemd binds into each assistant's sandbox
// (ai/assistant-spike/anas-companion-assistant.socket); the scheduled tasks'
// sandboxes do not have it. The tools move and animate the companion in the
// VNAS window of the conversation that called them.
//
// A call names no conversation: Hermes's MCP client sends only the tool and
// its arguments, and every conversation of the member shares one companion
// server. So the call is paired with the tool.start Hermes reported on the
// member's connections: one of a running turn, under a companion tool's name,
// not a subagent's mirror, with the same tool and the same arguments, and
// waiting in exactly one conversation. The command then goes, as an
// anas.companion event shaped like anas.approval, to that conversation's
// browser, the window that registered for it and sent its prompt, and to no
// other window. That window answers with companion.result. Everything else is
// answered as the companion contract says: no_desktop when nothing pairs or
// no window is the browser (a scheduled task, a subagent, a reconnect in the
// middle of a turn), busy when the pairing is ambiguous or a limit is reached,
// timeout when the window does not answer, and cancelled when the turn ends or
// the window goes first.

const (
	// companionPrefix is how Hermes names the companion server's tools.
	companionPrefix = "mcp__companion__"
	// mirroredTool starts the ids of the tool.start Hermes makes up on a
	// subagent's own session, with no arguments.
	mirroredTool = "submirror:"
	// maxCompanionCall bounds one call on the socket, and
	// maxCompanionAnswer the window's answer to one command.
	maxCompanionCall   = 8 << 10
	maxCompanionAnswer = 4 << 10
	// maxCompanionStarts bounds the calls one turn may have waiting.
	maxCompanionStarts = 32
	// companionRate is how many commands a member's windows get a minute;
	// companionRateEach caps some tools within it.
	companionRate = 20
	// maxLockTTL is the longest lock in milliseconds; dsh-pet allows 30 minutes.
	maxLockTTL = 300000
)

var (
	// companionReceipt is how long a command waits for the window's answer:
	// the window answers within 12 s, an animation once it ended.
	companionReceipt = 15 * time.Second
	// companionLate is how long a call waits for its tool.start, which comes
	// on another connection and may arrive after it.
	companionLate = 1500 * time.Millisecond
	// companionHelper is how long after a subagent started a companion tool
	// its call may still arrive: Hermes's 30 s per call, and some.
	companionHelper = 35 * time.Second
	// companionMinute is the limits' window.
	companionMinute = time.Minute

	companionRateEach = map[string]int{"broadcast": 6, "throw": 6}
)

// Statuses of a companion tool's result besides the window's own.
const (
	companionNoDesktop = "no_desktop"
	companionBusy      = "busy"
	companionTimeout   = "timeout"
	companionCancelled = "cancelled"
	companionRejected  = "rejected"
)

// companionStart is a companion tool's call a turn started.
type companionStart struct {
	toolID string
	tool   string
	// args are the call's arguments in canonical form (canonicalArgs).
	args string
}

// companionCommand is a command sent to a window and not yet settled.
type companionCommand struct {
	id      string
	session string
	browser *upstream
	event   frame
	// answer gets the tool's result once; nil for the Product Service's own
	// release of a lock, whose answer nobody waits for.
	answer chan map[string]any
}

type sentCommand struct {
	at   time.Time
	tool string
}

// send passes the command to its window.
func (c *companionCommand) send() { c.browser.toBrowser(c.event) }

// AccountResolver names the active user who owns a Linux UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accounts.SessionUser, error)
}

// CompanionServer serves the companion tools' socket: POST /call, nothing else.
// The peer's UID says whose assistant called, as on the photo service's socket
// for assistants.
func (h *Handler) CompanionServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", h.companionCallHTTP)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return &http.Server{Handler: mux, ConnContext: withPeerUID,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		ErrorLog: slog.NewLogLogger(h.logger.Handler(), slog.LevelError)}
}

type peerUIDKey struct{}

// withPeerUID records on each connection the UID of the process at the other
// end, as the kernel reports it.
func withPeerUID(ctx context.Context, conn net.Conn) context.Context {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return ctx
	}
	return context.WithValue(ctx, peerUIDKey{}, int(cred.Uid))
}

func (h *Handler) companionCallHTTP(w http.ResponseWriter, r *http.Request) {
	uid, ok := r.Context().Value(peerUIDKey{}).(int)
	if !ok || uid <= 0 || h.options.Accounts == nil {
		// Only assistants, running as members, reach this socket.
		h.logger.ErrorContext(r.Context(), "companion socket peer is not a member", "uid", uid, "credentials", ok)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the companion is not available on this device")
		return
	}
	user, err := h.options.Accounts.ResolveAccountUID(r.Context(), uid)
	switch {
	case errors.Is(err, accounts.ErrUserNotFound):
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "account lookup failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the companion is not available on this device")
		return
	case !accountid.ValidAssistantAccount(user.Username):
		httpjson.WriteError(w, http.StatusForbidden, "assistant_unavailable", "this account has no assistant")
		return
	}
	var call struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := httpjson.Decode(w, r, maxCompanionCall, &call); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_request", "the request body is not valid JSON")
		return
	}
	httpjson.Write(w, http.StatusOK, h.companionCall(r.Context(), user.Username, call.Tool, call.Args))
}

// companionCall carries out one call of a companion tool for account and
// returns the tool's result.
func (h *Handler) companionCall(ctx context.Context, account, tool string, raw json.RawMessage) map[string]any {
	args, canonical, problem := checkCompanion(tool, raw)
	if problem != "" {
		return map[string]any{"status": companionRejected, "reason": "bad-request", "message": problem}
	}
	h.membersMu.Lock()
	m := h.members[account]
	h.membersMu.Unlock()
	if m == nil {
		return map[string]any{"status": companionNoDesktop}
	}
	session, found, ambiguous := m.waitStart(ctx, tool, canonical)
	switch {
	case ambiguous:
		return map[string]any{"status": companionBusy}
	case !found:
		return map[string]any{"status": companionNoDesktop}
	}
	c, refusal := m.command(session, tool, args)
	if refusal != nil {
		return refusal
	}
	h.logger.Debug("companion command sent", "account", account, "session", session, "tool", tool)
	c.send()
	timer := time.NewTimer(companionReceipt)
	defer timer.Stop()
	select {
	case result := <-c.answer:
		return result
	case <-timer.C:
		m.settle(c.id, map[string]any{"status": companionTimeout})
		return <-c.answer
	case <-ctx.Done():
		// The companion server stopped waiting; nobody reads the result.
		m.settle(c.id, map[string]any{"status": companionCancelled})
		return <-c.answer
	}
}

// waitStart pairs a call with its tool.start, waiting companionLate for one
// that has not arrived yet.
func (m *member) waitStart(ctx context.Context, tool, args string) (session string, found, ambiguous bool) {
	deadline := time.NewTimer(companionLate)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		session, found, ambiguous = m.pair(tool, args, time.Now())
		if found || ambiguous {
			m.mu.Unlock()
			return session, found, ambiguous
		}
		if m.started == nil {
			m.started = make(chan struct{})
		}
		started := m.started
		m.mu.Unlock()
		select {
		case <-started:
		case <-deadline.C:
			return "", false, false
		case <-ctx.Done():
			return "", false, false
		}
	}
}

// pair takes the oldest started call of tool with these arguments, when the
// member has such calls in exactly one running turn and no subagent of that
// conversation may be making the same call. Under m.mu.
func (m *member) pair(tool, args string, now time.Time) (session string, found, ambiguous bool) {
	matches := func(c companionStart) bool { return c.tool == tool && c.args == args }
	for id, s := range m.sessions {
		if s.turn && slices.ContainsFunc(s.companion, matches) {
			if session != "" {
				return "", false, true
			}
			session = id
		}
	}
	if session == "" {
		return "", false, false
	}
	s := m.sessions[session]
	if s.helping(now) {
		return "", false, true
	}
	i := slices.IndexFunc(s.companion, matches)
	s.companion = slices.Delete(s.companion, i, i+1)
	return session, true, false
}

// companionStarted records the start of a companion tool's call in a running
// turn. Under m.mu.
func (m *member) companionStarted(s *liveSession, toolID, name string, rawArgs json.RawMessage) {
	tool, ok := strings.CutPrefix(name, companionPrefix)
	if !ok || !s.turn || toolID == "" || strings.HasPrefix(toolID, mirroredTool) {
		return
	}
	args, ok := canonicalArgs(rawArgs)
	if !ok || slices.ContainsFunc(s.companion, func(c companionStart) bool { return c.toolID == toolID }) {
		return
	}
	if len(s.companion) == maxCompanionStarts {
		s.companion = s.companion[1:]
	}
	s.companion = append(s.companion, companionStart{toolID: toolID, tool: tool, args: args})
	if m.started != nil {
		close(m.started)
		m.started = nil
	}
}

// companionDone forgets a call that completed without being paired.
func (s *liveSession) companionDone(toolID string) {
	s.companion = slices.DeleteFunc(s.companion, func(c companionStart) bool { return c.toolID == toolID })
}

// helperTool follows a subagent's tools on its parent's session: a companion
// tool it starts makes no tool.start, so its call could pass for one of the
// conversation's own.
func (s *liveSession) helperTool(started bool, helper, name string) {
	if started && strings.HasPrefix(name, companionPrefix) {
		if s.helpers == nil {
			s.helpers = map[string]time.Time{}
		}
		s.helpers[helper] = time.Now()
		return
	}
	delete(s.helpers, helper)
}

// helping reports a subagent whose companion tool may still be calling.
func (s *liveSession) helping(now time.Time) bool {
	for helper, at := range s.helpers {
		if now.Sub(at) < companionHelper {
			return true
		}
		delete(s.helpers, helper)
	}
	return false
}

// prompted records the connection whose window sent a session's prompt.
func (m *member) prompted(session string, u *upstream) {
	if !runtimeIDPattern.MatchString(session) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session(session).prompter = u
}

// command makes the command for a paired call, or the result that refuses it:
// no desktop unless the conversation's browser is the window that sent its
// prompt and is still there, busy over the limits.
func (m *member) command(session, tool string, args map[string]any) (*companionCommand, map[string]any) {
	id, err := newCommandID()
	if err != nil {
		return nil, map[string]any{"status": companionNoDesktop}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[session]
	if s == nil || !s.turn || s.browser == nil || s.browser != s.prompter || !s.browser.hasWindow() {
		return nil, map[string]any{"status": companionNoDesktop}
	}
	if !m.admit(tool, time.Now()) {
		return nil, map[string]any{"status": companionBusy, "reason": "rate-limited"}
	}
	if tool == "lock" {
		s.lockedBy = s.browser
	}
	c := m.register(id, session, s.browser, tool, args, make(chan map[string]any, 1))
	return c, nil
}

// register keeps a command until it is settled. Under m.mu.
func (m *member) register(id, session string, browser *upstream, tool string, args map[string]any, answer chan map[string]any) *companionCommand {
	c := &companionCommand{id: id, session: session, browser: browser, answer: answer,
		event: eventFrame("anas.companion", session, map[string]any{"command_id": id, "action": tool, "args": args})}
	m.commands[id] = c
	return c
}

// admit counts a command against the member's limits, or reports that it
// would go over one. Under m.mu.
func (m *member) admit(tool string, now time.Time) bool {
	m.sent = slices.DeleteFunc(m.sent, func(c sentCommand) bool { return now.Sub(c.at) >= companionMinute })
	if len(m.sent) >= companionRate {
		return false
	}
	if limit, ok := companionRateEach[tool]; ok {
		same := 0
		for _, c := range m.sent {
			if c.tool == tool {
				same++
			}
		}
		if same >= limit {
			return false
		}
	}
	m.sent = append(m.sent, sentCommand{at: now, tool: tool})
	return true
}

// settle gives a command its result, unless it already has one.
func (m *member) settle(id string, result map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settleLocked(id, result)
}

func (m *member) settleLocked(id string, result map[string]any) {
	c, ok := m.commands[id]
	if !ok {
		return
	}
	delete(m.commands, id)
	if c.answer != nil {
		c.answer <- result
	}
}

// companionTurnEnded settles a turn's commands as cancelled and returns the
// release of a lock one of them took, for the caller to send. Under m.mu.
func (m *member) companionTurnEnded(session string, s *liveSession) *companionCommand {
	s.companion = nil
	for id, c := range m.commands {
		if c.session == session && c.answer != nil {
			m.settleLocked(id, map[string]any{"status": companionCancelled, "reason": "turn-ended"})
		}
	}
	browser := s.lockedBy
	s.lockedBy = nil
	if browser == nil || !browser.hasWindow() {
		return nil
	}
	// Only the Product Service sends force: the window lets go of the lock this
	// conversation took, and of no other.
	id, err := newCommandID()
	if err != nil {
		return nil
	}
	c := m.register(id, session, browser, "lock_release", map[string]any{"force": true}, nil)
	time.AfterFunc(companionReceipt, func() { m.settle(id, nil) })
	return c
}

// windowLeft settles the commands sent to u's window as cancelled once it
// left; it lets go of its locks itself.
func (m *member) windowLeft(u *upstream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.commands {
		if c.browser == u {
			m.settleLocked(id, map[string]any{"status": companionCancelled, "reason": "window-closed"})
		}
	}
	for _, s := range m.sessions {
		if s.lockedBy == u {
			s.lockedBy = nil
		}
	}
}

// hasWindow reports whether the connection's window is still there.
func (u *upstream) hasWindow() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.browser != nil && !u.isClosed()
}

// companionResultParams is what a window may answer a command with.
var companionResultParams = method{params: map[string]check{"session_id": runtimeID, "command_id": pattern(commandIDPattern),
	"ok": boolean, "result": object(maxCompanionAnswer), "error": object(maxCompanionAnswer)},
	required: []string{"session_id", "command_id", "ok"}}

// object accepts a JSON object up to maxBytes.
func object(maxBytes int) check {
	return func(raw json.RawMessage) bool {
		var value map[string]json.RawMessage
		return len(raw) <= maxBytes && json.Unmarshal(raw, &value) == nil && value != nil
	}
}

// companionResult takes a window's answer to a command it was sent: once,
// and only from the window it went to.
func (u *upstream) companionResult(f frame) frame {
	params, err := companionResultParams.check(f.Params)
	if err != nil {
		return rpcError(f.ID, codeInvalidParams, err.Error())
	}
	var ok bool
	_ = json.Unmarshal(params["ok"], &ok)
	answer, present := params["result"]
	if !ok {
		answer, present = params["error"]
	}
	if !present {
		return rpcError(f.ID, codeInvalidParams, "parameter result or error is required")
	}
	id := unquote(params["command_id"])
	m := u.m
	m.mu.Lock()
	c := m.commands[id]
	if c == nil || c.browser != u || c.session != unquote(params["session_id"]) {
		m.mu.Unlock()
		return rpcError(f.ID, codeInvalidParams, "no such command waits for this window")
	}
	m.settleLocked(id, companionAnswer(ok, answer))
	m.mu.Unlock()
	return rpcResult(f.ID, map[string]bool{"accepted": true})
}

var companionReason = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// companionAnswer turns a window's answer into the tool's result. Its
// limit counts as the Product Service's own, and a window that no longer
// takes the conversation's commands as no desktop.
func companionAnswer(ok bool, raw json.RawMessage) map[string]any {
	var answer map[string]any
	_ = json.Unmarshal(raw, &answer)
	if ok {
		switch answer["status"] {
		case "ok", "ended", "cancelled", "failed":
			return answer
		}
		return map[string]any{"status": companionRejected, "reason": "not-ready"}
	}
	reason, _ := answer["reason"].(string)
	switch {
	case reason == "rate-limited":
		return map[string]any{"status": companionBusy, "reason": reason}
	case reason == "not-controlling":
		return map[string]any{"status": companionNoDesktop}
	case !companionReason.MatchString(reason):
		reason = "not-ready"
	}
	result := map[string]any{"status": companionRejected, "reason": reason}
	for _, key := range []string{"message", "expiresAt", "cause"} {
		if value, ok := answer[key]; ok {
			result[key] = value
		}
	}
	return result
}

func newCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// canonicalArgs puts a call's arguments in one form, whoever wrote them: keys
// sorted, numbers as JSON numbers of their value, null values and a missing
// object as no arguments.
func canonicalArgs(raw json.RawMessage) (string, bool) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return "", false
		}
	}
	for key, value := range args {
		if value == nil {
			delete(args, key)
		}
	}
	data, err := json.Marshal(args)
	return string(data), err == nil
}

// companionArg checks one argument of a companion tool and returns the value
// to pass on, or what is wrong with it.
type companionArg func(any) (any, string)

// companionTools are the tools of the companion contract with their
// arguments, as dsh-pet's routes take them less pet, surface, url and image.
var companionTools = map[string]struct {
	args     map[string]companionArg
	required []string
}{
	"anim": {args: map[string]companionArg{"name": animationName, "facing": oneOf("left", "right"), "lock": lockID},
		required: []string{"name"}},
	"broadcast": {args: map[string]companionArg{"text": bubbleText, "duration": number(0, 60000), "anim": toggle, "lock": lockID},
		required: []string{"text"}},
	"bubble_clear": {args: map[string]companionArg{"lock": lockID}},
	"position": {args: map[string]companionArg{"x": clamped(0, 1), "y": clamped(0, 1), "mode": oneOf("instant", "spring", "line"),
		"duration": number(0, 5000), "lock": lockID}, required: []string{"x", "y", "mode"}},
	"throw":        {args: map[string]companionArg{"angle": number(math.Inf(-1), math.Inf(1)), "power": power, "lock": lockID}, required: []string{"angle", "power"}},
	"sleep":        {},
	"wake":         {},
	"lock":         {args: map[string]companionArg{"lockId": lockID, "ttl": lockTTL}},
	"lock_release": {args: map[string]companionArg{"lockId": lockID}, required: []string{"lockId"}},
}

// checkCompanion checks a call's arguments against its tool: every key known,
// every value in range. It returns the arguments to send, clamped where the
// contract clamps, and the call's own arguments in canonical form for the
// pairing, or what is wrong.
func checkCompanion(tool string, raw json.RawMessage) (map[string]any, string, string) {
	rule, ok := companionTools[tool]
	if !ok {
		return nil, "", fmt.Sprintf("没有这个工具：%s", tool)
	}
	canonical, ok := canonicalArgs(raw)
	if !ok {
		return nil, "", "参数必须是一个对象"
	}
	var given map[string]any
	_ = json.Unmarshal([]byte(canonical), &given)
	args := map[string]any{}
	for key, value := range given {
		valid, known := rule.args[key]
		switch {
		case key == "url" || key == "image":
			return nil, "", "伙伴只播放自己的动画、只说纯文本，不接受 url 或 image"
		case !known:
			return nil, "", fmt.Sprintf("%s 不接受参数 %s", tool, key)
		}
		checked, problem := valid(value)
		if problem != "" {
			return nil, "", fmt.Sprintf("%s：%s", key, problem)
		}
		args[key] = checked
	}
	for _, key := range rule.required {
		if _, ok := args[key]; !ok {
			return nil, "", fmt.Sprintf("缺少参数 %s", key)
		}
	}
	return args, canonical, ""
}

func number(minimum, maximum float64) companionArg {
	return func(value any) (any, string) {
		n, ok := value.(float64)
		if !ok || n < minimum || n > maximum {
			if math.IsInf(minimum, -1) {
				return nil, "应为数字"
			}
			return nil, fmt.Sprintf("应为 %g–%g 的数字", minimum, maximum)
		}
		return n, ""
	}
}

func clamped(minimum, maximum float64) companionArg {
	return func(value any) (any, string) {
		n, ok := value.(float64)
		if !ok {
			return nil, "应为数字"
		}
		return min(max(n, minimum), maximum), ""
	}
}

func power(value any) (any, string) {
	n, ok := value.(float64)
	if !ok || n <= 0 || n > 3 {
		return nil, "应为大于 0、不超过 3 的数字"
	}
	return n, ""
}

func lockTTL(value any) (any, string) {
	n, ok := value.(float64)
	if !ok || n < 1000 {
		return nil, fmt.Sprintf("应为 1000–%d 毫秒", maxLockTTL)
	}
	return min(n, maxLockTTL), ""
}

func toggle(value any) (any, string) {
	if _, ok := value.(bool); !ok {
		return nil, "应为 true 或 false"
	}
	return value, ""
}

func oneOf(allowed ...string) companionArg {
	return func(value any) (any, string) {
		if s, ok := value.(string); ok && slices.Contains(allowed, s) {
			return s, ""
		}
		return nil, "应为 " + strings.Join(allowed, "、") + " 之一"
	}
}

// plainText accepts 1 to limit code points without control characters, but
// for line breaks where lines are allowed, and without bidi overrides.
func plainText(value any, limit int, lines bool) (string, bool) {
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > limit {
		return "", false
	}
	for _, r := range s {
		if (unicode.IsControl(r) && (!lines || r != '\n')) || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return "", false
		}
	}
	return s, true
}

func bubbleText(value any) (any, string) {
	if s, ok := plainText(value, 200, true); ok {
		return s, ""
	}
	return nil, "应为 1–200 字的纯文本，可以换行"
}

func animationName(value any) (any, string) {
	if s, ok := plainText(value, 64, false); ok {
		return s, ""
	}
	return nil, "应为动画名"
}

func lockID(value any) (any, string) {
	if s, ok := plainText(value, 128, false); ok {
		return s, ""
	}
	return nil, "应为 lock 返回的 lockId"
}
