package assistantapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func init() {
	// A window that does not answer, and a call without its tool.start, are
	// given up on sooner.
	companionReceipt = 2 * time.Second
	companionLate = 600 * time.Millisecond
}

const (
	otherRuntime = "0a1b2c3e"
	registration = `{"session_id":"` + fakeRuntime + `","protocol_version":1,"controller_id":"vnas-window","browser_profile_id":"webos","capabilities":["browser_snapshot"]}`
)

// current is the fake's last connection, where the test plays Hermes's events.
func (f *fakeServe) current() *websocket.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

// companionTurn opens a window that registered as the conversation's browser
// and sent its prompt, and returns it with the connection Hermes's events of
// the turn come on.
func companionTurn(t *testing.T, h *harness) (*window, *websocket.Conn) {
	t.Helper()
	w := h.open()
	conn := h.serve.current()
	w.call("session.create", `{"cols":80}`)
	if response := w.call("browser.controller.register", registration); len(response.Error) > 0 {
		t.Fatalf("registration refused: %s", response.Error)
	}
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"long"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.start") })
	return w, conn
}

// started plays the start of a companion tool's call.
func (h *harness) started(conn *websocket.Conn, session, toolID, tool, args string) {
	h.serve.event(conn, "tool.start", session, `{"tool_id":"`+toolID+`","name":"`+companionPrefix+tool+`","args":`+args+`}`)
}

// observed waits until the Product Service has seen every event played so
// far on conn: it passes a status update of its own to w once it read them.
func (h *harness) observed(w *window, conn *websocket.Conn) {
	w.t.Helper()
	h.marks++
	mark := fmt.Sprintf("mark-%d", h.marks)
	h.serve.event(conn, "status.update", fakeRuntime, `{"text":"`+mark+`"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "status.update") && strings.Contains(string(f.Params), mark) })
}

// calls makes a companion tool's call as alice's companion server would, and
// returns its result when it comes.
func (h *harness) calls(tool, args string) <-chan map[string]any {
	result := make(chan map[string]any, 1)
	go func() { result <- h.api.companionCall(context.Background(), "alice", tool, json.RawMessage(args)) }()
	return result
}

func resultOf(t *testing.T, results <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(frameWait):
		t.Fatal("the call got no result")
		return nil
	}
}

func statusOf(t *testing.T, results <-chan map[string]any) string {
	t.Helper()
	status, _ := resultOf(t, results)["status"].(string)
	return status
}

type companionEvent struct {
	SessionID string `json:"session_id"`
	Payload   struct {
		CommandID string         `json:"command_id"`
		Action    string         `json:"action"`
		Args      map[string]any `json:"args"`
	} `json:"payload"`
}

// command waits for the next command at w.
func command(w *window) companionEvent {
	w.t.Helper()
	f := w.waitFor(func(f frame) bool { return isEvent(f, "anas.companion") })
	var ev companionEvent
	if err := json.Unmarshal(f.Params, &ev); err != nil {
		w.t.Fatal(err)
	}
	return ev
}

// answer sends w's companion.result for a command and returns the response.
func answer(w *window, session, commandID, outcome string) frame {
	w.t.Helper()
	return w.call("companion.result", `{"session_id":"`+session+`","command_id":"`+commandID+`",`+outcome+`}`)
}

func TestACompanionCommandGoesToTheWindowOfTheConversationOnly(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// Another window has the conversation open and follows its turn.
	watcher := h.open()
	watcher.call("session.resume", `{"session_id":"`+fakeStored+`","cols":80}`)

	// Hermes reports the call with the model's arguments; the companion server
	// passes them on as Python wrote them.
	h.started(conn, fakeRuntime, "call-1", "position", `{"x":1,"y":0.25,"mode":"line","duration":300}`)
	results := h.calls("position", `{"x":1.0,"y":0.25,"mode":"line","duration":300.0}`)
	sent := command(w)
	if sent.SessionID != fakeRuntime || sent.Payload.Action != "position" || !commandIDPattern.MatchString(sent.Payload.CommandID) ||
		sent.Payload.Args["mode"] != "line" || sent.Payload.Args["x"] != 1.0 || len(sent.Payload.Args) != 4 {
		t.Fatalf("command = %+v", sent)
	}
	// Only the window it went to answers it, once.
	if response := answer(watcher, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) == 0 {
		t.Fatal("another window answered the command")
	}
	if response := answer(w, otherRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) == 0 {
		t.Fatal("an answer for another conversation was taken")
	}
	if response := answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok","x":1,"y":0.25}`); len(response.Error) > 0 {
		t.Fatalf("answer refused: %s", response.Error)
	}
	if result := resultOf(t, results); result["status"] != "ok" || result["y"] != 0.25 {
		t.Fatalf("result = %v", result)
	}
	if response := answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) == 0 {
		t.Fatal("a command was answered twice")
	}
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	watcher.waitFor(func(f frame) bool {
		if isEvent(f, "anas.companion") {
			t.Fatal("a command reached another window")
		}
		return isEvent(f, "message.complete")
	})
}

