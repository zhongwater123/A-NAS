package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

// serveAssistantInstance stands in for alice's Hermes API Server on
// <dir>/alice.sock with the rows it really returns.
func serveAssistantInstance(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "alice.key"), []byte("instance-key-alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "alice.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer instance-key-alice" {
			http.Error(w, `{"error":{"code":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		session := `{"id":"api_1","title":"计划","preview":"加一项","started_at":10.5,"last_active":20.25,"message_count":4,"hidden":false}`
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
			_, _ = io.WriteString(w, `{"object":"list","data":[`+session+`,{"id":"api_2","title":null,"preview":null,"started_at":30,"message_count":0}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			_, _ = io.WriteString(w, `{"session":{"id":"api_9","title":null,"started_at":50,"message_count":0}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/sessions/api_1":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "已有") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"Title '已有' is already in use by session api_2","code":"invalid_title"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"session":`+session+`}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/sessions/api_1":
			_, _ = io.WriteString(w, `{"deleted":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/api_1/messages":
			_, _ = io.WriteString(w, `{"object":"list","data":[
				{"id":1,"role":"user","content":"计划里加一项","timestamp":11},
				{"id":2,"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
				{"id":3,"role":"tool","content":"{\"content\":\"# 计划\"}","tool_call_id":"c1"},
				{"id":4,"role":"assistant","content":"已提交，等你确认。","finish_reason":"stop"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/api_1/chat/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: run.started\ndata: {}\n\nevent: assistant.delta\ndata: {\"delta\":\"好的\"}\n\nevent: done\ndata: {}\n\n")
		default:
			http.Error(w, `{"error":{"code":"not_found"}}`, http.StatusNotFound)
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
}

// contractChanges is a change store with one pending write and one move.
type contractChanges struct{ decided map[string]string }

func (c *contractChanges) List(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"changes":[
		{"id":7,"sessionId":"api_1","summary":"在 计划.md 末尾加一项","status":"pending","operations":[{"op":"write","path":"/nas/个人空间/计划.md","exists":true,"before":"# 计划\n","after":"# 计划\n- 买牛奶\n"}]},
		{"id":8,"summary":"归档旧笔记","status":"pending","operations":[{"op":"move","path":"/nas/个人空间/旧.md","to":"/nas/个人空间/归档/旧.md","exists":true}]}]}`), nil
}

func (c *contractChanges) Attach(context.Context, string, string, json.RawMessage) error {
	return assistantapi.ErrChangeNotFound
}

func (c *contractChanges) Decide(_ context.Context, _, id string, confirm bool) (json.RawMessage, error) {
	if id != "7" && id != "8" {
		return nil, assistantapi.ErrChangeNotFound
	}
	if c.decided[id] != "" {
		return nil, assistantapi.ErrChangeDecided
	}
	c.decided[id] = map[bool]string{true: "applied", false: "rejected"}[confirm]
	return json.RawMessage(`{"id":` + id + `,"summary":"在 计划.md 末尾加一项","status":"` + c.decided[id] + `","decidedAt":1791977000.5,"operations":[]}`), nil
}

func TestAssistantAPIReachesOnlyTheMembersOwnInstanceAndFollowsTheContract(t *testing.T) {
	ctx := context.Background()
	// A short directory keeps the socket path within the unix socket limit.
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	serveAssistantInstance(t, dir)

	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assistant := assistantapi.New(assistantapi.Options{SocketDir: dir, KeyDir: dir, Changes: &contractChanges{decided: map[string]string{}}, Logger: logger})
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService, Assistant: assistant, Logger: logger,
	})
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	f := &activityFixture{t: t, handler: handler, accounts: accountService, client: &contractClient{t: t, handler: handler, router: router}}
	f.client.do(http.MethodGet, "/api/v1/assistant/status", "", nil, http.StatusUnauthorized)

	asOwner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	owner, err := accountService.ResolveSession(ctx, asOwner.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, owner.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)

	if body := asAlice.do(http.MethodGet, "/api/v1/assistant/status", "", nil, http.StatusOK).Body.String(); !strings.Contains(body, `"available":true`) {
		t.Fatalf("alice status = %s", body)
	}
	// The administrator has no instance of their own and never reaches alice's.
	if body := asOwner.do(http.MethodGet, "/api/v1/assistant/status", "", nil, http.StatusOK).Body.String(); !strings.Contains(body, `"available":false`) {
		t.Fatalf("owner status = %s", body)
	}
	asOwner.do(http.MethodGet, "/api/v1/assistant/sessions", "", nil, http.StatusServiceUnavailable)

	sessions := asAlice.do(http.MethodGet, "/api/v1/assistant/sessions", "", nil, http.StatusOK).Body.String()
	if !strings.Contains(sessions, `"title":"计划"`) || !strings.Contains(sessions, `"id":"api_2","title":""`) {
		t.Fatalf("sessions = %s", sessions)
	}
	asAlice.do(http.MethodPost, "/api/v1/assistant/sessions", `{}`, nil, http.StatusCreated)
	asAlice.do(http.MethodPatch, "/api/v1/assistant/sessions/api_1", `{"title":"周末"}`, nil, http.StatusOK)
	asAlice.do(http.MethodPatch, "/api/v1/assistant/sessions/api_1", `{"title":"已有"}`, nil, http.StatusConflict)
	asAlice.do(http.MethodGet, "/api/v1/assistant/sessions/api_1/messages", "", nil, http.StatusOK)
	asAlice.do(http.MethodGet, "/api/v1/assistant/sessions/gone/messages", "", nil, http.StatusNotFound)
	stream := asAlice.do(http.MethodPost, "/api/v1/assistant/sessions/api_1/chat", `{"input":"你好"}`, nil, http.StatusOK)
	if !strings.Contains(stream.Body.String(), "event: assistant.delta") {
		t.Fatalf("stream = %s", stream.Body)
	}
	asAlice.do(http.MethodDelete, "/api/v1/assistant/sessions/api_1", "", nil, http.StatusNoContent)

	asAlice.do(http.MethodGet, "/api/v1/assistant/changes", "", nil, http.StatusOK)
	asAlice.do(http.MethodPost, "/api/v1/assistant/changes/7/confirm", "", nil, http.StatusOK)
	asAlice.do(http.MethodPost, "/api/v1/assistant/changes/7/reject", "", nil, http.StatusConflict)
	asAlice.do(http.MethodPost, "/api/v1/assistant/changes/8/reject", "", nil, http.StatusOK)
	asAlice.do(http.MethodPost, "/api/v1/assistant/changes/9/confirm", "", nil, http.StatusNotFound)

	// Writes need the CSRF token like every other product write.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/changes/8/confirm", nil)
	request.AddCookie(asAlice.cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("confirm without CSRF token = %d", recorder.Code)
	}

	unconfigured := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService,
		Assistant: assistantapi.New(assistantapi.Options{}), Logger: logger,
	})
	asAlice.handler = unconfigured
	asAlice.do(http.MethodGet, "/api/v1/assistant/status", "", nil, http.StatusServiceUnavailable)
}
