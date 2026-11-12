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
	"strconv"
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
//
// The tools that operate the desktop (desktop.go) take the same way: another
// MCP server in the sandbox, the same socket, the same pairing, and the same
// window. What differs between the two is in windowTools.

// windowTools is one family of tools that act in the member's VNAS window:
// the companion's, or those that operate the desktop.
type windowTools struct {
	// prefix is how Hermes names the family's tools.
	prefix string
	tools  map[string]toolRule
	// event carries a command to the window, and cancel tells the window that
	// a command it has not answered is no longer waited for; a family whose
	// window need not know has none.
	event, cancel string
	// receipt is how long a command waits for the window's answer.
	receipt *time.Duration
	// call bounds one call of the family's tools on the socket, and result is
	// what a window may answer one of its commands with.
	call   int
	result method
	// rate is how many commands a member's windows get a minute, and rateEach
	// caps some tools within it.
	rate     int
	rateEach map[string]int
	// answer turns a window's answer into the tool's result.
	answer func(ok bool, raw json.RawMessage) map[string]any
	// refuse says why keys a tool does not take are refused.
	refuse func(tool string, extra []string) string
}

// toolRule is a tool's arguments and the ones a call must give.
type toolRule struct {
	args     map[string]companionParam
	required []string
}

var companionFamily = &windowTools{prefix: companionPrefix, tools: companionTools, event: "anas.companion",
	receipt: &companionReceipt, call: maxCompanionCall, result: companionResultParams,
	rate: companionRate, rateEach: companionRateEach, answer: companionAnswer, refuse: extraProblem}

// windowFamilies are the families whose tools' calls are paired.
var windowFamilies = []*windowTools{companionFamily, desktopFamily}

// windowTool names the family a tool of Hermes's belongs to and the tool
// within it.
func windowTool(name string) (*windowTools, string, bool) {
	for _, family := range windowFamilies {
		if tool, ok := strings.CutPrefix(name, family.prefix); ok {
			_, known := family.tools[tool]
			return family, tool, known
		}
	}
	return nil, "", false
}

