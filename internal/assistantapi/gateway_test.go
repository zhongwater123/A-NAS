package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

var sessionTokenOf = accounts.SessionToken

func init() {
	// A connection kept for a turn notices quickly that the turn ended, and
	// waits for its review a short while.
	lingerCheck = 20 * time.Millisecond
	reviewWait = 3 * time.Second
}

type harness struct {
	t       *testing.T
	serve   *fakeServe
	changes *fakeChanges
	server  *httptest.Server
}

// newHarness serves the assistant API as the Product Service would for
// alice, signed in with the session "alice-session".
func newHarness(t *testing.T) *harness {
	t.Helper()
	serve := newFakeServe(t)
	changes := &fakeChanges{}
	api := New(Options{
		Connector: NewSocketConnector(serve.dir, serve.dir),
		SocketDir: serve.dir, Changes: changes,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithUser(accounts.WithSessionToken(r.Context(), "alice-session"), accounts.User{ID: "u-alice", Username: "alice"})
		api.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	return &harness{t: t, serve: serve, changes: changes, server: server}
}

type window struct {
	t    *testing.T
	conn *websocket.Conn
	next int
}

func (h *harness) open() *window {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+PathPrefix+"/ws", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.CloseNow() })
	w := &window{t: h.t, conn: conn}
	w.waitFor(func(f frame) bool { return isEvent(f, "gateway.ready") })
	return w
}

func (w *window) send(method, params string) string {
	w.t.Helper()
	w.next++
	id := "r" + string(rune('0'+w.next))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":"`+id+`","method":"`+method+`","params":`+params+`}`)); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// call sends a request and returns its response frame.
func (w *window) call(method, params string) frame {
	w.t.Helper()
	id := w.send(method, params)
	return w.waitFor(func(f frame) bool { return f.isResponse() && unquote(f.ID) == id })
}

func (w *window) waitFor(match func(frame) bool) frame {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := w.conn.Read(ctx)
		if err != nil {
			w.t.Fatalf("waiting for a frame: %v", err)
		}
		var f frame
		if err := json.Unmarshal(data, &f); err != nil {
			w.t.Fatal(err)
		}
		if f.isServerRequest() {
			w.t.Fatalf("a request from the server reached the window: %s", data)
		}
		if f.isEvent() {
			var ev event
			_ = json.Unmarshal(f.Params, &ev)
			if ev.Type == "pet.mood" {
				w.t.Fatalf("an event outside the allowlist reached the window: %s", data)
			}
		}
		if match(f) {
			return f
		}
	}
}

func isEvent(f frame, kind string) bool {
	var ev event
	return f.isEvent() && json.Unmarshal(f.Params, &ev) == nil && ev.Type == kind
}

func cardOf(f frame) card {
	var ev struct {
		Payload card `json:"payload"`
	}
	_ = json.Unmarshal(f.Params, &ev)
	return ev.Payload
}

func cardIs(status string) func(frame) bool {
	return func(f frame) bool { return isEvent(f, "anas.approval") && cardOf(f).Status == status }
}