func TestTheWindowsRefusalsBecomeTheToolsResult(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	for i, c := range []struct {
		outcome, status, reason string
	}{
		{`"ok":false,"error":{"reason":"locked","message":"占用中","expiresAt":1800000000000}`, "rejected", "locked"},
		{`"ok":false,"error":{"reason":"rate-limited","message":"太频繁"}`, "busy", "rate-limited"},
		{`"ok":false,"error":{"reason":"not-controlling","message":"不是这个窗口"}`, "no_desktop", ""},
		{`"ok":false,"error":{"reason":"<b>","message":"x"}`, "rejected", "not-ready"},
		{`"ok":true,"result":{"status":"ended","playbackId":"p1"}`, "ended", ""},
		{`"ok":true,"result":{"status":"rejected"}`, "rejected", "not-ready"},
	} {
		h.started(conn, fakeRuntime, fmt.Sprintf("call-%d", i), "anim", `{"name":"wave"}`)
		results := h.calls("anim", `{"name":"wave"}`)
		sent := command(w)
		if response := answer(w, fakeRuntime, sent.Payload.CommandID, c.outcome); len(response.Error) > 0 {
			t.Fatalf("answer %s refused: %s", c.outcome, response.Error)
		}
		result := resultOf(t, results)
		if result["status"] != c.status || (c.reason != "" && result["reason"] != c.reason) {
			t.Fatalf("%s gave %v", c.outcome, result)
		}
		if c.reason == "locked" && result["expiresAt"] != 1800000000000.0 {
			t.Fatalf("locked lost its expiresAt: %v", result)
		}
	}
}

func TestACallWithoutItsToolStartHasNoDesktop(t *testing.T) {
	h := newHarness(t)
	// A member with no window open at all.
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("no window: %s", status)
	}
	w, conn := companionTurn(t, h)
	// A scheduled task, a muted wake-up or a script in the sandbox: no tool.start.
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("no tool.start: %s", status)
	}
	// A subagent's mirror on its own session, the arguments of another call, and
	// a call started outside a turn do not pair either.
	h.started(conn, fakeRuntime, mirroredTool+"child:1", "wake", `{}`)
	h.started(conn, fakeRuntime, "call-1", "broadcast", `{"text":"你好"}`)
	h.started(conn, otherRuntime, "call-2", "wake", `{}`)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("mirror or no turn: %s", status)
	}
	if status := statusOf(t, h.calls("broadcast", `{"text":"再见"}`)); status != "no_desktop" {
		t.Fatalf("other arguments: %s", status)
	}
	// A call that completed is no longer waiting.
	h.serve.event(conn, "tool.complete", fakeRuntime, `{"tool_id":"call-1","name":"`+companionPrefix+`broadcast"}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("broadcast", `{"text":"你好"}`)); status != "no_desktop" {
		t.Fatalf("completed call: %s", status)
	}
}

func TestAToolStartThatComesAfterTheCallStillPairs(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	results := h.calls("wake", `{}`)
	time.Sleep(100 * time.Millisecond)
	// Hermes reports a call with no arguments without its args.
	h.serve.event(conn, "tool.start", fakeRuntime, `{"tool_id":"call-1","name":"`+companionPrefix+`wake"}`)
	sent := command(w)
	answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok","sleeping":false,"changed":true}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("status = %s", status)
	}
}

func TestTheSameCallWaitingInTwoConversationsIsBusy(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "message.start", otherRuntime, `{}`)
	h.started(conn, fakeRuntime, "call-1", "throw", `{"angle":90,"power":1}`)
	h.started(conn, otherRuntime, "call-2", "throw", `{"angle":90,"power":1}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("throw", `{"angle":90,"power":1}`)); status != "busy" {
		t.Fatalf("ambiguous: %s", status)
	}
	// Once the other conversation's call is done, the pairing is certain again.
	h.serve.event(conn, "tool.complete", otherRuntime, `{"tool_id":"call-2","name":"`+companionPrefix+`throw"}`)
	h.observed(w, conn)
	results := h.calls("throw", `{"angle":90,"power":1}`)
	sent := command(w)
	answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("status = %s", status)
	}
	// Two waiting calls in one conversation go first in, first out.
	h.started(conn, fakeRuntime, "call-3", "wake", `{}`)
	h.started(conn, fakeRuntime, "call-4", "wake", `{}`)
	for range 2 {
		results := h.calls("wake", `{}`)
		answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
		if status := statusOf(t, results); status != "ok" {
			t.Fatalf("status = %s", status)
		}
	}
}

func TestASubagentCallingTheCompanionMakesItsConversationBusy(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "subagent.tool", fakeRuntime, `{"subagent_id":"sa-1","tool_name":"`+companionPrefix+`wake"}`)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "busy" {
		t.Fatalf("with a subagent's call: %s", status)
	}
	h.serve.event(conn, "subagent.complete", fakeRuntime, `{"subagent_id":"sa-1"}`)
	h.observed(w, conn)
	results := h.calls("wake", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("after the subagent: %s", status)
	}
}

