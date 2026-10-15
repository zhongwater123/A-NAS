package assistantapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
)

// fakeHermes serves the Hermes API Server routes the assistant API uses, on
// <dir>/alice.sock, and records what it was asked.
type fakeHermes struct {
	dir       string
	requests  []string
	authorize string
}

func newFakeHermes(t *testing.T) *fakeHermes {
	t.Helper()
	dir, err := os.MkdirTemp("", "aa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "alice.key"), []byte("instance-key-alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHermes{dir: dir}
	listener, err := net.Listen("unix", filepath.Join(dir, "alice.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(fake.serve))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return fake
}

func (f *fakeHermes) serve(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.authorize = r.Header.Get("Authorization")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"id":"api_1","title":"计划","preview":"加一项","started_at":10,"last_active":20,"message_count":4,"hidden":false},
			{"id":"api_2","title":null,"preview":null,"started_at":30,"message_count":0,"hidden":false},
			{"id":"api_3","title":"internal","started_at":40,"message_count":1,"hidden":true}]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
		_, _ = io.WriteString(w, `{"session":{"id":"api_9","title":"新对话","started_at":50,"message_count":0}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/api_1/messages":
		_, _ = io.WriteString(w, `{"object":"list","session_id":"api_1","data":[{"id":1,"role":"user","content":"你好"}]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/api_1/chat/stream":
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: run.started\ndata: "+string(body)+"\n\n")
		w.(http.Flusher).Flush()
		// A submission split across writes, beside a tool call that is not one.
		_, _ = io.WriteString(w, "event: tool.started\ndata: {\"tool_name\":\"read_file\",\"args\":{\"path\":\"/nas/个人空间/计划.md\"}}\n\n")
		_, _ = io.WriteString(w, "event: tool.started\ndata: {\"tool_name\":\"mcp__anas__submit_changes\",\"args\":{\"summary\":\"加一项\",")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "\"operations\":[{\"op\":\"write\",\"path\":\"/nas/个人空间/计划.md\"}]},\"session_id\":\"api_1\"}\n\n")
		_, _ = io.WriteString(w, "event: tool.completed\ndata: {\"tool_name\":\"mcp__anas__submit_changes\",\"args\":null}\n\n")
		_, _ = io.WriteString(w, "event: assistant.delta\ndata: {\"delta\":\"好的\"}\n\nevent: done\ndata: {}\n\n")
	case r.Method == http.MethodPatch && r.URL.Path == "/api/sessions/api_1":
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "计划") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Title '计划' is already in use by session api_2","code":"invalid_title"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"session":{"id":"api_1","title":"周末","started_at":10,"message_count":4}}`)
	case r.URL.Path == "/api/sessions/missing/messages":
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	default:
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

type fakeChanges struct{ decided, attached []string }

func (f *fakeChanges) Attach(_ context.Context, account, session string, submission json.RawMessage) error {
	f.attached = append(f.attached, account+":"+session+":"+string(submission))
	return nil
}

func (f *fakeChanges) List(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"changes":[{"id":7,"status":"pending"}]}`), nil
}

func (f *fakeChanges) Decide(_ context.Context, account, id string, confirm bool) (json.RawMessage, error) {
	switch id {
	case "404":
		return nil, assistantapi.ErrChangeNotFound
	case "409":
		return nil, assistantapi.ErrChangeDecided
	case "500":
		return nil, errors.New("broker down")
	}
	f.decided = append(f.decided, account+":"+id+":"+map[bool]string{true: "confirm", false: "reject"}[confirm])
	return json.RawMessage(`{"id":7,"status":"applied"}`), nil
}

var alice = accounts.User{ID: "u-alice", Username: "alice"}

func serve(handler http.Handler, method, path, body string, user *accounts.User) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if user != nil {
		request = request.WithContext(assistantapi.WithUser(request.Context(), *user))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", recorder.Body.String(), err)
	}
	return body.Error.Code
}

func TestUnconfiguredAssistantIsUnavailable(t *testing.T) {
	recorder := serve(assistantapi.New(assistantapi.Options{}), http.MethodGet, "/api/v1/assistant/status", "", &alice)
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "assistant_unavailable" {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body)
	}
}

func TestRequestsNeedASignedInUser(t *testing.T) {
	hermes := newFakeHermes(t)
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir})
	if recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions", "", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", recorder.Code)
	}
	if len(hermes.requests) != 0 {
		t.Fatalf("reached the instance: %v", hermes.requests)
	}
}

func TestSessionsComeFromTheMembersOwnInstance(t *testing.T) {
	hermes := newFakeHermes(t)
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir})
	recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions", "", &alice)
	if recorder.Code != http.StatusOK {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body)
	}
	if hermes.authorize != "Bearer instance-key-alice" {
		t.Fatalf("authorization %q", hermes.authorize)
	}
	var body struct {
		Sessions []struct {
			ID, Title, Preview string
			MessageCount       int
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 2 || body.Sessions[0].Title != "计划" || body.Sessions[1].Title != "" || body.Sessions[0].MessageCount != 4 {
		t.Fatalf("sessions %+v", body.Sessions)
	}
	bob := accounts.User{ID: "u-bob", Username: "bob"}
	if recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions", "", &bob); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("bob without an instance got %d", recorder.Code)
	}
}

func TestStatusReportsWhetherTheMemberHasAnInstance(t *testing.T) {
	hermes := newFakeHermes(t)
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir})
	recorder := serve(handler, http.MethodGet, "/api/v1/assistant/status", "", &alice)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"available":true`) {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body)
	}
}