func (h *harness) post(path string) *http.Response {
	h.t.Helper()
	response, err := http.Post(h.server.URL+PathPrefix+path, "application/json", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func (h *harness) postJSON(path, body string) *http.Response {
	h.t.Helper()
	response, err := http.Post(h.server.URL+PathPrefix+path, "application/json", strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestGatewayPassesOnlyTheAllowedSurface(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	for method, params := range map[string]string{
		"shell.exec":       `{"command":"id"}`,
		"config.set":       `{"key":"yolo","value":true}`,
		"approval.respond": `{"session_id":"` + fakeRuntime + `","request_id":"req-1","choice":"once"}`,
		"profiles.create":  `{"name":"evil"}`,
	} {
		if reply := w.call(method, params); !strings.Contains(string(reply.Error), "-32601") {
			t.Fatalf("%s answered %s, want method not found", method, reply.Error)
		}
	}
	for _, params := range []string{`{"cols":80,"profile":"evil"}`, `{"source":"desktop"}`, `{"cwd":"/"}`} {
		if reply := w.call("session.create", params); !strings.Contains(string(reply.Error), "-32602") {
			t.Fatalf("session.create %s answered %s, want invalid params", params, reply.Error)
		}
	}
	if reply := w.call("session.resume", `{"session_id":"计划"}`); !strings.Contains(string(reply.Error), "-32602") {
		t.Fatalf("resume by title answered %s", reply.Error)
	}
	created := w.call("session.create", `{"cols":80}`)
	if len(created.Error) > 0 || strings.Contains(string(created.Result), "system_prompt") || strings.Contains(string(created.Result), "mcp_servers") {
		t.Fatalf("session.create = %s %s", created.Result, created.Error)
	}
	if !strings.Contains(string(created.Result), fakeStored) {
		t.Fatalf("session.create lost the stored id: %s", created.Result)
	}
	for _, got := range h.serve.received() {
		name, _, _ := strings.Cut(got, " ")
		if name != "client.capabilities" && name != "session.create" {
			t.Fatalf("%s reached Hermes", got)
		}
		if strings.Contains(got, "profile") || strings.Contains(got, "source") {
			t.Fatalf("a refused parameter reached Hermes: %s", got)
		}
	}
	if got := h.serve.received()[0]; got != `client.capabilities {"server_requests":true}` {
		t.Fatalf("first call to Hermes = %s, want the capabilities", got)
	}
}

func TestApprovalBecomesACardOnlyTheMemberCanConfirm(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"write"}`)
	awaiting := cardOf(w.waitFor(cardIs(cardAwaiting)))
	if awaiting.RequestID != "req-1" || awaiting.SessionID != fakeRuntime || awaiting.StoredSessionID != fakeStored {
		t.Fatalf("card = %+v", awaiting)
	}
	calls, _ := h.changes.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0], `"summary":"加一项"`) {
		t.Fatalf("executor calls = %v, want the prepared call's arguments", calls)
	}
	// The window cannot answer Hermes's request itself.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = w.conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":"srq-0123456789ab","result":{"choice":"once"}}`))
	select {
	case choice := <-h.serve.answered:
		t.Fatalf("Hermes was answered %q without the member's confirmation", choice)
	case <-time.After(200 * time.Millisecond):
	}
	if response := h.post("/approvals/req-1/confirm"); response.StatusCode != http.StatusOK {
		t.Fatalf("confirm = %d", response.StatusCode)
	}
	applied := cardOf(w.waitFor(cardIs("applied")))
	if applied.RequestID != "req-1" {
		t.Fatalf("applied card = %+v", applied)
	}
	calls, sessions := h.changes.recorded()
	want := []string{"approve c0ffee01", "get c0ffee01"}
	if len(calls) != 3 || calls[1] != want[0] || calls[2] != want[1] {
		t.Fatalf("executor calls = %v", calls)
	}
	for _, session := range sessions {
		if session != "alice-session" {
			t.Fatalf("the executor was called with session %q, want the member's", session)
		}
	}
	if h.serve.firstAnswer() != "once" {
		t.Fatalf("Hermes heard %v", h.serve.answers)
	}
	if response := h.post("/approvals/req-1/confirm"); response.StatusCode != http.StatusConflict {
		t.Fatalf("second confirm = %d, want 409", response.StatusCode)
	}
}

func TestRejectingAnswersDeny(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"write"}`)
	w.waitFor(cardIs(cardAwaiting))
	if response := h.post("/approvals/req-1/reject"); response.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d", response.StatusCode)
	}
	w.waitFor(cardIs(cardRejected))
	if h.serve.firstAnswer() != "deny" {
		t.Fatalf("Hermes heard %v", h.serve.answers)
	}
}

func TestConfirmingAfterHermesStoppedWaitingExpiresTheGrant(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"write"}`)
	w.waitFor(cardIs(cardAwaiting))
	h.serve.mu.Lock()
	h.serve.resolved = false
	h.serve.mu.Unlock()
	if response := h.post("/approvals/req-1/confirm"); response.StatusCode != http.StatusConflict {
		t.Fatalf("late confirm = %d, want 409", response.StatusCode)
	}
	calls, _ := h.changes.recorded()
	if calls[len(calls)-1] != "expire c0ffee01" {
		t.Fatalf("executor calls = %v, want the grant expired", calls)
	}
}

