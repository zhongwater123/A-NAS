package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/getkin/kin-openapi/openapi3"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
)

const assistantStored = "20261015_120000_abcdef"

// assistantInstance reports the gateway connections of the stand-in.
type assistantInstance struct {
	opened chan struct{}
	closed chan struct{}
}

// serveAssistantInstance stands in for alice's `hermes serve` on
// <dir>/alice.sock with the rows it really returns.
func serveAssistantInstance(t *testing.T, dir string) *assistantInstance {
	t.Helper()
	instance := &assistantInstance{opened: make(chan struct{}, 8), closed: make(chan struct{}, 8)}
	if err := os.WriteFile(filepath.Join(dir, "alice.token"), []byte("serve-token-alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "alice.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ws" && r.URL.Query().Get("token") == "serve-token-alice" && r.Host == "127.0.0.1:9119" {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			instance.opened <- struct{}{}
			defer func() { instance.closed <- struct{}{} }()
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{}}}`))
			for {
				if _, _, err := conn.Read(r.Context()); err != nil {
					return
				}
			}
		}
		if r.Header.Get("X-Hermes-Session-Token") != "serve-token-alice" || r.Host != "127.0.0.1:9119" {
			http.Error(w, `{"detail":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		session := `{"id":"` + assistantStored + `","title":"计划","preview":"加一项","started_at":10.5,"last_active":20.25,"message_count":4,"hidden":0}`
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
			_, _ = io.WriteString(w, `{"sessions":[`+session+`,{"id":"20261015_130000_aaaaaa","title":null,"preview":null,"started_at":30,"message_count":2}],"total":2}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/sessions/"+assistantStored:
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "已有") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"detail":"Title '已有' is already in use by session 20261015_130000_aaaaaa"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"title":"周末"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/sessions/"+assistantStored:
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/learning/graph":
			_, _ = io.WriteString(w, `{"nodes":[{"id":"family-trip","kind":"skill","category":"travel","createdBy":"learn","useCount":2,"timestamp":1800000000},
				{"id":"memory:memory:0:ab12","kind":"memory","memorySource":"memory","timestamp":1800000001}],
				"memory":[{"source":"memory","title":"简短","body":"alice 喜欢简短的回答","fingerprint":"ab12"}],"edges":[],"stats":{}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/learning/node":
			_, _ = io.WriteString(w, `{"ok":true,"kind":"skill","id":"family-trip","content":"# 出游计划"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/learning/node":
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/"+assistantStored+"/messages":
			_, _ = io.WriteString(w, `{"session_id":"`+assistantStored+`","messages":[
				{"id":1,"role":"user","content":"计划里加一项","timestamp":11},
				{"id":2,"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
				{"id":3,"role":"tool","content":"{\"content\":\"# 计划\"}","tool_call_id":"c1"},
				{"id":4,"role":"assistant","content":"已提交，等你确认。","finish_reason":"stop"}],"pagination":{}}`)
		default:
			http.Error(w, `{"detail":"Not found"}`, http.StatusNotFound)
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return instance
}

// Signing in starts the member's assistant in the background and keeps it
// running until they sign out, so that opening it does not wait for a cold
// start.
func TestSigningInKeepsTheMembersAssistantRunningUntilSigningOut(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	instance := serveAssistantInstance(t, dir)
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assistant := assistantapi.New(assistantapi.Options{
		Connector: assistantapi.SocketConnector{SocketDir: dir, TokenDir: dir}, SocketDir: dir, Logger: logger,
	})
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
	asOwner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	owner, err := accountService.ResolveSession(ctx, asOwner.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, owner.User, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	// The owner has no assistant here, so nothing is started for them.
	select {
	case <-instance.opened:
		t.Fatal("an assistant was started for a member without one")
	case <-time.After(200 * time.Millisecond):
	}

	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	select {
	case <-instance.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("signing in did not start alice's assistant")
	}
	// Loading the desktop again keeps the same connection.
	asAlice.do(http.MethodGet, "/api/v1/session", "", nil, http.StatusOK)
	select {
	case <-instance.opened:
		t.Fatal("a second connection was opened for the same member")
	case <-instance.closed:
		t.Fatal("the kept connection closed while alice is signed in")
	case <-time.After(300 * time.Millisecond):
	}

	asAlice.do(http.MethodDelete, "/api/v1/session", "", nil, http.StatusNoContent)
	select {
	case <-instance.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("signing out did not let alice's assistant go")
	}
}

func TestAssistantAPIReachesOnlyTheMembersOwnInstanceAndFollowsTheContract(t *testing.T) {
	ctx := context.Background()
	// A short directory keeps the socket path within the unix socket limit.
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_ = serveAssistantInstance(t, dir)

	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assistant := assistantapi.New(assistantapi.Options{
		Connector: assistantapi.SocketConnector{SocketDir: dir, TokenDir: dir}, SocketDir: dir, Logger: logger,
	})
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
	if !strings.Contains(sessions, `"title":"计划"`) || !strings.Contains(sessions, `"id":"20261015_130000_aaaaaa","title":""`) {
		t.Fatalf("sessions = %s", sessions)
	}
	stored := "/api/v1/assistant/sessions/" + assistantStored
	asAlice.do(http.MethodPatch, stored, `{"title":"周末"}`, nil, http.StatusOK)
	asAlice.do(http.MethodPatch, stored, `{"title":"已有"}`, nil, http.StatusConflict)
	asAlice.do(http.MethodGet, stored+"/messages", "", nil, http.StatusOK)
	asAlice.do(http.MethodGet, "/api/v1/assistant/sessions/20261015_120000_ffffff/messages", "", nil, http.StatusNotFound)
	asAlice.do(http.MethodDelete, stored, "", nil, http.StatusNoContent)

	if body := asAlice.do(http.MethodGet, "/api/v1/assistant/approvals", "", nil, http.StatusOK).Body.String(); !strings.Contains(body, `"approvals":[]`) {
		t.Fatalf("approvals = %s", body)
	}
	asAlice.do(http.MethodPost, "/api/v1/assistant/approvals/req-1/confirm", "", nil, http.StatusNotFound)
	asAlice.do(http.MethodPost, "/api/v1/assistant/approvals/req-1/reject", "", nil, http.StatusNotFound)
	asAlice.do(http.MethodPost, "/api/v1/assistant/approvals/req-1/confirm", `{"forSession":true}`, nil, http.StatusNotFound)
	asAlice.do(http.MethodPost, "/api/v1/assistant/approvals/srq-00000000000b/answer", `{"answers":{"q1":"照片"}}`, nil, http.StatusNotFound)
	if body := asAlice.do(http.MethodGet, "/api/v1/assistant/memory", "", nil, http.StatusOK).Body.String(); !strings.Contains(body, "简短的回答") {
		t.Fatalf("memory = %s", body)
	}
	asAlice.do(http.MethodGet, "/api/v1/assistant/memory/items/family-trip", "", nil, http.StatusOK)
	asAlice.do(http.MethodDelete, "/api/v1/assistant/memory/items/family-trip", "", nil, http.StatusNoContent)
	asAlice.do(http.MethodDelete, "/api/v1/assistant/memory/items/not-learned", "", nil, http.StatusNotFound)
	asAlice.do(http.MethodPost, "/api/v1/assistant/memory/clear", `{"conversations":false}`, nil, http.StatusOK)

	// The gateway is a WebSocket; a plain request is told so.
	asAlice.do(http.MethodGet, "/api/v1/assistant/ws", "", nil, http.StatusUpgradeRequired)

	// Writes need the CSRF token like every other product write.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/approvals/req-1/confirm", nil)
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