const (
	// companionPrefix is how Hermes names the companion server's tools.
	companionPrefix = "mcp__companion__"
	// mirroredTool starts the ids of the tool.start Hermes makes up on a
	// subagent's own session, with no arguments.
	mirroredTool = "submirror:"
	// maxCompanionCall bounds one call of a companion tool on the socket, and
	// maxCompanionAnswer the window's answer to one command.
	maxCompanionCall   = 8 << 10
	maxCompanionAnswer = 4 << 10
	// maxCompanionStarts bounds the calls one turn may have waiting.
	maxCompanionStarts = 32
	// maxCompanionWaiting bounds a member's calls under way on the socket:
	// Hermes's one, and a few it gave up on that are still settling.
	maxCompanionWaiting = 4
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
	// companionBody is how long a caller on the socket has to send a call
	// once its headers came.
	companionBody = 5 * time.Second
	// companionHelper is how long after a subagent started a companion tool
	// its call may still arrive, and companionStale the same for a turn's own
	// tool.start: Hermes's 120 s per call (managed-config.yaml), and some. A
	// start older than that belongs to a call Hermes gave up on, or to an
	// instance that stopped before its turn ended.
	companionHelper = 130 * time.Second
	companionStale  = 130 * time.Second
	// companionDelegation is how long a subagent that reported neither its end
	// nor anything else still counts as running.
	companionDelegation = 10 * time.Minute
	// companionAbandoned is how long the calls a stopped turn left unpaired may
	// still reach the socket: they were on their way when Hermes gave up on
	// them.
	companionAbandoned = 5 * time.Second
	// companionStall is how long a window that left a command unanswered gets
	// no more commands, unless it answers again: the calls queued behind the
	// first are answered timeout at once instead of 15 s each, which would
	// add up past Hermes's limit (it runs one call per server at a time and
	// counts from the queue).
	companionStall = 30 * time.Second
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

// companionStart is a call of a window tool a turn started.
type companionStart struct {
	toolID string
	family *windowTools
	tool   string
	// args are the call's arguments in canonical form (companionValues).
	args string
	// problem says why the call is refused when the model gave keys the
	// tool does not take, which the companion server never passes on.
	problem string
	at      time.Time
	// abandoned marks a call of a stopped turn, at is then when it stopped.
	abandoned bool
}

// companionCommand is a command sent to a window and not yet settled.
type companionCommand struct {
	id      string
	family  *windowTools
	session string
	browser *upstream
	event   frame
	// answer gets the tool's result once; nil for the Product Service's own
	// release of a lock, whose answer nobody waits for.
	answer chan map[string]any
	// way is how far the command got on its way to its window, and overdue
	// that it was settled while send was writing it: the window is then told
	// once the command is there (arrived). Under m.mu.
	way     commandWay
	overdue bool
}

// commandWay is how far a command got on its way to its window.
type commandWay int

const (
	// commandHeld has not been written: settled now, send drops it.
	commandHeld commandWay = iota
	// commandLeaving is being written by send.
	commandLeaving
	// commandThere was written to the window.
	commandThere
)

type sentCommand struct {
	at     time.Time
	family *windowTools
	tool   string
}

// send passes the command to its window, unless it was settled first: the
// end of the turn may settle it between command and send. A command of a
// turn the member is stopping is settled as cancelled instead: it may have
// waited behind another for its window past the stop. Commands go to a
// window one at a time, so the release the end of a turn sends never
// overtakes a lock still on its way. send gives up waiting for its turn when
// ctx ends, and gives up writing at ctx's deadline: a window that takes no
// frames holds up no more than ctx allows.
//
// The write ends only at the deadline, not when ctx is cancelled: the
// websocket closes the whole connection when a write's context ends, even
// just after the frame went out, and the companion server giving up on one
// call must not cut the member's VNAS window off.
func (c *companionCommand) send(ctx context.Context, m *member) {
	slot := c.browser.companionSlot()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-slot }()
	m.mu.Lock()
	_, waiting := m.commands[c.id]
	if s := m.sessions[c.session]; waiting && c.answer != nil && s != nil && s.stopping {
		// The member stopped the turn while the command waited for its window.
		m.settleLocked(c.id, map[string]any{"status": companionCancelled, "reason": "turn-ended"})
		waiting = false
	}
	if waiting {
		c.way = commandLeaving
	}
	m.mu.Unlock()
	if !waiting {
		return
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(companionReceipt)
	}
	write, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	_ = c.browser.toBrowserWithin(write, c.event)
	c.arrived(m)
}

// arrived records that the command was written to its window, and tells the
// window when the command was settled meanwhile: word that a command is no
// longer waited for never reaches a window before the command, which the
// window would then carry out for nobody.
func (c *companionCommand) arrived(m *member) {
	m.mu.Lock()
	c.way = commandThere
	overdue := c.overdue
	c.overdue = false
	m.mu.Unlock()
	if overdue {
		c.tellGone()
	}
}

// tellGone tells the command's window that the command is no longer waited
// for. On a goroutine of its own: a window that takes no frames must not hold
// up whoever holds m.mu.
func (c *companionCommand) tellGone() {
	if !c.browser.hasWindow() {
		return
	}
	go c.browser.toBrowser(eventFrame(c.family.cancel, c.session, map[string]any{"command_id": c.id}))
}

// sendRelease sends the Product Service's own release of a lock, which
// nobody waits for, giving up after companionReceipt. Its callers run it
// on a goroutine of its own, so that a window that takes no frames holds up
// neither Hermes's events nor the end of a connection.
func (c *companionCommand) sendRelease(m *member) {
	ctx, cancel := context.WithTimeout(context.Background(), companionReceipt)
	defer cancel()
	c.send(ctx, m)
}

// AccountResolver names the active user who owns a Linux UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accounts.SessionUser, error)
}

// CompanionServer serves the socket of the window tools: POST /call for the
// companion's, POST /desktop/call for those that operate the desktop, nothing
// else. The peer's UID says whose assistant called, as on the photo service's
// socket for assistants.
func (h *Handler) CompanionServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", h.windowCallHTTP(companionFamily))
	mux.HandleFunc("POST /desktop/call", h.windowCallHTTP(desktopFamily))
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