func TestApprovalAfterTheTurnIsDeniedUnseen(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"review"}`)
	select {
	case choice := <-h.serve.answered:
		if choice != "deny" {
			t.Fatalf("the review's approval was answered %q", choice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the review's approval was not answered")
	}
	if calls, _ := h.changes.recorded(); len(calls) != 0 {
		t.Fatalf("executor calls = %v, want none", calls)
	}
}

func TestADangerousCommandBecomesACardShowingIt(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"rm"}`)
	awaiting := cardOf(w.waitFor(cardIs(cardAwaiting)))
	if awaiting.Kind != kindCommand || awaiting.Command == nil || awaiting.Command.Text != "rm -rf /nas/助手/旧草稿" ||
		!awaiting.Command.ForSession || awaiting.Command.Script || awaiting.StoredSessionID != fakeStored {
		t.Fatalf("card = %+v %+v", awaiting, awaiting.Command)
	}
	if calls, _ := h.changes.recorded(); len(calls) != 0 {
		t.Fatalf("executor calls = %v, want none for a command", calls)
	}
	if response := h.postJSON("/approvals/req-3/confirm", `{"forSession":true}`); response.StatusCode != http.StatusOK {
		t.Fatalf("confirm = %d", response.StatusCode)
	}
	w.waitFor(cardIs(cardApproved))
	if h.serve.firstAnswer() != "session" {
		t.Fatalf("Hermes heard %v, want the command allowed for the conversation", h.serve.answers)
	}
}

func TestRejectingAScriptAnswersDeny(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"script"}`)
	awaiting := cardOf(w.waitFor(cardIs(cardAwaiting)))
	if awaiting.Kind != kindCommand || awaiting.Command == nil || !awaiting.Command.Script {
		t.Fatalf("card = %+v", awaiting)
	}
	if response := h.post("/approvals/req-3/reject"); response.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d", response.StatusCode)
	}
	w.waitFor(cardIs(cardRejected))
	if h.serve.firstAnswer() != "deny" {
		t.Fatalf("Hermes heard %v", h.serve.answers)
	}
}

func TestQuestionsAreAnsweredAsTheReplyToHermes(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.send("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"ask"}`)
	awaiting := cardOf(w.waitFor(cardIs(cardAwaiting)))
	if awaiting.Kind != kindQuestion || len(awaiting.Questions) != 2 || awaiting.Questions[0].ID != "q1" ||
		len(awaiting.Questions[0].Choices) != 2 || awaiting.RequestID != "srq-00000000000b" {
		t.Fatalf("card = %+v", awaiting)
	}
	if response := h.post("/approvals/srq-00000000000b/confirm"); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirming a question = %d, want 400", response.StatusCode)
	}
	if response := h.postJSON("/approvals/srq-00000000000b/answer", `{"answers":{"q9":"x"}}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("answering an unknown question = %d, want 400", response.StatusCode)
	}
	if response := h.postJSON("/approvals/srq-00000000000b/answer", `{"answers":{"q1":"照片","q2":null}}`); response.StatusCode != http.StatusOK {
		t.Fatalf("answer = %d", response.StatusCode)
	}
	answered := cardOf(w.waitFor(cardIs(cardAnswered)))
	if answered.Answers["q1"] != "照片" {
		t.Fatalf("answered card = %+v", answered)
	}
	select {
	case <-h.serve.answered:
	case <-time.After(5 * time.Second):
	}
	if reply := h.serve.lastReply(); reply != `{"answers":{"q1":"照片","q2":null}}` {
		t.Fatalf("Hermes was answered %s", reply)
	}
}

func TestATurnKeepsTheInstanceAfterTheWindowLeaves(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"long"}`)
	_ = w.conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-h.serve.closed:
		t.Fatal("the connection to the instance closed while the turn ran")
	case <-time.After(300 * time.Millisecond):
	}
	h.serve.finish()
	select {
	case <-h.serve.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection outlived the turn")
	}
}

func TestTheConnectionWaitsForTheReviewOfATurn(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	w.call("session.create", `{"cols":80}`)
	w.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"hello"}`)
	w.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
	_ = w.conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-h.serve.closed:
		t.Fatal("the connection closed under the review of the turn")
	case <-time.After(500 * time.Millisecond):
	}
	started := time.Now()
	h.serve.reviewed()
	select {
	case <-h.serve.closed:
		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("the connection stayed %s after the review", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection outlived the review")
	}
}

func TestEveryWindowWithTheConversationOpenFollowsItsTurn(t *testing.T) {
	h := newHarness(t)
	first := h.open()
	first.call("session.create", `{"cols":80}`)
	first.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"long"}`)
	first.waitFor(func(f frame) bool { return isEvent(f, "message.delta") })
	// A second window opens the same conversation: Hermes now sends its events
	// there only.
	second := h.open()
	resumed := second.call("session.resume", `{"session_id":"`+fakeStored+`","cols":80}`)
	if strings.Contains(string(resumed.Result), "pending_approval") || strings.Contains(string(resumed.Result), "open_requests") {
		t.Fatalf("resume passed Hermes's own approvals to the window: %s", resumed.Result)
	}
	h.serve.finish()
	second.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
	first.waitFor(func(f frame) bool { return isEvent(f, "message.complete") })
}

