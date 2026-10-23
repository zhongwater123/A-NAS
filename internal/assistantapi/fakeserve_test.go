package assistantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	// fakeToken is alice's instance secret, her password for Hermes's login.
	fakeToken   = "serve-token-alice"
	fakeRuntime = "0a1b2c3d"
	fakeStored  = "20261015_120000_abcdef"
)

// fakeServe stands in for alice's `hermes serve` on <dir>/alice.sock: the
// REST session routes and /api/ws, with a turn that calls 提交更改 and asks
// for approval the way Hermes does (#111). It records what reached it.
type fakeServe struct {
	t   *testing.T
	dir string

	mu       sync.Mutex
	methods  []string
	rest     []string
	answers  []string // choices received, from response frames or approval.respond
	replies  []string // results of the response frames received, as sent
	resolved bool     // approval.respond answers resolved: 1 while true
	conns    int
	conn     *websocket.Conn
	// access is the access token of the current login; logins counts them.
	access string
	logins int
	// controller is the connection that registered as the browser; results
	// are the browser command results it sent.
	controller *websocket.Conn
	results    chan string
	closed     chan struct{}
	answered   chan string
	// The learning graph: alice's memories (store, text) and the skills the
	// assistant made (name, createdBy). system are A-NAS's own skills, from
	// skills.external_dirs: Hermes shows their text but leaves them out of the
	// graph and refuses to delete them.
	memories [][2]string
	skills   [][2]string
	system   []string
	// Alice's scheduled tasks (tasks_test.go), and how long a run takes.
	jobs    []*fakeJob
	runTime time.Duration
}

