package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
	"github.com/zhongwater123/A-NAS/internal/workspace"
	"github.com/zhongwater123/A-NAS/internal/workspaceapi"
	"github.com/zhongwater123/A-NAS/internal/workspaceservice"
)

type workspaceFixture struct {
	handler http.Handler
	users   *accounts.Service
	admin   accounts.User
	alice   accounts.User
	owner   signedIn
	member  signedIn
	bob     signedIn
}

// newWorkspaceFixture serves the workspace in this process as development
// does: the service's own authentication over control.db read-only.
func newWorkspaceFixture(t *testing.T) workspaceFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	controlDB := filepath.Join(dir, "control.db")
	store, err := accounts.OpenSQLite(controlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{PasswordCost: accounts.PasswordCost{MemoryKiB: 64, Iterations: 1, Parallelism: 1}})
	admin, err := users.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := users.CreateMember(ctx, admin, "alice", "member password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateMember(ctx, admin, "bob", "member password for testing"); err != nil {
		t.Fatal(err)
	}
	pages, err := workspace.Open(t.TempDir(), workspace.Options{Admits: func(string, int64) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pages.Close() })
	directory := sessiondir.New(controlDB)
	t.Cleanup(func() { _ = directory.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := workspaceapi.New(pages, workspaceapi.Options{Logger: logger, Directory: directory})
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users, Logger: logger,
		Workspace: serviceproxy.Local(workspaceservice.Authenticate(directory, logger, api)),
	})
	return workspaceFixture{
		handler: handler, users: users, admin: admin, alice: alice,
		owner:  signIn(t, handler, "owner", "correct horse battery staple"),
		member: signIn(t, handler, "alice", "member password for testing"),
		bob:    signIn(t, handler, "bob", "member password for testing"),
	}
}

func sendPage(s signedIn, method, path string, page []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(page))
	request.AddCookie(s.cookie)
	request.Header.Set("X-CSRF-Token", s.csrf)
	request.Header.Set("Content-Type", "text/html")
	response := httptest.NewRecorder()
	s.handler.ServeHTTP(response, request)
	return response
}