func TestTheWindowIsTheBrowserOfItsOwnSessionOnly(t *testing.T) {
	h := newHarness(t)
	first := h.open()
	first.call("session.create", `{"cols":80}`)
	register := `{"session_id":"` + fakeRuntime + `","protocol_version":1,"controller_id":"vnas-window","browser_profile_id":"webos","capabilities":%s}`
	for _, refused := range []string{
		fmt.Sprintf(register, `["browser_navigate"]`),
		fmt.Sprintf(register, `["browser_snapshot","browser_cdp"]`),
		fmt.Sprintf(register, `[]`),
		strings.Replace(fmt.Sprintf(register, `["browser_snapshot"]`), "vnas-window", "extension", 1),
		strings.Replace(fmt.Sprintf(register, `["browser_snapshot"]`), `"protocol_version":1`, `"protocol_version":true`, 1),
	} {
		if response := first.call("browser.controller.register", refused); len(response.Error) == 0 {
			t.Fatalf("registration %s passed", refused)
		}
	}
	if response := first.call("browser.controller.register", fmt.Sprintf(register, `["browser_snapshot","browser_click"]`)); len(response.Error) > 0 {
		t.Fatalf("registration refused: %s", response.Error)
	}
	// A second window with the conversation open follows its turn, but the
	// browser commands go to the window that is the session's browser only.
	second := h.open()
	second.call("session.resume", `{"session_id":"`+fakeStored+`","cols":80}`)
	first.call("prompt.submit", `{"session_id":"`+fakeRuntime+`","text":"browse"}`)
	command := first.waitFor(func(f frame) bool { return isEvent(f, "browser.controller.command") })
	var ev struct {
		Payload struct {
			CommandID string `json:"command_id"`
			Action    string `json:"action"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(command.Params, &ev)
	if ev.Payload.Action != "browser_snapshot" {
		t.Fatalf("command = %s", command.Params)
	}
	if response := first.call("browser.controller.result", `{"session_id":"`+fakeRuntime+`","command_id":"`+ev.Payload.CommandID+
		`","ok":true,"result":{"snapshot":"- button \"相册\" [ref=e1]"}}`); len(response.Error) > 0 {
		t.Fatalf("result refused: %s", response.Error)
	}
	second.waitFor(func(f frame) bool {
		if isEvent(f, "browser.controller.command") {
			t.Fatal("a browser command reached another window")
		}
		return isEvent(f, "message.complete") && strings.Contains(string(f.Params), "相册")
	})
	// The window that leaves stops being the session's browser.
	_ = first.conn.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(h.serve.methodsSeen(), "\n"), "browser.controller.detach") {
		if time.Now().After(deadline) {
			t.Fatalf("no detach after the window left: %v", h.serve.methodsSeen())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIdleWindowClosingLetsTheInstanceGo(t *testing.T) {
	h := newHarness(t)
	w := h.open()
	_ = w.conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-h.serve.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection to the instance stayed open with nothing running")
	}
}

func TestRESTRoutesUseTheInstanceTokenAndHideItsInternals(t *testing.T) {
	h := newHarness(t)
	get := func(path string) (int, string) {
		response, err := http.Get(h.server.URL + PathPrefix + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	status, body := get("/sessions")
	if status != http.StatusOK || !strings.Contains(body, `"title":"计划"`) || strings.Contains(body, "hidden") || strings.Contains(body, "system_prompt") {
		t.Fatalf("sessions = %d %s", status, body)
	}
	if status, _ := get("/sessions/20261015/messages"); status != http.StatusBadRequest {
		t.Fatalf("a prefix id = %d, want 400", status)
	}
	if status, body := get("/sessions/" + fakeStored + "/messages"); status != http.StatusOK || !strings.Contains(body, "等你确认") {
		t.Fatalf("messages = %d %s", status, body)
	}
	request, _ := http.NewRequest(http.MethodPatch, h.server.URL+PathPrefix+"/sessions/"+fakeStored, strings.NewReader(`{"title":"已有"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("rename to a used title = %d, want 409", response.StatusCode)
	}
	if !strings.Contains(strings.Join(h.serve.rest, "\n"), "min_messages=1") {
		t.Fatalf("list asked Hermes %v", h.serve.rest)
	}
}