func TestOnlyTheWindowThatSentThePromptGetsCommands(t *testing.T) {
	h := newHarness(t)
	first := h.open()
	conn := h.serve.current()
	first.call("session.create", `{"cols":80}`)
	first.call("browser.controller.register", registration)
	second := h.open()
	second.call("session.resume", `{"session_id":"`+fakeStored+`","cols":80}`)
	h.serve.finish() // the resumed session reported a turn under way
	second.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
	// Between turns the second window became the browser, then the first sent
	// the prompt.
	if response := second.call("browser.controller.register", registration); len(response.Error) > 0 {
		t.Fatalf("registration refused: %s", response.Error)
	}
	first.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"long"}`)
	first.waitFor(func(f frame) bool { return isEvent(f, "message.start") })
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("browser is not the prompter: %s", status)
	}
}

func TestAnUnansweredCommandTimesOut(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "bubble_clear", `{}`)
	results := h.calls("bubble_clear", `{}`)
	sent := command(w)
	if status := statusOf(t, results); status != "timeout" {
		t.Fatalf("status = %s", status)
	}
	if response := answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) == 0 {
		t.Fatal("a late answer was taken")
	}
}

func TestTheTurnsEndCancelsItsCommandsAndReleasesItsLock(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "lock", `{"ttl":1800000}`)
	results := h.calls("lock", `{"ttl":1800000}`)
	sent := command(w)
	if sent.Payload.Args["ttl"] != float64(maxLockTTL) {
		t.Fatalf("ttl not capped: %v", sent.Payload.Args)
	}
	answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok","lockId":"l-1","expiresAt":1}`)
	if result := resultOf(t, results); result["lockId"] != "l-1" {
		t.Fatalf("result = %v", result)
	}
	h.started(conn, fakeRuntime, "call-2", "anim", `{"name":"wave","lock":"l-1"}`)
	results = h.calls("anim", `{"name":"wave","lock":"l-1"}`)
	command(w)
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	if result := resultOf(t, results); result["status"] != "cancelled" || result["reason"] != "turn-ended" {
		t.Fatalf("result = %v", result)
	}
	release := command(w)
	if release.Payload.Action != "lock_release" || release.Payload.Args["force"] != true || len(release.Payload.Args) != 1 {
		t.Fatalf("release = %+v", release)
	}
	if response := answer(w, fakeRuntime, release.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) > 0 {
		t.Fatalf("release answer refused: %s", response.Error)
	}
	// A turn that took no lock releases none.
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"hello"}`)
	w.waitFor(func(f frame) bool {
		if isEvent(f, "anas.companion") {
			t.Fatal("a turn without a lock released one")
		}
		return isEvent(f, "message.complete")
	})
}

func TestAWindowThatLeavesCancelsItsCommands(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "anim", `{"name":"wave"}`)
	results := h.calls("anim", `{"name":"wave"}`)
	command(w)
	_ = w.conn.Close(websocket.StatusNormalClosure, "")
	if result := resultOf(t, results); result["status"] != "cancelled" || result["reason"] != "window-closed" {
		t.Fatalf("result = %v", result)
	}
	// The turn goes on without a window: no desktop for its next call.
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("after the window left: %s", status)
	}
}

func TestTheLimitsMakeCommandsBusy(t *testing.T) {
	m := &member{}
	now := time.Now()
	for i := range 6 {
		if !m.admit("broadcast", now) || !m.admit("throw", now) {
			t.Fatalf("command %d refused", i)
		}
	}
	if m.admit("broadcast", now) || m.admit("throw", now) {
		t.Fatal("a seventh broadcast or throw in a minute passed")
	}
	for i := range 8 {
		if !m.admit("anim", now) {
			t.Fatalf("anim %d refused", i)
		}
	}
	if m.admit("wake", now) {
		t.Fatal("a 21st command in a minute passed")
	}
	if !m.admit("broadcast", now.Add(companionMinute)) {
		t.Fatal("the limit outlived its minute")
	}

	h := newHarness(t)
	_, conn := companionTurn(t, h)
	h.api.membersMu.Lock()
	alice := h.api.members["alice"]
	h.api.membersMu.Unlock()
	alice.mu.Lock()
	for range companionRate {
		alice.sent = append(alice.sent, sentCommand{at: time.Now(), tool: "anim"})
	}
	alice.mu.Unlock()
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	if result := resultOf(t, h.calls("wake", `{}`)); result["status"] != "busy" || result["reason"] != "rate-limited" {
		t.Fatalf("result = %v", result)
	}
}

func TestCompanionArgumentsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	for _, c := range []struct{ tool, args string }{
		{"anim", `{"name":"wave","url":"http://example.com/a.webm"}`},
		{"broadcast", `{"text":"看图","image":"data:image/png;base64,AA=="}`},
		{"broadcast", `{"text":"` + strings.Repeat("长", 201) + `"}`},
		{"broadcast", `{"text":"bell\u0007"}`},
		{"broadcast", `{"text":"\u202eevil"}`},
		{"broadcast", `{"text":"invoice\u200f.pdf.exe"}`},
		{"broadcast", `{"text":"a\u200eb"}`},
		{"broadcast", `{"text":"a\u061cb"}`},
		{"broadcast", `{"text":"a\u2066b\u2069"}`},
		{"broadcast", `{"text":"\u7b2c\u4e00\u6bb5\u2029\u7b2c\u4e8c\u6bb5"}`},
		{"broadcast", `{"text":"\u7b2c\u4e00\u884c\u2028\u7b2c\u4e8c\u884c"}`},
		{"lock_release", `{"lockId":""}`},
		{"broadcast", `{"text":"  "}`},
		{"broadcast", `{"text":"hi","duration":60001}`},
		{"anim", `{"facing":"left"}`},
		{"anim", `{"name":"wave","facing":"up"}`},
		{"anim", `{"name":"wave","pet":"other"}`},
		{"position", `{"x":0.5,"y":0.5,"mode":"teleport"}`},
		{"position", `{"x":0.5,"y":0.5,"mode":"line","duration":5001}`},
		{"throw", `{"angle":10,"power":0}`},
		{"throw", `{"angle":10,"power":3.5}`},
		{"lock", `{"ttl":999}`},
		{"lock_release", `{"lockId":"l-1","force":true}`},
		{"lock_release", `{}`},
		{"sleep", `{"lock":"l-1"}`},
		{"open_url", `{}`},
		{"wake", `[]`},
	} {
		h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), members: map[string]*member{}}
		result := h.companionCall(context.Background(), "alice", c.tool, json.RawMessage(c.args))
		if result["status"] != "rejected" || result["reason"] != "bad-request" || result["message"] == "" {
			t.Errorf("%s %s gave %v", c.tool, c.args, result)
		}
	}
	args, canonical, problem := checkCompanion("position", json.RawMessage(`{"y":-1,"x":1.5,"mode":"spring","lock":null}`))
	if problem != "" || args["x"] != 1.0 || args["y"] != 0.0 || len(args) != 3 || canonical != `{"mode":"spring","x":1.5,"y":-1}` {
		t.Fatalf("args = %v, canonical %s, problem %q", args, canonical, problem)
	}
	if args, _, problem := checkCompanion("broadcast", json.RawMessage(`{"text":"第一行\n第二行","anim":false}`)); problem != "" || args["text"] != "第一行\n第二行" {
		t.Fatalf("args = %v, problem %q", args, problem)
	}
	// An empty lock is no lock, as dsh-pet's empty ?lock= and the window take it;
	// numbers and booleans written as strings are taken as Hermes coerces them.
	args, canonical, problem = checkCompanion("broadcast", json.RawMessage(`{"text":"hi","lock":"","duration":"3000","anim":"FALSE"}`))
	if problem != "" || len(args) != 3 || args["duration"] != 3000.0 || args["anim"] != false || canonical != `{"anim":false,"duration":3000,"text":"hi"}` {
		t.Fatalf("args = %v, canonical %s, problem %q", args, canonical, problem)
	}
	if args, _, problem := checkCompanion("lock", json.RawMessage(`{"lockId":" ","ttl":"null"}`)); problem != "" || len(args) != 0 {
		t.Fatalf("args = %v, problem %q", args, problem)
	}
	if _, _, problem := checkCompanion("throw", json.RawMessage(`{"angle":"NaN","power":1}`)); problem == "" {
		t.Fatal("NaN passed as an angle")
	}
}

func TestACompanionResultIsCheckedLikeAnyRequest(t *testing.T) {
	h := newHarness(t)
	w, _ := companionTurn(t, h)
	for _, params := range []string{
		`{"session_id":"` + fakeRuntime + `","command_id":"not-an-id","ok":true,"result":{"status":"ok"}}`,
		`{"session_id":"` + fakeRuntime + `","command_id":"0123456789abcdef0123456789abcdef","ok":true}`,
		`{"session_id":"` + fakeRuntime + `","command_id":"0123456789abcdef0123456789abcdef","ok":true,"result":"ok"}`,
		`{"session_id":"` + fakeRuntime + `","command_id":"0123456789abcdef0123456789abcdef","ok":true,"result":{"status":"ok"},"extra":1}`,
		`{"session_id":"` + fakeRuntime + `","command_id":"0123456789abcdef0123456789abcdef","ok":false,"error":{"reason":"` + strings.Repeat("x", maxCompanionAnswer) + `"}}`,
		`{"session_id":"` + fakeRuntime + `","command_id":"0123456789abcdef0123456789abcdef","ok":true,"result":{"status":"ok"}}`,
	} {
		if response := w.call("companion.result", params); len(response.Error) == 0 {
			t.Errorf("companion.result %s was taken", params)
		}
	}
	if strings.Contains(strings.Join(h.serve.methodsSeen(), "\n"), "companion.result") {
		t.Fatal("companion.result reached Hermes")
	}
}

// uids stands in for the account store.
type uids map[int]string

func (u uids) ResolveAccountUID(_ context.Context, uid int) (accounts.SessionUser, error) {
	name, ok := u[uid]
	if !ok {
		return accounts.SessionUser{}, accounts.ErrUserNotFound
	}
	return accounts.SessionUser{UserID: "u-" + name, Username: name}, nil
}

func TestTheCompanionSocketKnowsTheMemberByTheirUID(t *testing.T) {
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	serve := func(resolver AccountResolver) *http.Client {
		h := New(Options{Accounts: resolver, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		_ = os.Remove(path)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		server := h.CompanionServer()
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })
		return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}}
	}
	post := func(client *http.Client, path, body string) (int, map[string]any) {
		t.Helper()
		response, err := client.Post("http://companion"+path, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var data map[string]any
		_ = json.NewDecoder(response.Body).Decode(&data)
		return response.StatusCode, data
	}
	uid := os.Getuid()
	if uid == 0 {
		t.Skip("root at the other end is a wiring fault, not a member")
	}
	client := serve(uids{uid: "alice"})
	if status, data := post(client, "/call", `{"tool":"broadcast","args":{"text":"hi","url":"http://example.com"}}`); status != http.StatusOK || data["reason"] != "bad-request" {
		t.Fatalf("checked call: %d %v", status, data)
	}
	if status, data := post(client, "/call", `{"tool":"wake","args":{}}`); status != http.StatusOK || data["status"] != "no_desktop" {
		t.Fatalf("no window: %d %v", status, data)
	}
	if status, _ := post(client, "/other", `{}`); status != http.StatusNotFound {
		t.Fatalf("another route: %d", status)
	}
	if status, _ := post(serve(uids{}), "/call", `{"tool":"wake","args":{}}`); status != http.StatusUnauthorized {
		t.Fatalf("not a member's UID: %d", status)
	}
	if status, _ := post(serve(uids{uid: "Not An Account!"}), "/call", `{"tool":"wake","args":{}}`); status != http.StatusForbidden {
		t.Fatalf("an account without an assistant: %d", status)
	}
	if status, _ := post(serve(nil), "/call", `{"tool":"wake","args":{}}`); status != http.StatusServiceUnavailable {
		t.Fatalf("no account store: %d", status)
	}
}

// alice is the member the harness's windows belong to.
func (h *harness) alice() *member {
	h.api.membersMu.Lock()
	defer h.api.membersMu.Unlock()
	return h.api.members["alice"]
}

func TestModelArgumentsPairWithWhatTheServerPassesOn(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// tool.start carries the model's own arguments; Hermes and the companion
	// server coerce them before the call reaches the socket.
	for i, c := range []struct{ start, call string }{
		{`{"x":"0.5","y":" 0.5 ","mode":"instant"}`, `{"x":0.5,"y":0.5,"mode":"instant"}`},
		{`{"text":"hi","duration":"3000","anim":"false","lock":"null"}`, `{"text":"hi","duration":3000.0,"anim":false}`},
		{`{"name":"wave","lock":""}`, `{"name":"wave","lock":""}`},
		{`{"name":"wave","lock":"  "}`, `{"name":"wave"}`},
	} {
		var tool string
		switch {
		case strings.Contains(c.start, `"mode"`):
			tool = "position"
		case strings.Contains(c.start, `"text"`):
			tool = "broadcast"
		default:
			tool = "anim"
		}
		h.started(conn, fakeRuntime, fmt.Sprintf("call-%d", i), tool, c.start)
		results := h.calls(tool, c.call)
		sent := command(w)
		if _, locked := sent.Payload.Args["lock"]; locked || sent.Payload.Action != tool {
			t.Fatalf("%s sent %+v", c.call, sent)
		}
		answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
		if status := statusOf(t, results); status != "ok" {
			t.Fatalf("%s paired with %s: %s", c.call, c.start, status)
		}
	}
}

func TestKeysTheServerDropsAreRefusedOnceThePairingIsMade(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// The companion server passes on only the keys its tool takes; tool.start
	// still has the others.
	h.started(conn, fakeRuntime, "call-1", "broadcast", `{"text":"看这里","image":"https://x/y.png"}`)
	h.started(conn, fakeRuntime, "call-2", "anim", `{"name":"wave","pet":"other"}`)
	h.observed(w, conn)
	result := resultOf(t, h.calls("broadcast", `{"text":"看这里"}`))
	if result["status"] != "rejected" || result["reason"] != "bad-request" || !strings.Contains(result["message"].(string), "url 或 image") {
		t.Fatalf("image: %v", result)
	}
	result = resultOf(t, h.calls("anim", `{"name":"wave"}`))
	if result["status"] != "rejected" || result["reason"] != "bad-request" || !strings.Contains(result["message"].(string), "pet") {
		t.Fatalf("pet: %v", result)
	}
	// Both starts were used up, and nothing went to the window.
	if status := statusOf(t, h.calls("anim", `{"name":"wave"}`)); status != "no_desktop" {
		t.Fatalf("start not used up: %s", status)
	}
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	w.waitFor(func(f frame) bool {
		if isEvent(f, "anas.companion") {
			t.Fatal("a refused call reached the window")
		}
		return isEvent(f, "message.complete")
	})
}

func TestAWakeUpTurnNoWindowPromptedHasNoDesktop(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	// Hermes's own turn after a background task: message.start, no prompt.
	h.serve.event(conn, "message.start", fakeRuntime, `{}`)
	h.started(conn, fakeRuntime, "call-1", "broadcast", `{"text":"后台读到的内容"}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("broadcast", `{"text":"后台读到的内容"}`)); status != "no_desktop" {
		t.Fatalf("wake-up turn: %s", status)
	}

	// A prompt Hermes queued behind a turn under way prompts the next one.
	m := &member{sessions: map[string]*liveSession{}, commands: map[string]*companionCommand{}}
	u := &upstream{}
	m.session(fakeRuntime).turn = true
	m.prompted(fakeRuntime, u, true)
	s := m.sessions[fakeRuntime]
	if s.prompter != nil {
		t.Fatal("a queued prompt prompted the turn under way")
	}
	m.companionTurnEnded(fakeRuntime, s, false)
	if s.prompter != u || s.nextPrompter != nil {
		t.Fatal("the queued prompt did not prompt the next turn")
	}
	m.companionTurnEnded(fakeRuntime, s, false)
	if s.prompter != nil {
		t.Fatal("the prompter outlived its turn")
	}
}