func newFakeServe(t *testing.T) *fakeServe {
	t.Helper()
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "alice.token"), []byte(fakeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeServe{t: t, dir: dir, resolved: true, closed: make(chan struct{}, 8), answered: make(chan string, 8), results: make(chan string, 8),
		memories: [][2]string{{"memory", "alice 喜欢简短的回答"}, {"profile", "alice 每周日大扫除"}},
		skills:   [][2]string{{"family-trip", "learn"}, {"anas-photos", "agent"}}, system: []string{"anas-files"}}
	listener, err := net.Listen("unix", filepath.Join(dir, "alice.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return f
}

func (f *fakeServe) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != instanceHost {
		http.Error(w, `{"detail":"bad host"}`, http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/auth/password-login" {
		f.login(w, r)
		return
	}
	f.mu.Lock()
	access := f.access
	f.mu.Unlock()
	if r.URL.Path == "/api/ws" {
		if access == "" || r.URL.Query().Get("token") != access {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		f.gateway(w, r)
		return
	}
	if access == "" || r.Header.Get("Authorization") != "Bearer "+access {
		http.Error(w, `{"detail":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.rest = append(f.rest, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	session := `{"id":"` + fakeStored + `","title":"计划","preview":"加一项","started_at":10.5,"last_active":20.25,"message_count":4,"hidden":0,"system_prompt":"secret"}`
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
		_, _ = io.WriteString(w, `{"sessions":[`+session+`,{"id":"20261015_130000_aaaaaa","title":null,"preview":null,"started_at":30,"message_count":2},
			{"id":"20261015_140000_bbbbbb","title":"hidden","started_at":40,"message_count":1,"hidden":1}],"total":3}`)
	case r.Method == http.MethodPatch && r.URL.Path == "/api/sessions/"+fakeStored:
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "已有") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"detail":"Title '已有' is already in use by session 20261015_130000_aaaaaa"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"title":"周末"}`)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/sessions/20261015_130000_aaaaaa":
		// A turn is writing to it.
		http.Error(w, `{"detail":"session has an active writer"}`, http.StatusConflict)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/sessions/"):
		_, _ = io.WriteString(w, `{"ok":true}`)
	case r.URL.Path == "/api/learning/graph" || r.URL.Path == "/api/learning/node":
		f.learning(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/cron/") || strings.HasPrefix(r.URL.Path, "/api/sessions/cron_"):
		f.cron(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/"+fakeStored+"/messages":
		_, _ = io.WriteString(w, `{"session_id":"`+fakeStored+`","messages":[
			{"id":1,"role":"user","content":"计划里加一项","timestamp":11},
			{"id":4,"role":"assistant","content":"已提交，等你确认。","finish_reason":"stop"}],"pagination":{}}`)
	default:
		http.Error(w, `{"detail":"Not found"}`, http.StatusNotFound)
	}
}

// login answers Hermes's password login: alice with her instance's secret
// gets a new access token, which replaces the last.
func (f *fakeServe) login(w http.ResponseWriter, r *http.Request) {
	var login struct{ Provider, Username, Password string }
	_ = json.NewDecoder(r.Body).Decode(&login)
	if r.Method != http.MethodPost || login.Provider != "basic" || login.Username != "alice" || login.Password != fakeToken {
		http.Error(w, `{"detail":"Invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.logins++
	f.access = fmt.Sprintf("access-%d", f.logins)
	access := f.access
	f.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "hermes_session_at", Value: access, MaxAge: 43200, Path: "/"})
	_, _ = io.WriteString(w, `{"ok":true,"next":"/"}`)
}

// restart stands in for the instance restarting: it signs sessions with a new
// key, so the last access token is refused.
func (f *fakeServe) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access = "signed-before-the-restart"
}

// learning serves Hermes's learning graph routes over the fake's memories and
// skills; memory ids are memory:<store>:<n>:<fingerprint>, the fingerprint
// being the text's length here.
func (f *fakeServe) learning(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	memoryID := func(n int) string {
		return fmt.Sprintf("memory:%s:%d:%d", f.memories[n][0], n, len(f.memories[n][1]))
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Path == "/api/learning/node" {
			id := r.URL.Query().Get("id")
			if slices.Contains(append(f.skillNames(), f.system...), id) {
				_, _ = fmt.Fprintf(w, `{"ok":true,"kind":"skill","id":%q,"content":"---\nname: %s\n---\n# 出游计划"}`, id, id)
				return
			}
			http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
			return
		}
		var nodes, cards []string
		for _, skill := range f.skills {
			nodes = append(nodes, fmt.Sprintf(`{"id":%q,"kind":"skill","category":"travel","createdBy":%q,"useCount":2,"timestamp":1800000000}`, skill[0], skill[1]))
		}
		for n, memory := range f.memories {
			nodes = append(nodes, fmt.Sprintf(`{"id":%q,"kind":"memory","memorySource":%q,"category":"memory","timestamp":1800000001}`, memoryID(n), memory[0]))
			cards = append(cards, fmt.Sprintf(`{"source":%q,"title":%q,"body":%q,"fingerprint":"%d"}`, memory[0], memory[1], memory[1], len(memory[1])))
		}
		_, _ = fmt.Fprintf(w, `{"nodes":[%s],"edges":[],"clusters":[],"memory":[%s],"stats":{}}`, strings.Join(nodes, ","), strings.Join(cards, ","))
	case http.MethodDelete:
		var body struct {
			ID      string `json:"id"`
			Profile string `json:"profile"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Profile != "" {
			http.Error(w, `{"detail":"a profile reached Hermes"}`, http.StatusTeapot)
			return
		}
		if slices.Contains(f.system, body.ID) {
			http.Error(w, `{"detail":"external skills are read-only to the curator"}`, http.StatusBadRequest)
			return
		}
		for n := range f.memories {
			if memoryID(n) == body.ID {
				f.memories = append(f.memories[:n], f.memories[n+1:]...)
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
		}
		for n, skill := range f.skills {
			if skill[0] == body.ID {
				f.skills = append(f.skills[:n], f.skills[n+1:]...)
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
		}
		http.Error(w, `{"detail":"memory changed — stale, refresh the graph"}`, http.StatusBadRequest)
	}
}

func (f *fakeServe) skillNames() []string {
	names := []string{}
	for _, skill := range f.skills {
		names = append(names, skill[0])
	}
	return names
}

func (f *fakeServe) gateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	// A browser command's result can be a screenshot of the page.
	conn.SetReadLimit(maxUpstreamBytes)
	f.mu.Lock()
	f.conns++
	f.conn = conn
	f.mu.Unlock()
	defer func() { f.closed <- struct{}{} }()
	ctx := context.Background()
	f.event(conn, "gateway.ready", "", `{"replay_epoch":"e1","heartbeat":true,"change_events":true,"skin":{}}`)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg frame
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Method == "" {
			// An answer to a request from the server.
			var answer struct {
				Choice string `json:"choice"`
			}
			_ = json.Unmarshal(msg.Result, &answer)
			f.mu.Lock()
			f.replies = append(f.replies, string(msg.Result)+string(msg.Error))
			f.mu.Unlock()
			f.record(answer.Choice)
			continue
		}
		f.mu.Lock()
		f.methods = append(f.methods, msg.Method+" "+string(msg.Params))
		f.mu.Unlock()
		switch msg.Method {
		case "client.capabilities":
			f.reply(conn, msg.ID, `{"server_requests":["approval"]}`)
		case "session.create":
			f.reply(conn, msg.ID, `{"session_id":"`+fakeRuntime+`","stored_session_id":"`+fakeStored+`","message_count":0,"messages":[],
				"info":{"model":"m","system_prompt":"the whole prompt","mcp_servers":[{"name":"anas"}],"yolo":false,"approval_mode":"manual"}}`)
		case "session.resume":
			// The live session itself, as Hermes answers for one still in memory;
			// its events now go to this connection.
			f.reply(conn, msg.ID, `{"session_id":"`+fakeRuntime+`","session_key":"`+fakeStored+`","running":true,"message_count":0,"messages":[],
				"info":{"model":"m"},"pending_approval":{"request_id":"req-9"},"open_requests":[{"id":"srq-0123456789ab"}]}`)
		case "prompt.submit":
			f.reply(conn, msg.ID, `{"status":"streaming"}`)
			var params struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			go f.turn(conn, params.Text)
		case "browser.controller.register":
			f.mu.Lock()
			f.controller = conn
			f.mu.Unlock()
			f.reply(conn, msg.ID, `{"scope":{"session_id":"`+fakeStored+`","controller_id":"vnas-window","capabilities":["browser_snapshot"]}}`)
		case "browser.controller.result":
			// The answer goes before the turn hears of the result. Otherwise the
			// turn's message.complete can come first, and the window's call,
			// which passes over every frame before its answer, swallows it.
			f.reply(conn, msg.ID, `{"accepted":true}`)
			f.results <- string(msg.Params)
		case "browser.controller.detach":
			f.reply(conn, msg.ID, `{"detached":true}`)
		case "approval.respond":
			var params struct {
				Choice string `json:"choice"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			f.mu.Lock()
			resolved := f.resolved
			f.mu.Unlock()
			if resolved {
				f.record(params.Choice)
				f.reply(conn, msg.ID, `{"resolved":1}`)
			} else {
				f.reply(conn, msg.ID, `{"resolved":0}`)
			}
		default:
			f.reply(conn, msg.ID, `{}`)
		}
	}
}

// turn plays one turn. "write" calls 提交更改 and waits for the approval;
// "review" ends the turn and then asks for approval with no call under way,
// as the background review does; "long" runs until finish is called.
func (f *fakeServe) turn(conn *websocket.Conn, said string) {
	f.event(conn, "message.start", fakeRuntime, `{}`)
	f.event(conn, "pet.mood", fakeRuntime, `{"mood":"happy"}`)
	switch said {
	case "write":
		f.event(conn, "tool.start", fakeRuntime, `{"tool_id":"t1","name":"`+submitTool+`","args":{"summary":"加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md","from":"/nas/助手/待确认/计划.md"}]}}`)
		f.request(conn, "srq-0123456789ab", "approval", `{"session_id":"`+fakeRuntime+`","request_id":"req-1","pattern_key":"mcp_elicitation","choices":["once","session","always","deny"]}`)
		choice := <-f.answered
		f.event(conn, "tool.complete", fakeRuntime, `{"tool_id":"t1","name":"`+submitTool+`","result_text":"`+choice+`"}`)
		f.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	case "review":
		f.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
		f.request(conn, "srq-ba9876543210", "approval", `{"session_id":"`+fakeRuntime+`","request_id":"req-2","pattern_key":"mcp_elicitation"}`)
	case "rm", "script":
		// A dangerous command, or a script, waits for an approval as Hermes asks
		// for one: the command itself, no request to pair with a call of 提交更改.
		command, key := `rm -rf /nas/助手/旧草稿`, "recursive delete"
		if said == "script" {
			command, key = `execute_code <<'PY'\nprint(1)\nPY`, "execute_code"
		}
		f.event(conn, "tool.start", fakeRuntime, `{"tool_id":"t2","name":"terminal","args":{}}`)
		f.request(conn, "srq-00000000000a", "approval", `{"session_id":"`+fakeRuntime+`","request_id":"req-3","command":"`+command+
			`","pattern_key":"`+key+`","pattern_keys":["`+key+`"],"description":"`+key+`","choices":["once","session","always","deny"]}`)
		choice := <-f.answered
		f.event(conn, "tool.complete", fakeRuntime, `{"tool_id":"t2","name":"terminal","result_text":"`+choice+`"}`)
		f.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	case "ask":
		// The clarify tool: questions as a request from the server.
		f.event(conn, "tool.start", fakeRuntime, `{"tool_id":"t3","name":"clarify","args":{}}`)
		f.request(conn, "srq-00000000000b", "clarify", `{"session_id":"`+fakeRuntime+
			`","questions":[{"qid":"q1","question":"放到哪个文件夹？","choices":["照片","文档"]},{"qid":"q2","question":"改个名字吗？"}]}`)
		<-f.answered
		f.event(conn, "message.complete", fakeRuntime, `{"text":"done","status":"complete"}`)
	case "browse":
		// browser_snapshot: Hermes sends the command to the connection that
		// registered as the session's browser, and waits for its result.
		f.mu.Lock()
		controller := f.controller
		f.mu.Unlock()
		f.event(conn, "tool.start", fakeRuntime, `{"tool_id":"t4","name":"browser_snapshot","args":{}}`)
		f.event(controller, "browser.controller.command", fakeRuntime,
			`{"command_id":"0123456789abcdef0123456789abcdef","action":"browser_snapshot","arguments":{},"controller_id":"vnas-window"}`)
		select {
		case result := <-f.results:
			f.event(conn, "message.complete", fakeRuntime, `{"text":`+strconv.Quote(result)+`,"status":"complete"}`)
		case <-time.After(5 * time.Second):
			f.event(conn, "message.complete", fakeRuntime, `{"text":"no result","status":"complete"}`)
		}
	case "look":
		// browser_vision on the window's lane: Hermes asks the window for a
		// screenshot of its page (hermes-patches/0003).
		f.mu.Lock()
		controller := f.controller
		f.mu.Unlock()
		f.event(conn, "tool.start", fakeRuntime, `{"tool_id":"t5","name":"browser_vision","args":{"question":"what is open?"}}`)
		f.event(controller, "browser.controller.command", fakeRuntime,
			`{"command_id":"fedcba9876543210fedcba9876543210","action":"browser_screenshot","arguments":{"annotate":false},"controller_id":"vnas-window"}`)
		select {
		case result := <-f.results:
			f.event(conn, "message.complete", fakeRuntime, `{"text":"received `+strconv.Itoa(len(result))+` bytes","status":"complete"}`)
		case <-time.After(5 * time.Second):
			f.event(conn, "message.complete", fakeRuntime, `{"text":"no result","status":"complete"}`)
		}
	case "long":
		f.event(conn, "message.delta", fakeRuntime, `{"text":"thinking…"}`)
	default:
		f.event(conn, "message.delta", fakeRuntime, `{"text":"hi"}`)
		f.event(conn, "message.complete", fakeRuntime, `{"text":"hi","status":"complete"}`)
	}
}

func (f *fakeServe) methodsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func (f *fakeServe) finish() {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	f.event(conn, "message.complete", fakeRuntime, `{"text":"late","status":"complete"}`)
}

// reviewed reports a background review of the last turn, as Hermes does
// when the review changed memory or skills.
func (f *fakeServe) reviewed() {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	f.event(conn, "review.summary", fakeRuntime, `{"text":"💾 自我改进检阅：记忆已更新"}`)
}

func (f *fakeServe) record(choice string) {
	f.mu.Lock()
	f.answers = append(f.answers, choice)
	f.mu.Unlock()
	f.answered <- choice
}

func (f *fakeServe) write(conn *websocket.Conn, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, []byte(value))
}

func (f *fakeServe) reply(conn *websocket.Conn, id json.RawMessage, result string) {
	f.write(conn, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":`+result+`}`)
}

func (f *fakeServe) event(conn *websocket.Conn, kind, session, payload string) {
	f.write(conn, `{"jsonrpc":"2.0","method":"event","params":{"type":"`+kind+`","session_id":"`+session+`","payload":`+payload+`,"seq":1}}`)
}

func (f *fakeServe) request(conn *websocket.Conn, id, method, params string) {
	f.write(conn, `{"jsonrpc":"2.0","id":"`+id+`","method":"`+method+`","params":`+params+`}`)
}

func (f *fakeServe) firstAnswer() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.answers) == 0 {
		return ""
	}
	return f.answers[0]
}

func (f *fakeServe) lastReply() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		return ""
	}
	return f.replies[len(f.replies)-1]
}

func (f *fakeServe) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// fakeChanges stands in for the executor and records the session each call
// carried.
type fakeChanges struct {
	mu       sync.Mutex
	calls    []string
	sessions []string
	status   string
}

func (c *fakeChanges) note(ctx context.Context, call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	c.sessions = append(c.sessions, sessionTokenOf(ctx))
}

func (c *fakeChanges) change(status string) Change {
	raw, _ := json.Marshal(map[string]any{"id": "c0ffee01", "status": status, "summary": "加一项",
		"operations": []map[string]any{{"op": "write", "path": "/nas/个人空间/计划.md", "after": "- 买牛奶"}}})
	return Change{ID: "c0ffee01", Status: status, Raw: raw}
}

func (c *fakeChanges) Prepare(ctx context.Context, _ string, submission Submission) (Change, error) {
	c.note(ctx, "prepare "+submission.RequestID+" "+string(submission.Args))
	return c.change("awaiting"), nil
}

func (c *fakeChanges) Decide(ctx context.Context, _, id string, decision Decision) (Change, error) {
	c.note(ctx, string(decision)+" "+id)
	status := map[Decision]string{Approve: "approved", Reject: "rejected", Expire: "expired"}[decision]
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
	return c.change(status), nil
}

// Get reports a granted change as applied, as after the tool ran it, and any
// other as it was left.
func (c *fakeChanges) Get(ctx context.Context, _, id string) (Change, error) {
	c.note(ctx, "get "+id)
	c.mu.Lock()
	status := c.status
	c.mu.Unlock()
	if status == "approved" {
		status = "applied"
	}
	return c.change(status), nil
}

func (c *fakeChanges) recorded() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...), append([]string(nil), c.sessions...)
}