// windowCallHTTP takes the calls of one family's tools.
func (h *Handler) windowCallHTTP(family *windowTools) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.companionCallHTTP(w, r, family) }
}

func (h *Handler) companionCallHTTP(w http.ResponseWriter, r *http.Request, family *windowTools) {
	uid, ok := r.Context().Value(peerUIDKey{}).(int)
	if !ok || uid <= 0 || h.options.Accounts == nil {
		// Only assistants, running as members, reach this socket.
		h.logger.ErrorContext(r.Context(), "companion socket peer is not a member", "uid", uid, "credentials", ok)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the companion is not available on this device")
		return
	}
	// The call is read before it counts against the member's bound, and
	// within companionBody: a caller that sends its headers and never its
	// body holds none of the member's places. A refused body keeps the
	// deadline, or the server would wait for the rest of it before answering;
	// a call read loses it, since the server's wait for the connection to
	// close, which ends the call early, would meet it too.
	var call struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	body := http.NewResponseController(w)
	_ = body.SetReadDeadline(time.Now().Add(companionBody))
	if err := httpjson.Decode(w, r, int64(family.call), &call); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_request", "the request body is not valid JSON")
		return
	}
	_ = body.SetReadDeadline(time.Time{})
	if !h.companionCallBegins(uid) {
		httpjson.Write(w, http.StatusOK, map[string]any{"status": companionBusy, "reason": "rate-limited"})
		return
	}
	defer h.companionCallEnds(uid)
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
	httpjson.Write(w, http.StatusOK, h.companionCall(r.Context(), user.Username, family, call.Tool, call.Args))
}

// companionCallBegins counts a call of uid's under way, or reports that uid
// has maxCompanionWaiting already. Hermes makes one call at a time on a
// member's companion server; more come from elsewhere in the sandbox, such
// as its terminal, and each holds a goroutine and an account lookup of the
// service all members share.
func (h *Handler) companionCallBegins(uid int) bool {
	h.companionCallsMu.Lock()
	defer h.companionCallsMu.Unlock()
	if h.companionCalls[uid] >= maxCompanionWaiting {
		return false
	}
	if h.companionCalls == nil {
		h.companionCalls = map[int]int{}
	}
	h.companionCalls[uid]++
	return true
}

func (h *Handler) companionCallEnds(uid int) {
	h.companionCallsMu.Lock()
	defer h.companionCallsMu.Unlock()
	h.companionCalls[uid]--
	if h.companionCalls[uid] <= 0 {
		delete(h.companionCalls, uid)
	}
}

// companionCall carries out one call of a window tool for account and
// returns the tool's result.
func (h *Handler) companionCall(ctx context.Context, account string, family *windowTools, tool string, raw json.RawMessage) map[string]any {
	args, canonical, problem := checkCompanion(family, tool, raw)
	if problem != "" {
		return map[string]any{"status": companionRejected, "reason": "bad-request", "message": problem}
	}
	h.membersMu.Lock()
	m := h.members[account]
	h.membersMu.Unlock()
	if m == nil {
		return map[string]any{"status": companionNoDesktop}
	}
	start, session, found, ambiguous := m.waitStart(ctx, family, tool, canonical)
	switch {
	case ambiguous:
		return map[string]any{"status": companionBusy}
	case !found:
		return map[string]any{"status": companionNoDesktop}
	case start.abandoned:
		// Hermes gave up on this call when the member stopped its turn.
		return map[string]any{"status": companionCancelled, "reason": "turn-ended"}
	case start.problem != "":
		// The model gave url, image or another key the tool does not take; the
		// companion server dropped it, Hermes's tool.start still has it.
		return map[string]any{"status": companionRejected, "reason": "bad-request", "message": start.problem}
	}
	c, refusal := m.command(session, family, tool, args)
	if refusal != nil {
		return refusal
	}
	h.logger.Debug("window command sent", "account", account, "session", session, "tool", family.prefix+tool)
	// The window has the family's receipt to answer, counted from before the
	// command waits for the window's previous ones to go out.
	receipt, cancel := context.WithTimeout(ctx, *family.receipt)
	defer cancel()
	c.send(receipt, m)
	select {
	case result := <-c.answer:
		return result
	case <-receipt.Done():
		if ctx.Err() != nil {
			// The companion server stopped waiting; nobody reads the result.
			m.settle(c.id, map[string]any{"status": companionCancelled})
		} else {
			m.stalled(c)
		}
		return <-c.answer
	}
}