func samplePage(t *testing.T) []byte {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "workspace", "testdata", "prd.html"))
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func smallJPEG(t *testing.T) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func TestTheWorkspaceKeepsToTheContract(t *testing.T) {
	f := newWorkspaceFixture(t)

	if got := f.member.call(t, http.MethodGet, "/api/v1/workspace", ""); got.Code != http.StatusNotFound {
		t.Fatalf("a member nobody added: %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodGet, "/api/v1/workspace/pages", ""); got.Code != http.StatusForbidden || answerCode(got) != "workspace_forbidden" {
		t.Fatalf("a member nobody added lists pages: %d %s", got.Code, got.Body.String())
	}
	if got := f.owner.call(t, http.MethodGet, "/api/v1/workspace", ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"admin":true`) {
		t.Fatalf("an administrator: %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodGet, "/api/v1/workspace/admin/members", ""); got.Code != http.StatusForbidden {
		t.Fatalf("a member lists the members: %d", got.Code)
	}
	for _, id := range []string{f.alice.ID} {
		if got := f.owner.call(t, http.MethodPut, "/api/v1/workspace/admin/members/"+id, ""); got.Code != http.StatusNoContent {
			t.Fatalf("add a member: %d %s", got.Code, got.Body.String())
		}
	}
	members := f.owner.call(t, http.MethodGet, "/api/v1/workspace/admin/members", "")
	if members.Code != http.StatusOK || !strings.Contains(members.Body.String(), `"username":"alice"`) || strings.Count(members.Body.String(), `"member":true`) != 1 {
		t.Fatalf("members = %d %s", members.Code, members.Body.String())
	}

	created := sendPage(f.member, http.MethodPost, "/api/v1/workspace/pages?name=prd.html", samplePage(t))
	if created.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", created.Code, created.Body.String())
	}
	var page workspace.Page
	_ = json.Unmarshal(created.Body.Bytes(), &page)
	if page.Title != "家庭相册分享 PRD" || page.Latest.Version != 1 {
		t.Fatalf("page = %+v", page)
	}
	for _, path := range []string{"/api/v1/workspace/pages", "/api/v1/workspace/pages/" + page.ID} {
		if got := f.member.call(t, http.MethodGet, path, ""); got.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := f.owner.call(t, http.MethodPatch, "/api/v1/workspace/pages/"+page.ID, `{"title":"相册分享"}`); got.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", got.Code, got.Body.String())
	}

	view := f.member.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID+"/versions/latest/view?highlight=12,40&scroll=300", "")
	policy := view.Header().Get("Content-Security-Policy")
	if view.Code != http.StatusOK || !strings.HasPrefix(policy, "sandbox allow-scripts;") || !strings.Contains(policy, "connect-src data: blob:") ||
		strings.Contains(policy, "allow-same-origin") || view.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("view = %d, policy %q", view.Code, policy)
	}
	if !strings.Contains(view.Body.String(), `<script src="/assets/workspace-frame.js" data-page="`+page.ID+`" data-highlight="12,40" data-scroll="300"></script>`) {
		t.Fatalf("the view has no helper: %.300s", view.Body.String())
	}
	file := f.member.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID+"/versions/1/file", "")
	if file.Code != http.StatusOK || !bytes.Equal(file.Body.Bytes(), samplePage(t)) || !strings.HasPrefix(file.Header().Get("Content-Disposition"), "attachment;") ||
		!strings.HasPrefix(file.Header().Get("Content-Security-Policy"), "sandbox;") {
		t.Fatalf("export = %d %v", file.Code, file.Header())
	}

	saved := sendPage(f.member, http.MethodPost, "/api/v1/workspace/pages/"+page.ID+"/versions?baseVersion=1", []byte("<title>v2</title>"))
	if saved.Code != http.StatusCreated {
		t.Fatalf("a new version = %d %s", saved.Code, saved.Body.String())
	}
	stale := sendPage(f.member, http.MethodPost, "/api/v1/workspace/pages/"+page.ID+"/versions?baseVersion=1", []byte("<title>v3</title>"))
	if stale.Code != http.StatusConflict || answerCode(stale) != "page_changed" || !strings.Contains(stale.Body.String(), `"version":2`) {
		t.Fatalf("a version over an old one = %d %s", stale.Code, stale.Body.String())
	}

	if got := f.member.call(t, http.MethodPut, "/api/v1/workspace/viewing", `{"pageId":"`+page.ID+`","version":2}`); got.Code != http.StatusBadRequest {
		t.Fatalf("viewing without a tab = %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodPut, "/api/v1/workspace/viewing", `{"pageId":"`+page.ID+`","version":2,"tab":"tab-1","focused":true}`); got.Code != http.StatusNoContent {
		t.Fatalf("viewing = %d %s", got.Code, got.Body.String())
	}
	// Everyone who may use the workspace sees who has the page open.
	if got := f.owner.call(t, http.MethodGet, "/api/v1/workspace/pages/"+page.ID, ""); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), `"presence":{"viewers":[{"member":{"id":"`+f.alice.ID+`"`) {
		t.Fatalf("the page's presence = %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodDelete, "/api/v1/workspace/viewing/tab-1", ""); got.Code != http.StatusNoContent {
		t.Fatalf("closing the tab = %d %s", got.Code, got.Body.String())
	}
	if got := f.owner.call(t, http.MethodGet, "/api/v1/workspace/pages", ""); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), `"presence":{"viewers":[],"agents":[]}`) {
		t.Fatalf("the list after the tab closed = %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodPut, "/api/v1/workspace/viewing", `{"pageId":"`+page.ID+`","version":2,"tab":"tab-1","focused":true}`); got.Code != http.StatusNoContent {
		t.Fatalf("viewing again = %d %s", got.Code, got.Body.String())
	}
	selection := `{"pageId":"` + page.ID + `","version":2,"at":0,"generated":false,"tag":"title","text":"v2","dom":"","rect":{"x":0,"y":0,"w":1,"h":1},"style":{},"rules":[],"image":"` + smallJPEG(t) + `"}`
	if got := f.member.call(t, http.MethodPut, "/api/v1/workspace/selection", selection); got.Code != http.StatusNoContent {
		t.Fatalf("selection = %d %s", got.Code, got.Body.String())
	}
	notJPEG := strings.Replace(selection, smallJPEG(t), "data:image/png;base64,iVBORw0KGgo=", 1)
	if got := f.member.call(t, http.MethodPut, "/api/v1/workspace/selection", notJPEG); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a selection with a PNG = %d %s", got.Code, got.Body.String())
	}
	if got := f.member.call(t, http.MethodDelete, "/api/v1/workspace/selection", ""); got.Code != http.StatusNoContent {
		t.Fatalf("clear selection = %d", got.Code)
	}

	if got := f.owner.call(t, http.MethodPut, "/api/v1/workspace/admin/members/"+f.bobID(t), ""); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if got := f.bob.call(t, http.MethodDelete, "/api/v1/workspace/pages/"+page.ID, ""); got.Code != http.StatusForbidden {
		t.Fatalf("another member deletes the page: %d %s", got.Code, got.Body.String())
	}
	activity := f.member.call(t, http.MethodGet, "/api/v1/activity?category=workspace", "")
	if activity.Code != http.StatusOK || !strings.Contains(activity.Body.String(), "workspace.page_created") || !strings.Contains(activity.Body.String(), "workspace.page_saved") {
		t.Fatalf("activity = %d %s", activity.Code, activity.Body.String())
	}
	if got := f.member.call(t, http.MethodDelete, "/api/v1/workspace/pages/"+page.ID, ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", got.Code, got.Body.String())
	}
	if got := f.owner.call(t, http.MethodDelete, "/api/v1/workspace/admin/members/"+f.alice.ID, ""); got.Code != http.StatusNoContent {
		t.Fatalf("remove a member = %d", got.Code)
	}
}

func (f workspaceFixture) bobID(t *testing.T) string {
	t.Helper()
	id, err := f.users.UserIDForUsername(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// agentRequest is an MCP call as an agent's HTTP client sends it.
func agentRequest(target, token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestOnlyAnAgentTokenReachesTheMCPEndpoint(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceFixture(t)
	if got := f.owner.call(t, http.MethodPut, "/api/v1/workspace/admin/members/"+f.alice.ID, ""); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	_, token, err := f.users.CreateAgentToken(ctx, f.alice, "laptop", accounts.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	const target = "http://127.0.0.1:8080/api/v1/workspace/mcp"
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		return response
	}

	fromBrowser := agentRequest(target, token)
	fromBrowser.Header.Set("Origin", "http://127.0.0.1:8080")
	if got := serve(fromBrowser); got.Code != http.StatusForbidden {
		t.Fatalf("a browser's request = %d %s", got.Code, got.Body.String())
	}
	rebound := agentRequest("http://evil.example/api/v1/workspace/mcp", token)
	if got := serve(rebound); got.Code != http.StatusForbidden {
		t.Fatalf("a request for another host = %d %s", got.Code, got.Body.String())
	}
	withCookie := agentRequest(target, "")
	withCookie.AddCookie(f.member.cookie)
	withCookie.Header.Set("X-CSRF-Token", f.member.csrf)
	if got := serve(withCookie); got.Code != http.StatusUnauthorized || got.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("a session cookie instead of a token = %d %v", got.Code, got.Header())
	}
	if got := serve(agentRequest(target, token)); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"result"`) {
		t.Fatalf("an agent's ping = %d %s", got.Code, got.Body.String())
	}

	// A client on the LAN that keeps presenting bad tokens waits.
	forged := accountid.AgentTokenPrefix + strings.Repeat("0", 64)
	var last *httptest.ResponseRecorder
	for range 6 {
		request := agentRequest(target, forged)
		request.Header.Set("X-Forwarded-For", "192.168.1.77")
		last = serve(request)
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Fatalf("the sixth bad token = %d %s", last.Code, last.Body.String())
	}

	if got := f.owner.call(t, http.MethodDelete, "/api/v1/workspace/admin/members/"+f.alice.ID, ""); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	if got := serve(agentRequest(target, token)); got.Code != http.StatusForbidden || answerCode(got) != "workspace_forbidden" {
		t.Fatalf("a removed member's agent = %d %s", got.Code, got.Body.String())
	}
}

var curlLink = regexp.MustCompile(`"(http://[^"]+/api/v1/workspace/transfers/[^"]+)"`)

// An agent rewrites a whole page through one-time links: it downloads the
// page, uploads its rewrite, and neither link works twice.
func TestAnAgentMovesWholePagesThroughOneTimeLinks(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceFixture(t)
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	if got := f.owner.call(t, http.MethodPut, "/api/v1/workspace/admin/members/"+f.alice.ID, ""); got.Code != http.StatusNoContent {
		t.Fatal(got.Body.String())
	}
	created := sendPage(f.member, http.MethodPost, "/api/v1/workspace/pages?name=prd.html", samplePage(t))
	var page workspace.Page
	_ = json.Unmarshal(created.Body.Bytes(), &page)
	_, token, err := f.users.CreateAgentToken(ctx, f.alice, "laptop", accounts.AgentClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/api/v1/workspace/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	link := func(tool string, arguments map[string]any) string {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		match := curlLink.FindStringSubmatch(text)
		if match == nil || !strings.HasPrefix(match[1], server.URL) {
			t.Fatalf("%s gave no link at the agent's address: %s", tool, text)
		}
		return match[1]
	}

	download := link("workspace_checkout", map[string]any{"page": page.ID})
	if head, err := http.Head(download); err != nil || head.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD on a download link = %v %v", head, err)
	}
	response, err := http.Get(download)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, samplePage(t)) {
		t.Fatalf("download = %d", response.StatusCode)
	}
	if again, _ := http.Get(download); again.StatusCode != http.StatusNotFound {
		t.Fatalf("a download link used twice = %d", again.StatusCode)
	}

	upload := link("workspace_upload", map[string]any{"page": page.ID, "base_version": 1, "note": "整页换成新版式"})
	put := func(url string, content []byte) (int, string) {
		request, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(content))
		request.Header.Set("Content-Type", "text/html")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		answer, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(answer)
	}
	if status, answer := put(upload, []byte("<title>新版式</title>")); status != http.StatusCreated || !strings.Contains(answer, `"version":2`) {
		t.Fatalf("upload = %d %s", status, answer)
	}
	stale := link("workspace_upload", map[string]any{"page": page.ID, "base_version": 1, "note": "又一版"})
	if status, answer := put(stale, []byte("<title>又一版</title>")); status != http.StatusConflict || !strings.Contains(answer, "page_changed") {
		t.Fatalf("an upload over an old version = %d %s", status, answer)
	}
	fresh := link("workspace_upload", map[string]any{"note": "新页面"})
	if status, answer := put(fresh, []byte("<title>新页面</title>")); status != http.StatusCreated || !strings.Contains(answer, `"title":"新页面"`) {
		t.Fatalf("a new page = %d %s", status, answer)
	}

	// A link outlives nothing: once the token is revoked, it is refused.
	last := link("workspace_checkout", map[string]any{"page": page.ID})
	tokens, _ := f.users.AgentTokens(ctx, f.alice)
	if err := f.users.RevokeAgentToken(ctx, f.alice, tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	if revoked, _ := http.Get(last); revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a link of a revoked token = %d", revoked.StatusCode)
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}