func TestAPromptQueuedAfterTheTurnEndedPromptsTheTurnHermesStartsForIt(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
	// The member answers at once, while Hermes still finishes the turn that
	// ended: it queues the prompt and starts its turn with no message.complete
	// in between (seen with the pinned Hermes, #412).
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"queued"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.start") })
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	results := h.calls("wake", `{}`)
	wake := command(w)
	if wake.Payload.Action != "wake" {
		t.Fatalf("command = %+v", wake)
	}
	if response := answer(w, fakeRuntime, wake.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`); len(response.Error) > 0 {
		t.Fatalf("answer refused: %s", response.Error)
	}
	if result := resultOf(t, results); result["status"] != "ok" {
		t.Fatalf("result = %v", result)
	}
}

func TestACommandSettledBeforeItWentOutIsNotSent(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	m := h.alice()
	// The member stops the turn just as a lock call is paired.
	c, refusal := m.command(fakeRuntime, "lock", map[string]any{"ttl": 1000.0})
	if refusal != nil {
		t.Fatalf("refused: %v", refusal)
	}
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	if release := command(w); release.Payload.Action != "lock_release" {
		t.Fatalf("first = %+v", release)
	}
	c.send(context.Background(), m)
	if result := <-c.answer; result["status"] != "cancelled" {
		t.Fatalf("result = %v", result)
	}
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"hello"}`)
	w.waitFor(func(f frame) bool {
		if isEvent(f, "anas.companion") {
			t.Fatal("a cancelled command reached the window after the release")
		}
		return isEvent(f, "message.complete")
	})
}