// waitStart pairs a call with its tool.start, waiting companionLate for one
// that has not arrived yet, or for one that makes the pairing ambiguous (pair).
func (m *member) waitStart(ctx context.Context, family *windowTools, tool, args string) (start companionStart, session string, found, ambiguous bool) {
	deadline := time.NewTimer(companionLate)
	defer deadline.Stop()
	late := false
	for {
		m.mu.Lock()
		start, session, found, ambiguous = m.pair(family, tool, args, time.Now(), late)
		if found || ambiguous || late {
			m.mu.Unlock()
			return start, session, found, ambiguous
		}
		if m.started == nil {
			m.started = make(chan struct{})
		}
		started := m.started
		m.mu.Unlock()
		select {
		case <-started:
		case <-deadline.C:
			late = true
		case <-ctx.Done():
			return companionStart{}, "", false, false
		}
	}
}

// pair takes the oldest started call of tool with these arguments, when the
// member has such calls in exactly one running turn and no subagent of any
// conversation may be making the same call: a subagent's call has no
// tool.start of its own and could take another conversation's.
//
// A call a stopped turn left comes before one of the same conversation's next
// turn: it was sent before any made since. A turn counts as stopped from
// the moment the window's session.interrupt goes to Hermes (member.stopping),
// not from its message.complete: Hermes may finish the call under way and
// start the next before it looks at the stop. So the calls of a stopping
// turn, started before the stop or after it, are the stopped turn's too, and
// none of them is sent. A stopped turn's call is not the only call that may
// match, though: Hermes reports a call's start before the call waits for the
// server, and stopping the turn may end it there, never sent. So it is taken
// only once late, after companionLate, by which time another conversation's
// own call has had its tool.start; with one there the pairing is ambiguous.
//
// Before late, nothing is taken while a subagent of the member runs either: a
// subagent's call may reach the socket before the subagent.tool that says it
// made one, as a call may come before its tool.start. Under m.mu.
func (m *member) pair(family *windowTools, tool, args string, now time.Time, late bool) (start companionStart, session string, found, ambiguous bool) {
	matches := func(c companionStart) bool { return c.family == family && c.tool == tool && c.args == args }
	stopped := ""
	for id, s := range m.sessions {
		s.abandoned = slices.DeleteFunc(s.abandoned, func(c companionStart) bool { return now.Sub(c.at) >= companionAbandoned })
		s.companion = slices.DeleteFunc(s.companion, func(c companionStart) bool { return now.Sub(c.at) >= companionStale })
		if stopped == "" && (slices.ContainsFunc(s.abandoned, matches) || s.stopping && slices.ContainsFunc(s.companion, matches)) {
			stopped = id
		}
	}
	for id, s := range m.sessions {
		if id == stopped || !s.turn || s.stopping || !slices.ContainsFunc(s.companion, matches) {
			continue
		}
		if session != "" || stopped != "" {
			return companionStart{}, "", false, true
		}
		session = id
	}
	if stopped != "" {
		if !late {
			return companionStart{}, "", false, false
		}
		s := m.sessions[stopped]
		if i := slices.IndexFunc(s.abandoned, matches); i >= 0 {
			start = s.abandoned[i]
			s.abandoned = slices.Delete(s.abandoned, i, i+1)
			return start, stopped, true, false
		}
		// The turn is still stopping: its call is taken from those waiting.
		i := slices.IndexFunc(s.companion, matches)
		start = s.companion[i]
		start.abandoned = true
		s.companion = slices.Delete(s.companion, i, i+1)
		return start, stopped, true, false
	}
	if session == "" {
		return companionStart{}, "", false, false
	}
	for _, s := range m.sessions {
		if s.helping(now) {
			return companionStart{}, "", false, true
		}
	}
	if !late && m.delegating(now) {
		return companionStart{}, "", false, false
	}
	s := m.sessions[session]
	i := slices.IndexFunc(s.companion, matches)
	start = s.companion[i]
	s.companion = slices.Delete(s.companion, i, i+1)
	return start, session, true, false
}