func TestMessagesAndInvalidIDs(t *testing.T) {
	hermes := newFakeHermes(t)
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir})
	recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions/api_1/messages", "", &alice)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"messages":[{"id":1`) {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body)
	}
	if recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions/missing/messages", "", &alice); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing session got %d", recorder.Code)
	}
	if recorder := serve(handler, http.MethodGet, "/api/v1/assistant/sessions/..%2Fetc/messages", "", &alice); recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal got %d", recorder.Code)
	}
	for _, request := range hermes.requests {
		if strings.Contains(request, "etc") {
			t.Fatalf("forwarded %q", request)
		}
	}
}

func TestRenameChecksTheTitle(t *testing.T) {
	hermes := newFakeHermes(t)
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir})
	if recorder := serve(handler, http.MethodPatch, "/api/v1/assistant/sessions/api_1", `{"title":" 周末 "}`, &alice); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"title":"周末"`) {
		t.Fatalf("rename got %d %s", recorder.Code, recorder.Body)
	}
	if recorder := serve(handler, http.MethodPatch, "/api/v1/assistant/sessions/api_1", `{"title":"计划"}`, &alice); recorder.Code != http.StatusConflict || errorCode(t, recorder) != "title_in_use" {
		t.Fatalf("taken title got %d %s", recorder.Code, recorder.Body)
	}
	requests := len(hermes.requests)
	long := `{"title":"` + strings.Repeat("长", 101) + `"}`
	if recorder := serve(handler, http.MethodPatch, "/api/v1/assistant/sessions/api_1", long, &alice); recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_title" {
		t.Fatalf("long title got %d %s", recorder.Code, recorder.Body)
	}
	if len(hermes.requests) != requests {
		t.Fatalf("a too-long title reached the instance")
	}
}

func TestChatRelaysTheStream(t *testing.T) {
	hermes := newFakeHermes(t)
	changes := &fakeChanges{}
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir, Changes: changes})
	recorder := serve(handler, http.MethodPost, "/api/v1/assistant/sessions/api_1/chat", `{"input":"  加一项交电费  "}`, &alice)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("got %d %s", recorder.Code, recorder.Header())
	}
	stream := recorder.Body.String()
	for _, want := range []string{`{"input":"加一项交电费"}`, "event: tool.started", "event: assistant.delta", "event: done"} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream lacks %q:\n%s", want, stream)
		}
	}
	// The change submitted in this turn belongs to this conversation.
	want := `alice:api_1:{"summary":"加一项","operations":[{"op":"write","path":"/nas/个人空间/计划.md"}]}`
	if len(changes.attached) != 1 || changes.attached[0] != want {
		t.Fatalf("attached %q", changes.attached)
	}
	if recorder := serve(handler, http.MethodPost, "/api/v1/assistant/sessions/api_1/chat", `{"input":"   "}`, &alice); recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty input got %d", recorder.Code)
	}
}

func TestPendingChanges(t *testing.T) {
	hermes := newFakeHermes(t)
	if recorder := serve(assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir}), http.MethodGet, "/api/v1/assistant/changes", "", &alice); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store got %d", recorder.Code)
	}
	changes := &fakeChanges{}
	handler := assistantapi.New(assistantapi.Options{SocketDir: hermes.dir, KeyDir: hermes.dir, Changes: changes})
	if recorder := serve(handler, http.MethodGet, "/api/v1/assistant/changes", "", &alice); recorder.Code != http.StatusOK {
		t.Fatalf("list got %d", recorder.Code)
	}
	if recorder := serve(handler, http.MethodPost, "/api/v1/assistant/changes/7/confirm", "", &alice); recorder.Code != http.StatusOK {
		t.Fatalf("confirm got %d", recorder.Code)
	}
	if len(changes.decided) != 1 || changes.decided[0] != "alice:7:confirm" {
		t.Fatalf("decided %v", changes.decided)
	}
	for id, want := range map[string]int{"404": http.StatusNotFound, "409": http.StatusConflict, "500": http.StatusBadGateway, "x7": http.StatusBadRequest} {
		if recorder := serve(handler, http.MethodPost, "/api/v1/assistant/changes/"+id+"/reject", "", &alice); recorder.Code != want {
			t.Fatalf("reject %s got %d, want %d", id, recorder.Code, want)
		}
	}
}
