package assistantapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func init() {
	// A window that does not answer a step is given up on sooner.
	desktopReceipt = 2 * time.Second
}

// step makes a desktop tool's call as alice's desktop server would, and
// returns its result when it comes.
func (h *harness) step(ctx context.Context, tool, args string) <-chan map[string]any {
	result := make(chan map[string]any, 1)
	go func() { result <- h.api.companionCall(ctx, "alice", desktopFamily, tool, json.RawMessage(args)) }()
	return result
}

// stepStarted plays the start of a desktop tool's call.
func (h *harness) stepStarted(conn *websocket.Conn, toolID, tool, args string) {
	h.serve.event(conn, "tool.start", fakeRuntime, `{"tool_id":"`+toolID+`","name":"`+desktopPrefix+tool+`","args":`+args+`}`)
}

// desktopCommand waits for the next desktop command at w.
func desktopCommand(w *window) companionEvent {
	w.t.Helper()
	f := w.waitFor(func(f frame) bool { return isEvent(f, "anas.desktop") })
	var ev companionEvent
	if err := json.Unmarshal(f.Params, &ev); err != nil {
		w.t.Fatal(err)
	}
	return ev
}

// stepAnswered sends w's desktop.result for a command and returns the response.
func stepAnswered(w *window, commandID, outcome string) frame {
	w.t.Helper()
	return w.call("desktop.result", `{"session_id":"`+fakeRuntime+`","command_id":"`+commandID+`",`+outcome+`}`)
}

// taken is what the step returned, as the window gave it.
func taken(t *testing.T, result map[string]any) string {
	t.Helper()
	raw, ok := result["result"].(json.RawMessage)
	if !ok || result["status"] != "ok" {
		t.Fatalf("result = %v", result)
	}
	return string(raw)
}

func TestADesktopStepGoesToTheBrowserOfTheConversationOnly(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	// Another window has the conversation open and follows its turn, but
	// cannot become its browser while the turn runs.
	watcher := h.open()
	watcher.call("session.resume", `{"session_id":"`+fakeStored+`","cols":80}`)
	if response := watcher.call("desktop.attach", registration); len(response.Error) == 0 {
		t.Fatal("another window became the browser of a turn under way")
	}

	h.stepStarted(conn, "call-1", "click", `{"ref":"e3"}`)
	results := h.step(context.Background(), "click", `{"ref":"e3"}`)
	ev := desktopCommand(w)
	if ev.SessionID != fakeRuntime || ev.Payload.Action != "click" || ev.Payload.Args["ref"] != "e3" {
		t.Fatalf("command = %+v", ev)
	}
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":true,"result":{"clicked":"相册"}`); len(response.Error) > 0 {
		t.Fatalf("result refused: %s", response.Error)
	}
	if got := taken(t, resultOf(t, results)); !strings.Contains(got, "相册") {
		t.Fatalf("the step returned %s", got)
	}
	// An answer counts once.
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":true,"result":{}`); len(response.Error) == 0 {
		t.Fatal("a second answer to the same command passed")
	}
	h.nothingSent(watcher, conn)

	// Between turns the window the member sends from becomes the browser.
	h.serve.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
	if response := watcher.call("desktop.attach", registration); len(response.Error) > 0 {
		t.Fatalf("attaching between turns refused: %s", response.Error)
	}
}

func TestAWindowAttachesOnlyToAConversationItOpened(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	if response := w.call("desktop.attach", registration); len(response.Error) == 0 {
		t.Fatal("a window attached to a conversation it has not opened")
	}
	w.call("session.create", `{"cols":80}`)
	for _, refused := range []string{
		`{"session_id":"` + otherRuntime + `"}`,
		`{"session_id":"` + fakeRuntime + `","capabilities":["navigate"]}`,
		`{}`,
	} {
		if response := w.call("desktop.attach", refused); len(response.Error) == 0 {
			t.Fatalf("attaching with %s passed", refused)
		}
	}
	if response := w.call("desktop.attach", registration); len(response.Error) > 0 {
		t.Fatalf("attaching refused: %s", response.Error)
	}
	// None of it is Hermes's to hear of.
	if seen := strings.Join(h.serve.methodsSeen(), "\n"); strings.Contains(seen, "desktop.") {
		t.Fatalf("Hermes was sent %s", seen)
	}
}