func TestAWindowThatDoesNotAnswerGetsNoMoreCommandsForAWhile(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	results := h.calls("wake", `{}`)
	first := command(w)
	if status := statusOf(t, results); status != "timeout" {
		t.Fatalf("first: %s", status)
	}
	// The calls Hermes queued behind it are answered at once.
	h.started(conn, fakeRuntime, "call-2", "sleep", `{}`)
	began := time.Now()
	if result := resultOf(t, h.calls("sleep", `{}`)); result["status"] != "timeout" || result["reason"] != "unresponsive" ||
		time.Since(began) >= companionReceipt {
		t.Fatalf("second: %v after %s", result, time.Since(began))
	}
	// Any answer shows the window is back.
	answer(w, fakeRuntime, first.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	h.started(conn, fakeRuntime, "call-3", "wake", `{}`)
	results = h.calls("wake", `{}`)
	sent := command(w)
	if sent.Payload.Action != "wake" {
		t.Fatalf("third = %+v", sent)
	}
	answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("third: %s", status)
	}
}

func TestASubagentAnywhereMakesTheMembersCallsBusy(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// A subagent of another conversation calls wake; this conversation waits
	// with its own wake.
	h.serve.event(conn, "message.start", otherRuntime, `{}`)
	h.serve.event(conn, "subagent.tool", otherRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1","tool_name":"`+companionPrefix+`wake"}`)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "busy" {
		t.Fatalf("with another conversation's subagent: %s", status)
	}
	// The subagent is known by its id and child session together.
	h.serve.event(conn, "subagent.complete", otherRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1"}`)
	h.observed(w, conn)
	results := h.calls("wake", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("after the subagent: %s", status)
	}
	// A subagent that never reports its end stops counting once its call can
	// no longer arrive.
	h.serve.event(conn, "subagent.tool", otherRuntime, `{"subagent_id":"sa-2","child_session_id":"c-2","tool_name":"`+companionPrefix+`wake"}`)
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	h.observed(w, conn)
	m := h.alice()
	m.mu.Lock()
	m.sessions[otherRuntime].helpers["sa-2/c-2"] = time.Now().Add(-companionHelper)
	m.mu.Unlock()
	results = h.calls("wake", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("after the subagent's time: %s", status)
	}
}

func TestAStartLeftOverFromAStoppedInstanceIsForgotten(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// A turn whose instance stopped before message.complete left a start.
	h.serve.event(conn, "message.start", otherRuntime, `{}`)
	h.started(conn, otherRuntime, "call-1", "bubble_clear", `{}`)
	h.started(conn, fakeRuntime, "call-2", "bubble_clear", `{}`)
	h.observed(w, conn)
	m := h.alice()
	m.mu.Lock()
	m.sessions[otherRuntime].companion[0].at = time.Now().Add(-companionStale)
	m.mu.Unlock()
	results := h.calls("bubble_clear", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("status = %s", status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if left := len(m.sessions[otherRuntime].companion); left != 0 {
		t.Fatalf("%d stale starts kept", left)
	}
}

func TestACallTheServerStopsWaitingForIsSettled(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "anim", `{"name":"wave"}`)
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan map[string]any, 1)
	go func() { results <- h.api.companionCall(ctx, "alice", "anim", json.RawMessage(`{"name":"wave"}`)) }()
	sent := command(w)
	cancel()
	if status := statusOf(t, results); status != "cancelled" {
		t.Fatalf("status = %s", status)
	}
	m := h.alice()
	m.mu.Lock()
	left := len(m.commands)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d commands left unsettled", left)
	}
	if response := answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ended"}`); len(response.Error) == 0 {
		t.Fatal("an answer to a settled command was taken")
	}
}