// companionStarted records the start of a companion tool's call in a running
// turn. In a turn the member is stopping, pair takes it as the stopped turn's
// and it is never sent. Under m.mu.
func (m *member) companionStarted(s *liveSession, toolID, name string, rawArgs json.RawMessage) {
	family, tool, known := windowTool(name)
	if !known || !s.turn || toolID == "" || strings.HasPrefix(toolID, mirroredTool) {
		return
	}
	values, extra, ok := companionValues(family, tool, rawArgs)
	if !ok || slices.ContainsFunc(s.companion, func(c companionStart) bool { return c.toolID == toolID }) {
		return
	}
	if len(s.companion) == maxCompanionStarts {
		s.companion = s.companion[1:]
	}
	s.companion = append(s.companion, companionStart{toolID: toolID, family: family, tool: tool, args: canonicalForm(values),
		problem: family.refuse(tool, extra), at: time.Now()})
	m.wake()
}

// wake lets the calls waiting in waitStart look again. Under m.mu.
func (m *member) wake() {
	if m.started != nil {
		close(m.started)
		m.started = nil
	}
}

// companionDone forgets a call that completed without being paired, or keeps
// it as abandoned when the member is stopping the turn: Hermes then completes
// it without waiting for its call, which may still come. Under m.mu.
func (m *member) companionDone(s *liveSession, toolID string) {
	i := slices.IndexFunc(s.companion, func(c companionStart) bool { return c.toolID == toolID })
	if i < 0 {
		return
	}
	if s.stopping {
		s.abandon(s.companion[i:i+1], time.Now())
	}
	s.companion = slices.Delete(s.companion, i, i+1)
}

// abandon keeps calls Hermes gave up on at now, for those still on their way
// to pair with instead of with a later call that has the same arguments.
func (s *liveSession) abandon(calls []companionStart, now time.Time) {
	s.abandoned = slices.DeleteFunc(s.abandoned, func(c companionStart) bool { return now.Sub(c.at) >= companionAbandoned })
	for _, c := range calls {
		c.at, c.abandoned = now, true
		s.abandoned = append(s.abandoned, c)
	}
	if extra := len(s.abandoned) - maxCompanionStarts; extra > 0 {
		s.abandoned = s.abandoned[extra:]
	}
}

// subagentEvent follows a subagent's events on its parent's session: whether
// it still runs, and whether the tool it started last is a companion tool,
// whose call makes no tool.start and could pass for one of the conversation's
// own. Under m.mu.
func (m *member) subagentEvent(s *liveSession, kind, helper, tool string) {
	if kind == "subagent.complete" {
		delete(s.subagents, helper)
		delete(s.helpers, helper)
		return
	}
	now := time.Now()
	if s.subagents == nil {
		s.subagents = map[string]time.Time{}
	}
	s.subagents[helper] = now
	if kind != "subagent.tool" {
		return
	}
	if family, _, _ := windowTool(tool); family == nil {
		delete(s.helpers, helper)
		return
	}
	if s.helpers == nil {
		s.helpers = map[string]time.Time{}
	}
	s.helpers[helper] = now
	m.wake()
}

