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