func TestTheBroadcastLimitHoldsOnTheWholePath(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	m := h.alice()
	m.mu.Lock()
	for range companionRateEach["broadcast"] {
		m.sent = append(m.sent, sentCommand{at: time.Now(), tool: "broadcast"})
	}
	m.mu.Unlock()
	h.started(conn, fakeRuntime, "call-1", "broadcast", `{"text":"再说一次"}`)
	if result := resultOf(t, h.calls("broadcast", `{"text":"再说一次"}`)); result["status"] != "busy" || result["reason"] != "rate-limited" {
		t.Fatalf("seventh broadcast: %v", result)
	}
	// Other tools still pass within the member's limit.
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	results := h.calls("wake", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("wake: %s", status)
	}
}

// windowOf returns the connection of alice's one window.
func (h *harness) windowOf() *upstream {
	m := h.alice()
	m.mu.Lock()
	defer m.mu.Unlock()
	for u := range m.upstreams {
		return u
	}
	return nil
}

// stop stops the turn under way, as the member's Stop does, and plays its
// end.
func (h *harness) stop(w *window, conn *websocket.Conn) {
	w.t.Helper()
	w.call("session.interrupt", `{"session_id":"`+fakeRuntime+`"}`)
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"stopped","status":"interrupted"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
}

// nextTurn starts a turn of the window's own prompt after the last one ended.
func nextTurn(w *window) {
	w.t.Helper()
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"long"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.start") })
}

func TestStoppingATurnDropsThePromptQueuedBehindIt(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// The window's prompt queued behind the turn under way, which the member
	// then stops: Hermes drops the queued prompt with it.
	h.alice().prompted(fakeRuntime, h.windowOf(), true)
	h.stop(w, conn)
	// Hermes's own turn follows, a wake-up after a background task.
	h.serve.event(conn, "message.start", fakeRuntime, `{}`)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("wake-up after a stop: %s", status)
	}
}