// delegating reports a subagent of the member still running. Under m.mu.
func (m *member) delegating(now time.Time) bool {
	running := false
	for _, s := range m.sessions {
		for helper, at := range s.subagents {
			if now.Sub(at) < companionDelegation {
				running = true
			} else {
				delete(s.subagents, helper)
			}
		}
	}
	return running
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

// prompted records the connection whose window sent a session's prompt: for
// the turn under way, or for the next one when Hermes queued the prompt
// behind it. A turn no window prompted, such as Hermes's own wake-up after a
// background task, has no prompter. It runs before openTurn, so the session's
// turn is still the one Hermes's events left: Hermes also queues a prompt
// that comes after a turn's message.complete while it still finishes that
// turn, and then starts the prompt's turn with no message.complete in
// between, so a prompt queued when no turn is under way prompts the next turn
// itself.
func (m *member) prompted(session string, u *upstream, queued bool) {
	if !runtimeIDPattern.MatchString(session) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(session)
	if queued && s.turn {
		s.nextPrompter = u
		return
	}
	s.prompter = u
}

// command makes the command for a paired call, or the result that refuses it:
// no desktop unless the conversation's browser is the window that sent its
// prompt and is still there, cancelled once the member stops the turn, busy
// over the limits.
func (m *member) command(session string, family *windowTools, tool string, args map[string]any) (*companionCommand, map[string]any) {
	id, err := newCommandID()
	if err != nil {
		return nil, map[string]any{"status": companionNoDesktop}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[session]
	if s != nil && s.turn && s.stopping {
		// The member stopped the turn after the call was paired. send would
		// settle the command before writing it too, but by then it would
		// have counted against the limits, and a lock would have left a
		// release for the turn's end to send.
		return nil, map[string]any{"status": companionCancelled, "reason": "turn-ended"}
	}
	if s == nil || !s.turn || s.browser == nil || s.browser != s.prompter || !s.browser.hasWindow() {
		return nil, map[string]any{"status": companionNoDesktop}
	}
	now := time.Now()
	if at, ok := m.stall[s.browser]; ok {
		if now.Sub(at) < companionStall {
			return nil, map[string]any{"status": companionTimeout, "reason": "unresponsive"}
		}
		delete(m.stall, s.browser)
	}
	if !m.admit(family, tool, now) {
		return nil, map[string]any{"status": companionBusy, "reason": "rate-limited"}
	}
	if family == companionFamily && tool == "lock" {
		s.lockedBy = s.browser
	}
	c := m.register(id, session, s.browser, family, tool, args, make(chan map[string]any, 1))
	return c, nil
}

// register keeps a command until it is settled. Under m.mu.
func (m *member) register(id, session string, browser *upstream, family *windowTools, tool string, args map[string]any,
	answer chan map[string]any) *companionCommand {
	c := &companionCommand{id: id, family: family, session: session, browser: browser, answer: answer,
		event: eventFrame(family.event, session, map[string]any{"command_id": id, "action": tool, "args": args})}
	m.commands[id] = c
	return c
}

// admit counts a command against its family's limits for the member, or
// reports that it would go over one. Under m.mu.
func (m *member) admit(family *windowTools, tool string, now time.Time) bool {
	m.sent = slices.DeleteFunc(m.sent, func(c sentCommand) bool { return now.Sub(c.at) >= companionMinute })
	all, same := 0, 0
	for _, c := range m.sent {
		if c.family != family {
			continue
		}
		all++
		if c.tool == tool {
			same++
		}
	}
	if all >= family.rate {
		return false
	}
	if limit, ok := family.rateEach[tool]; ok && same >= limit {
		return false
	}
	m.sent = append(m.sent, sentCommand{at: now, family: family, tool: tool})
	return true
}

// stalled settles a command its window left unanswered as a timeout, and
// holds back that window's next commands for companionStall.
func (m *member) stalled(c *companionCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, waiting := m.commands[c.id]; !waiting {
		return
	}
	m.settleLocked(c.id, map[string]any{"status": companionTimeout})
	if m.stall == nil {
		m.stall = map[*upstream]time.Time{}
	}
	m.stall[c.browser] = time.Now()
}

// settle gives a command its result, unless it already has one.
func (m *member) settle(id string, result map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settleLocked(id, result)
}

// settleLocked gives a command a result that is not its window's answer, and
// tells the window so when the command's family has it told: what the window
// still holds for the command, such as a card asking the member, is over. A
// command that never left for its window needs no word, and one that is
// leaving gets it once it is there (arrived).
func (m *member) settleLocked(id string, result map[string]any) {
	c := m.settled(id, result)
	if c == nil || c.family.cancel == "" {
		return
	}
	switch c.way {
	case commandLeaving:
		c.overdue = true
	case commandThere:
		c.tellGone()
	case commandHeld:
	}
}

// settled gives a command its result, unless it already has one, and returns
// the command it settled. Under m.mu.
func (m *member) settled(id string, result map[string]any) *companionCommand {
	c, ok := m.commands[id]
	if !ok {
		return nil
	}
	delete(m.commands, id)
	if c.answer != nil {
		c.answer <- result
	}
	return c
}

// companionTurnEnded settles a turn's commands as cancelled and returns the
// release of a lock one of them took, for the caller to send. A stopped turn
// leaves its unpaired calls abandoned, and its subagents forgotten: stopping
// a turn stops them too, and not every one reports its end. Under m.mu.
func (m *member) companionTurnEnded(session string, s *liveSession, stopped bool) *companionCommand {
	if stopped {
		s.abandon(s.companion, time.Now())
		s.helpers, s.subagents = nil, nil
	}
	s.companion = nil
	s.prompter, s.nextPrompter = s.nextPrompter, nil
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
	c := m.register(id, session, browser, companionFamily, "lock_release", map[string]any{"force": true}, nil)
	time.AfterFunc(companionReceipt, func() { m.settle(id, nil) })
	return c
}

// connectionClosed ends the turns whose events came on u, now that none of
// their events can come any more, as when the instance stopped in the middle
// of a turn; the member's next conversation has another runtime id, and the
// old turn's calls must not pair beside it. It returns the releases of the
// locks those turns took, for the caller to send. Under m.mu.
func (m *member) connectionClosed(u *upstream) []*companionCommand {
	var releases []*companionCommand
	for id, s := range m.sessions {
		if s.owner != u || !s.turn {
			continue
		}
		s.turn, s.submit, s.stopping = false, nil, false
		// Its companion server stopped with the instance: none of the turn's
		// calls is still on its way.
		s.companion = nil
		if release := m.companionTurnEnded(id, s, true); release != nil {
			releases = append(releases, release)
		}
	}
	return releases
}

// windowLeft settles the commands sent to u's window as cancelled once it
// left; it lets go of its locks itself.
func (m *member) windowLeft(u *upstream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stall, u)
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

// companionResult takes a window's answer to a command of family it was
// sent: once, and only from the window it went to.
func (u *upstream) companionResult(f frame, family *windowTools) frame {
	params, err := family.result.check(f.Params)
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
	// Any answer, even a late one, shows the window is there again.
	delete(m.stall, u)
	c := m.commands[id]
	if c == nil || c.family != family || c.browser != u || c.session != unquote(params["session_id"]) {
		m.mu.Unlock()
		return rpcError(f.ID, codeInvalidParams, "no such command waits for this window")
	}
	m.settled(id, family.answer(ok, answer))
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

// companionValues puts a call's arguments in the form the companion server
// passes them on, whoever wrote them, and names the keys its tool does not
// take. Hermes reports a call's start with the model's own arguments and
// coerces them to the tool's schema only afterwards (tools/arg_coercion.py),
// and the companion server drops nulls and keys its tool does not take. So,
// on both sides of a pairing: a number or boolean written as a string is the
// number or boolean, "null" or a blank where an argument is optional (lock:
// "", as dsh-pet's empty ?lock=) is no argument, and null values and a
// missing object are no arguments.
func companionValues(family *windowTools, tool string, raw json.RawMessage) (values map[string]any, extra []string, ok bool) {
	given := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &given); err != nil || given == nil {
			return nil, nil, false
		}
	}
	rule := family.tools[tool]
	values = map[string]any{}
	for key, value := range given {
		param, known := rule.args[key]
		switch {
		case value == nil:
		case !known:
			extra = append(extra, key)
		default:
			if value = param.loosen(value, !slices.Contains(rule.required, key)); value != nil {
				values[key] = value
			}
		}
	}
	slices.Sort(extra)
	return values, extra, true
}

// canonicalForm writes arguments with their keys sorted and numbers as JSON
// numbers of their value, so that 1 and 1.0 compare equal.
func canonicalForm(values map[string]any) string {
	data, _ := json.Marshal(values)
	return string(data)
}

// extraProblem says why keys a tool does not take are refused.
func extraProblem(tool string, extra []string) string {
	switch {
	case slices.Contains(extra, "url") || slices.Contains(extra, "image"):
		return "伙伴只播放自己的动画、只说纯文本，不接受 url 或 image"
	case len(extra) > 0:
		return fmt.Sprintf("%s 不接受参数 %s", tool, extra[0])
	}
	return ""
}

// companionArg checks one argument of a companion tool and returns the value
// to pass on, or what is wrong with it.
type companionArg func(any) (any, string)

// companionParam is one argument of a companion tool: the JSON type the
// companion server advertises for it, and its check.
type companionParam struct {
	kind  string
	check companionArg
}

func textParam(check companionArg) companionParam {
	return companionParam{kind: "string", check: check}
}

func numberParam(check companionArg) companionParam {
	return companionParam{kind: "number", check: check}
}

var flagParam = companionParam{kind: "boolean", check: toggle}

// loosen takes a value as Hermes's coercion would for the parameter's type.
func (p companionParam) loosen(value any, optional bool) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	word := strings.ToLower(strings.TrimSpace(s))
	switch {
	case optional && (word == "null" || (p.kind == "string" && word == "")):
		return nil
	case p.kind == "number":
		if n, err := strconv.ParseFloat(word, 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n
		}
	case p.kind == "boolean" && (word == "true" || word == "false"):
		return word == "true"
	}
	return value
}

