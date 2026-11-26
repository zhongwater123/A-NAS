//go:build linux

package workspaceservice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
	"github.com/zhongwater123/A-NAS/internal/workspaceservice"
)

// bearer adds an agent token to every request, as Claude Code does with a
// configured header.
type bearer struct{ token string }

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}

type member struct {
	server *httptest.Server
	cookie *http.Cookie
	csrf   string
}

func signIn(t *testing.T, server *httptest.Server, username, password string) member {
	t.Helper()
	response, err := http.Post(server.URL+"/api/v1/session", "application/json", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("sign in %s: %d %v", username, response.StatusCode, err)
	}
	return member{server: server, cookie: response.Cookies()[0], csrf: session.CSRFToken}
}

func (m member) call(t *testing.T, method, path, contentType string, body []byte) (int, []byte) {
	t.Helper()
	request, _ := http.NewRequest(method, m.server.URL+path, bytes.NewReader(body))
	request.AddCookie(m.cookie)
	request.Header.Set("X-CSRF-Token", m.csrf)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(response.Body)
	return response.StatusCode, answer
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// The whole chain of a member's request, as the specification walks it: a
// member uploads a page in the desktop and picks its button, their agent
// reads what they picked and makes it smaller, and the page has a new
// version that points at the button. Every hop is the real one but the
// browser: the Product Service's agent posture, the proxy's headers, the
// service's authentication hook in storeservice, the Host Agent's read of
// control.db, the API, the MCP endpoint and the store.
func TestAMembersAgentChangesWhatTheMemberPicked(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	controlDB := filepath.Join(dir, "control.db")
	store, err := accounts.OpenSQLite(controlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, credentials{}, accounts.Options{PasswordCost: accounts.PasswordCost{MemoryKiB: 64, Iterations: 1, Parallelism: 1}})
	admin, err := users.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := users.CreateMember(ctx, admin, "alice", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "workspace.sock")
	directory := sessiondir.New(controlDB)
	t.Cleanup(func() { _ = directory.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- workspaceservice.Run(serviceCtx, workspaceservice.Config{Root: root, SocketPath: socket, Directory: directory, Logger: logger,
			CheckStore: func(string) error { return nil }, RetryInterval: 20 * time.Millisecond,
			// A temporary directory has less room than the data volume's reserve.
			Admits: func(string, int64) (bool, error) { return true, nil }})
	}()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	product := httptest.NewServer(httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		Workspace: workspaceservice.Proxy(socket, logger), Logger: logger,
	}))
	t.Cleanup(product.Close)

	owner := signIn(t, product, "owner", "correct horse battery staple")
	member := signIn(t, product, "alice", "another long passphrase")
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _ := owner.call(t, http.MethodGet, "/api/v1/workspace", "", nil)
		if status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the workspace service did not start: %d", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, _ := member.call(t, http.MethodGet, "/api/v1/workspace", "", nil); status != http.StatusNotFound {
		t.Fatalf("a member nobody added sees the workspace: %d", status)
	}
	if status, body := owner.call(t, http.MethodPut, "/api/v1/workspace/admin/members/"+alice.ID, "", nil); status != http.StatusNoContent {
		t.Fatalf("adding alice: %d %s", status, body)
	}

	source, err := os.ReadFile(filepath.Join("..", "workspace", "testdata", "prd.html"))
	if err != nil {
		t.Fatal(err)
	}
	status, body := member.call(t, http.MethodPost, "/api/v1/workspace/pages?name=prd.html", "text/html", source)
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, body)
	}
	var page struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &page)
	status, view := member.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID+"/versions/latest/view", "", nil)
	if status != http.StatusOK || !bytes.Contains(view, []byte(`<button data-anas-at="`)) {
		t.Fatalf("view: %d", status)
	}

	// The member picks the button: the frame reports its data-anas-at.
	button := bytes.Index(source, []byte(`<button class="btn btn-primary">`))
	member.call(t, http.MethodPut, "/api/v1/workspace/viewing", "application/json", []byte(`{"pageId":"`+page.ID+`","version":1}`))
	selection := `{"pageId":"` + page.ID + `","version":1,"at":` + strconv.Itoa(button) + `,"generated":false,"tag":"button","text":"发出分享","dom":"",` +
		`"rect":{"x":10,"y":20,"w":120,"h":52},"style":{"padding":"14px 28px"},"rules":[".btn",".btn-primary"]}`
	if status, body := member.call(t, http.MethodPut, "/api/v1/workspace/selection", "application/json", []byte(selection)); status != http.StatusNoContent {
		t.Fatalf("selection: %d %s", status, body)
	}

	_, token, err := users.CreateAgentToken(ctx, alice, "办公电脑", accounts.AgentClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: product.URL + "/api/v1/workspace/mcp", HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("an agent connects: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	context1, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_context"})
	if err != nil {
		t.Fatal(err)
	}
	read := text(context1)
	for _, want := range []string{`"lines": "108-108"`, `发出分享`, `"line": 44`, `padding: 14px 28px`, `"latest_version": 1`} {
		if !strings.Contains(read, want) {
			t.Fatalf("the context lacks %s:\n%s", want, read)
		}
	}
	rule := ".btn { border: 0; border-radius: 10px; padding: 14px 28px; font-size: 16px; font-weight: 600; cursor: pointer; }"
	edited, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_edit_page", Arguments: map[string]any{
		"page": page.ID, "base_version": 1, "note": "把按钮改小",
		"edits": []map[string]string{{"old_text": rule, "new_text": strings.Replace(rule, "padding: 14px 28px; font-size: 16px", "padding: 8px 18px; font-size: 14px", 1)}},
	}})
	if err != nil || !strings.HasPrefix(text(edited), "Saved v2.") {
		t.Fatalf("the edit: %v %s", err, text(edited))
	}

	status, body = member.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID, "", nil)
	var saved struct {
		Latest struct {
			Version int    `json:"version"`
			Note    string `json:"note"`
			Via     struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"via"`
			SavedBy struct {
				Name string `json:"name"`
			} `json:"savedBy"`
		} `json:"latest"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &saved) != nil || saved.Latest.Version != 2 || saved.Latest.Note != "把按钮改小" ||
		saved.Latest.Via.Kind != "claude-code" || saved.Latest.Via.Name != "办公电脑" || saved.Latest.SavedBy.Name != "alice" {
		t.Fatalf("the page after the agent's edit: %d %s", status, body)
	}
	status, file := member.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID+"/versions/2/file", "", nil)
	if status != http.StatusOK || !bytes.Contains(file, []byte("padding: 8px 18px; font-size: 14px")) {
		t.Fatalf("export of v2: %d", status)
	}

	// The administrator takes alice out: her agent is refused at once.
	owner.call(t, http.MethodDelete, "/api/v1/workspace/admin/members/"+alice.ID, "", nil)
	refused, err := http.NewRequest(http.MethodPost, product.URL+"/api/v1/workspace/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	refused.Header.Set("Content-Type", "application/json")
	refused.Header.Set("Accept", "application/json, text/event-stream")
	response, err := (&http.Client{Transport: bearer{token}}).Do(refused)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("a removed member's agent: %d", response.StatusCode)
	}
}

// credentials stands in for the Host Agent's credential provisioning.
type credentials struct{}

func (credentials) SetCredential(context.Context, accounts.CredentialRequest) error { return nil }
func (credentials) DisableCredential(context.Context, string) error                 { return nil }