func TestATurnStopsWhenTheWindowAsksNotWhenHermesAnswers(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// The window's prompt queued behind the turn, a subagent's companion tool
	// and a call of the turn's own on its way when the member stops it.
	h.alice().prompted(fakeRuntime, h.windowOf(), true)
	h.serve.event(conn, "subagent.tool", fakeRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1","tool_name":"`+companionPrefix+`wake"}`)
	h.started(conn, fakeRuntime, "call-1", "broadcast", `{"text":"你好"}`)
	h.observed(w, conn)
	// Hermes's turn thread ends the turn before Hermes answers the request.
	h.serve.mu.Lock()
	h.serve.stopFirst = true
	h.serve.mu.Unlock()
	w.call("session.interrupt", `{"session_id":"`+fakeRuntime+`"}`)
	// Hermes's own turn follows, a wake-up after a background task: it has no
	// prompter, and the stopped turn's subagent makes nothing busy.
	h.serve.event(conn, "message.start", fakeRuntime, `{}`)
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	h.observed(w, conn)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "no_desktop" {
		t.Fatalf("wake-up after a stop: %s", status)
	}
	// The stopped turn's call, late, is still its own.
	if result := resultOf(t, h.calls("broadcast", `{"text":"你好"}`)); result["status"] != "cancelled" || result["reason"] != "turn-ended" {
		t.Fatalf("the stopped turn's call: %v", result)
	}
}

func TestAStopOutsideATurnStopsNothingLater(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	h.observed(w, conn)
	w.call("session.interrupt", `{"session_id":"`+fakeRuntime+`"}`)
	// Hermes starts a turn of its own, which no request of a window opens.
	h.serve.event(conn, "message.start", fakeRuntime, `{}`)
	h.observed(w, conn)
	m := h.alice()
	m.mu.Lock()
	stopping := m.sessions[fakeRuntime].stopping
	m.mu.Unlock()
	if stopping {
		t.Fatal("a stop with no turn under way stopped the next one")
	}
}

func TestAnInstanceThatStopsInATurnEndsIt(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.serve.event(conn, "subagent.tool", fakeRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1","tool_name":"`+companionPrefix+`wake"}`)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	// The instance stops: its connection closes, and the window's with it.
	_ = conn.Close(websocket.StatusGoingAway, "")
	m := h.alice()
	deadline := time.Now().Add(frameWait)
	for {
		m.mu.Lock()
		s := m.sessions[fakeRuntime]
		ended := !s.turn && len(s.companion) == 0 && len(s.helpers) == 0 && len(s.subagents) == 0
		m.mu.Unlock()
		if ended {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn outlived its instance")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The member's next instance gives the conversation another runtime id:
	// the same call there pairs with it alone, at once.
	w = h.open()
	conn = h.serve.current()
	h.serve.event(conn, "message.start", otherRuntime, `{}`)
	h.started(conn, otherRuntime, "call-2", "wake", `{}`)
	h.observed(w, conn)
	m.mu.Lock()
	start, session, found, ambiguous := m.pair("wake", "{}", time.Now(), false)
	m.mu.Unlock()
	if !found || ambiguous || session != otherRuntime || start.toolID != "call-2" || m.delegating(time.Now()) {
		t.Fatalf("pairing after the restart: %+v in %s, found %v, ambiguous %v", start, session, found, ambiguous)
	}
}

func TestASubagentsCallThatComesBeforeItsEventIsNotTheConversations(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// A subagent runs; its wake reaches the socket before its subagent.tool,
	// while the conversation's own wake waits.
	h.serve.event(conn, "subagent.start", fakeRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1"}`)
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	results := h.calls("wake", `{}`)
	time.Sleep(companionLate / 4)
	h.serve.event(conn, "subagent.tool", fakeRuntime, `{"subagent_id":"sa-1","child_session_id":"c-1","tool_name":"`+companionPrefix+`wake"}`)
	if status := statusOf(t, results); status != "busy" {
		t.Fatalf("the subagent's call: %s", status)
	}
	h.serve.event(conn, "tool.complete", fakeRuntime, `{"tool_id":"call-1","name":"`+companionPrefix+`wake"}`)
	// Stopping the turn stops its subagents, which need not report it: the
	// next turn's calls pair at once.
	h.stop(w, conn)
	nextTurn(w)
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	h.observed(w, conn)
	began := time.Now()
	results = h.calls("wake", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
	if status := statusOf(t, results); status != "ok" || time.Since(began) >= companionLate {
		t.Fatalf("after the stop: %s after %s", status, time.Since(began))
	}
}

func TestACallOfAStoppedTurnDoesNotTakeTheNextTurns(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	m := h.alice()
	// The member stops the turn with a call on its way, which Hermes completes
	// at once; or ends it without completing the call.
	for round, completed := range []bool{true, false} {
		h.started(conn, fakeRuntime, fmt.Sprintf("call-%d", round), "broadcast", `{"text":"你好"}`)
		h.observed(w, conn)
		w.call("session.interrupt", `{"session_id":"`+fakeRuntime+`"}`)
		if completed {
			h.serve.event(conn, "tool.complete", fakeRuntime, fmt.Sprintf(`{"tool_id":"call-%d","name":"%sbroadcast"}`, round, companionPrefix))
		}
		h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"stopped","status":"interrupted"}`)
		w.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
		nextTurn(w)
		h.started(conn, fakeRuntime, fmt.Sprintf("call-%d-next", round), "broadcast", `{"text":"你好"}`)
		h.observed(w, conn)
		// The stopped turn's call arrives first, then the new turn's own.
		if result := resultOf(t, h.calls("broadcast", `{"text":"你好"}`)); result["status"] != "cancelled" || result["reason"] != "turn-ended" {
			t.Fatalf("round %d, the stopped turn's call: %v", round, result)
		}
		results := h.calls("broadcast", `{"text":"你好"}`)
		answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok"}`)
		if status := statusOf(t, results); status != "ok" {
			t.Fatalf("round %d, the new turn's call: %s", round, status)
		}
		h.stop(w, conn)
		nextTurn(w)
	}
	// A call that never came is let go of.
	m.mu.Lock()
	m.sessions[fakeRuntime].abandon([]companionStart{{toolID: "call-x", tool: "wake", args: "{}"}}, time.Now().Add(-companionAbandoned))
	_, _, found, _ := m.pair("wake", "{}", time.Now(), true)
	m.mu.Unlock()
	if found {
		t.Fatal("an abandoned call outlived its time")
	}
}

func TestAStoppedTurnsCallDoesNotTakeAnotherConversations(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// The member stops a turn whose wake started, and which Hermes may never
	// have sent: it waited behind another call for the server.
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	h.observed(w, conn)
	h.stop(w, conn)
	// Another conversation's turn calls wake too, its call before its
	// tool.start: whose call it is cannot be told.
	results := h.calls("wake", `{}`)
	time.Sleep(companionLate / 4)
	h.serve.event(conn, "message.start", otherRuntime, `{}`)
	h.started(conn, otherRuntime, "call-2", "wake", `{}`)
	if status := statusOf(t, results); status != "busy" {
		t.Fatalf("a call before its start: %s", status)
	}
	h.observed(w, conn)
	if status := statusOf(t, h.calls("wake", `{}`)); status != "busy" {
		t.Fatalf("a call after its start: %s", status)
	}
	// Nothing was taken: the stopped turn's call and the other conversation's
	// are both still there.
	m := h.alice()
	m.mu.Lock()
	left := len(m.sessions[fakeRuntime].abandoned) + len(m.sessions[otherRuntime].companion)
	m.mu.Unlock()
	if left != 2 {
		t.Fatalf("%d calls left, want 2", left)
	}
}

// holdWindow takes the window's place for a companion command on its way, as
// a command whose frame the window does not take would, until the test ends
// or the returned function gives it back.
func (h *harness) holdWindow() func() {
	slot := h.windowOf().companionSlot()
	slot <- struct{}{}
	var once sync.Once
	give := func() { once.Do(func() { <-slot }) }
	h.t.Cleanup(give)
	return give
}

func TestAWindowThatTakesNoFramesHoldsUpNothingElse(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.started(conn, fakeRuntime, "call-1", "lock", `{}`)
	results := h.calls("lock", `{}`)
	answer(w, fakeRuntime, command(w).Payload.CommandID, `"ok":true,"result":{"status":"ok","lockId":"l-1","expiresAt":1}`)
	resultOf(t, results)
	// A command is stuck on its way to the window. A call that waits behind it
	// ends when its caller stops waiting, settled.
	give := h.holdWindow()
	h.started(conn, fakeRuntime, "call-2", "wake", `{}`)
	ctx, cancel := context.WithTimeout(context.Background(), companionReceipt/4)
	defer cancel()
	began := time.Now()
	if result := h.api.companionCall(ctx, "alice", "wake", json.RawMessage(`{}`)); result["status"] != "cancelled" || time.Since(began) >= companionReceipt {
		t.Fatalf("result %v after %s", result, time.Since(began))
	}
	// The turn's end, and the events after it, still go through while the
	// release of its lock waits.
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	h.observed(w, conn)
	m := h.alice()
	m.mu.Lock()
	left := len(m.commands)
	m.mu.Unlock()
	if left != 1 {
		t.Fatalf("%d commands waiting, want the release alone", left)
	}
	give()
	if release := command(w); release.Payload.Action != "lock_release" {
		t.Fatalf("release = %+v", release)
	}
}

func TestAMembersCallsUnderWayAreBounded(t *testing.T) {
	h := &Handler{}
	for i := range maxCompanionWaiting {
		if !h.companionCallBegins(1000) {
			t.Fatalf("call %d refused", i)
		}
	}
	if h.companionCallBegins(1000) {
		t.Fatal("a call over the bound began")
	}
	if !h.companionCallBegins(1001) {
		t.Fatal("another member's call was refused")
	}
	h.companionCallEnds(1000)
	if !h.companionCallBegins(1000) {
		t.Fatal("a call was refused after one ended")
	}

	// On the socket: the member's calls over the bound are busy at once.
	uid := os.Getuid()
	if uid == 0 {
		t.Skip("root at the other end is a wiring fault, not a member")
	}
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	api := New(Options{Accounts: uids{uid: "alice"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := api.CompanionServer()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	post := func() map[string]any {
		t.Helper()
		response, err := client.Post("http://companion/call", "application/json", strings.NewReader(`{"tool":"wake","args":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var data map[string]any
		_ = json.NewDecoder(response.Body).Decode(&data)
		return data
	}
	for range maxCompanionWaiting {
		api.companionCallBegins(uid)
	}
	if data := post(); data["status"] != "busy" || data["reason"] != "rate-limited" {
		t.Fatalf("over the bound: %v", data)
	}
	api.companionCallEnds(uid)
	if data := post(); data["status"] != "no_desktop" {
		t.Fatalf("within the bound: %v", data)
	}
}