// companionTools are the tools of the companion contract with their
// arguments, as dsh-pet's routes take them less pet, surface, url and image.
var companionTools = map[string]toolRule{
	"anim": {args: map[string]companionParam{"name": textParam(animationName), "facing": textParam(oneOf("left", "right")), "lock": textParam(lockID)},
		required: []string{"name"}},
	"broadcast": {args: map[string]companionParam{"text": textParam(bubbleText), "duration": numberParam(number(0, 60000)), "anim": flagParam,
		"lock": textParam(lockID)}, required: []string{"text"}},
	"bubble_clear": {args: map[string]companionParam{"lock": textParam(lockID)}},
	"position": {args: map[string]companionParam{"x": numberParam(clamped(0, 1)), "y": numberParam(clamped(0, 1)),
		"mode": textParam(oneOf("instant", "spring", "line")), "duration": numberParam(number(0, 5000)), "lock": textParam(lockID)},
		required: []string{"x", "y", "mode"}},
	"throw": {args: map[string]companionParam{"angle": numberParam(number(math.Inf(-1), math.Inf(1))), "power": numberParam(power),
		"lock": textParam(lockID)}, required: []string{"angle", "power"}},
	"sleep":        {},
	"wake":         {},
	"lock":         {args: map[string]companionParam{"lockId": textParam(lockID), "ttl": numberParam(lockTTL)}},
	"lock_release": {args: map[string]companionParam{"lockId": textParam(lockID)}, required: []string{"lockId"}},
}