func TestADesktopStepAnswersWithAPictureOfThePage(t *testing.T) {
	// Megabytes through a WebSocket take longer under the race detector than the
	// two seconds the other tests give a window; no step here is left unanswered.
	defer func(receipt time.Duration) { desktopReceipt = receipt }(desktopReceipt)
	desktopReceipt = time.Minute
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.stepStarted(conn, "call-1", "look", `{"annotate":true}`)
	results := h.step(context.Background(), "look", `{"annotate":true}`)
	ev := desktopCommand(w)
	if ev.Payload.Action != "look" || ev.Payload.Args["annotate"] != true {
		t.Fatalf("command = %+v", ev)
	}
	// The page as a PNG in base64 is far larger than any message.
	image := strings.Repeat("A", 4<<20)
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":true,"result":{"mime_type":"image/png","data":"`+image+`"}`); len(response.Error) > 0 {
		t.Fatalf("picture refused: %s", response.Error)
	}
	if got := taken(t, resultOf(t, results)); len(got) < len(image) {
		t.Fatalf("the step returned %d bytes, not the picture", len(got))
	}
	// Anything larger than a picture is refused, and the command still waits.
	h.stepStarted(conn, "call-2", "look", `{}`)
	results = h.step(context.Background(), "look", `{}`)
	ev = desktopCommand(w)
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":true,"result":{"data":"`+strings.Repeat("A", maxDesktopResult)+`"}`); len(response.Error) == 0 {
		t.Fatal("a result over the limit passed")
	}
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":true,"result":{"data":""}`); len(response.Error) > 0 {
		t.Fatalf("result refused: %s", response.Error)
	}
	taken(t, resultOf(t, results))
}

func TestAStepTheWindowDidNotTakeSaysWhy(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	h.stepStarted(conn, "call-1", "press", `{"key":"Enter"}`)
	results := h.step(context.Background(), "press", `{"key":"Enter"}`)
	ev := desktopCommand(w)
	if response := stepAnswered(w, ev.Payload.CommandID, `"ok":false,"error":{"message":"成员没有允许这一步，NAS 上什么都没有改"}`); len(response.Error) > 0 {
		t.Fatalf("result refused: %s", response.Error)
	}
	result := resultOf(t, results)
	if result["status"] != desktopFailed || result["message"] != "成员没有允许这一步，NAS 上什么都没有改" {
		t.Fatalf("result = %v", result)
	}
	// A window that gives no reason still fails the step, in a few words.
	h.stepStarted(conn, "call-2", "press", `{"key":"Enter"}`)
	results = h.step(context.Background(), "press", `{"key":"Enter"}`)
	ev = desktopCommand(w)
	stepAnswered(w, ev.Payload.CommandID, `"ok":false,"error":{"message":"`+strings.Repeat("长", maxDesktopFailure+50)+`"}`)
	if result := resultOf(t, results); result["status"] != desktopFailed || len([]rune(result["message"].(string))) != maxDesktopFailure {
		t.Fatalf("result = %v", result["status"])
	}
}