// checkCompanion checks a call's arguments against its tool: every key known,
// every value in range. It returns the arguments to send, clamped where the
// contract clamps, and the call's own arguments in canonical form for the
// pairing, or what is wrong.
func checkCompanion(family *windowTools, tool string, raw json.RawMessage) (map[string]any, string, string) {
	rule, ok := family.tools[tool]
	if !ok {
		return nil, "", fmt.Sprintf("没有这个工具：%s", tool)
	}
	values, extra, ok := companionValues(family, tool, raw)
	if !ok {
		return nil, "", "参数必须是一个对象"
	}
	if problem := family.refuse(tool, extra); problem != "" {
		return nil, "", problem
	}
	args := map[string]any{}
	for key, value := range values {
		checked, problem := rule.args[key].check(value)
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
	return args, canonicalForm(values), ""
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
// for \n where lines are allowed, without the other line and paragraph
// separators, and without bidi controls (marks, embeddings, overrides and
// isolates), which would reorder what the bubble shows.
func plainText(value any, limit int, lines bool) (string, bool) {
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > limit {
		return "", false
	}
	for _, r := range s {
		if (unicode.IsControl(r) && (!lines || r != '\n')) || unicode.In(r, unicode.Bidi_Control, unicode.Zl, unicode.Zp) {
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