func TestTheWindowIsToldWhenItsStepIsNoLongerWaitedFor(t *testing.T) {
	h := newHarness(t)
	w, conn := companionTurn(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.stepStarted(conn, "call-1", "click", `{"ref":"e1"}`)
	results := h.step(ctx, "click", `{"ref":"e1"}`)
	ev := desktopCommand(w)
	// The desktop server gave up on the call, as when Hermes stopped the turn:
	// the card the window may be asking the member with is over.
	cancel()
	told := w.waitFor(func(f frame) bool { return isEvent(f, "anas.desktop.cancel") })
	if !strings.Contains(string(told.Params), ev.Payload.CommandID) || !strings.Contains(string(told.Params), fakeRuntime) {
		t.Fatalf("cancel = %s", told.Params)
	}
	if status := statusOf(t, results); status != companionCancelled {
		t.Fatalf("status = %s", status)
	}
	// A window that leaves a step unanswered is told too, once the step timed out.
	h.stepStarted(conn, "call-2", "click", `{"ref":"e1"}`)
	results = h.step(context.Background(), "click", `{"ref":"e1"}`)
	ev = desktopCommand(w)
	told = w.waitFor(func(f frame) bool { return isEvent(f, "anas.desktop.cancel") })
	if !strings.Contains(string(told.Params), ev.Payload.CommandID) {
		t.Fatalf("cancel = %s", told.Params)
	}
	if status := statusOf(t, results); status != companionTimeout {
		t.Fatalf("status = %s", status)
	}
}

func TestWithoutAWindowThereIsNoDesktop(t *testing.T) {
	h := newHarness(t)
	// No window of alice's at all, as when a scheduled task calls.
	if result := h.api.companionCall(context.Background(), "alice", desktopFamily, "snapshot", json.RawMessage(`{}`)); result["status"] != companionNoDesktop {
		t.Fatalf("result = %v", result)
	}
	// A window that shows a conversation but never attached is no desktop.
	w := h.open()
	conn := h.serve.current()
	w.call("session.create", `{"cols":80}`)
	nextTurn(w)
	h.stepStarted(conn, "call-1", "snapshot", `{}`)
	if status := statusOf(t, h.step(context.Background(), "snapshot", `{}`)); status != companionNoDesktop {
		t.Fatalf("status = %s", status)
	}
	h.nothingSent(w, conn)
}

func TestDesktopStepsAreCheckedAndCountedApartFromTheCompanions(t *testing.T) {
	for _, c := range []struct {
		tool, args, problem string
	}{
		{"click", `{}`, "缺少参数 ref"},
		{"click", `{"ref":"e1","url":"https://example.com"}`, "click 不接受参数 url"},
		{"click", `{"ref":""}`, "ref："},
		{"type", `{"ref":"e1"}`, "缺少参数 text"},
		{"scroll", `{"direction":"left"}`, "direction："},
		{"press", `{"key":"` + strings.Repeat("a", maxDesktopName+1) + `"}`, "key："},
		{"navigate", `{"url":"https://example.com"}`, "没有这个工具"},
		{"anim", `{"name":"wave"}`, "没有这个工具"},
	} {
		if _, _, problem := checkCompanion(desktopFamily, c.tool, json.RawMessage(c.args)); !strings.Contains(problem, c.problem) {
			t.Errorf("%s %s: problem = %q, want %q", c.tool, c.args, problem, c.problem)
		}
	}
	// Clearing a control is typing nothing into it, and a flag the model wrote
	// as a string is the flag.
	if args, _, problem := checkCompanion(desktopFamily, "type", json.RawMessage(`{"ref":"e1","text":""}`)); problem != "" || args["text"] != "" {
		t.Fatalf("typing nothing: %v %q", args, problem)
	}
	if args, _, problem := checkCompanion(desktopFamily, "snapshot", json.RawMessage(`{"full":"true"}`)); problem != "" || args["full"] != true {
		t.Fatalf("a full snapshot: %v %q", args, problem)
	}
	if family, tool, known := windowTool(desktopPrefix + "look"); family != desktopFamily || tool != "look" || !known {
		t.Fatalf("look belongs to %v %q %v", family, tool, known)
	}
	if family, _, known := windowTool("browser_click"); family != nil || known {
		t.Fatal("a tool of neither server counts as a window tool")
	}

	// The companion's limit is not the desktop's: a task on the desktop takes
	// many more steps a minute than the companion gets commands.
	m := &member{}
	now := time.Now()
	for range companionRate {
		if !m.admit(companionFamily, "anim", now) {
			t.Fatal("a companion command within its limit was refused")
		}
	}
	if m.admit(companionFamily, "anim", now) {
		t.Fatal("a companion command over its limit passed")
	}
	for range desktopRate {
		if !m.admit(desktopFamily, "click", now) {
			t.Fatal("a step within the desktop's limit was refused")
		}
	}
	if m.admit(desktopFamily, "snapshot", now) {
		t.Fatal("a step over the desktop's limit passed")
	}
	if !m.admit(desktopFamily, "snapshot", now.Add(companionMinute)) {
		t.Fatal("a step a minute later was refused")
	}
}
