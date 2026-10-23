package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/shares"
)

func TestMemberSharingProductWorkflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := accounts.OpenSQLite(filepath.Join(root, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	users := accounts.NewService(db, &developmentOperations{}, accounts.Options{})
	owner, err := users.SetupAdministrator(ctx, "owner", "owner strong password")
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.CreateMember(ctx, owner, "member", "initial member password")
	if err != nil {
		t.Fatal(err)
	}
	if err = users.ChangePassword(ctx, member, "initial member password", "member changed password"); err != nil {
		t.Fatal(err)
	}
	// httptest requests are direct loopback connections, where these sessions work.
	direct := accounts.WithClientOrigin(ctx, accounts.ClientOrigin{Via: accounts.ViaLoopback})
	ownerSession, err := users.Authenticate(direct, "owner", "owner strong password")
	if err != nil {
		t.Fatal(err)
	}
	memberSession, err := users.Authenticate(direct, "member", "member changed password")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := files.OpenSQLite(filepath.Join(root, "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	volume := filepath.Join(root, "volume")
	fs := files.NewLocalService(catalog, volume, users, files.Options{AllowUnverifiedVolume: true, DisableCapacityReserve: true})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api, closeShares, err := configuredShares(ctx, Config{Mode: ModeFake, DataMount: volume}, users, fs, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer closeShares()
	handler := httpapi.NewProduct(httpapi.ProductDependencies{Accounts: users, Files: fs, Shares: api, Logger: logger})
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacyrouter.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	call := func(session accounts.Session, method, url string, body any) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = strings.NewReader(string(b))
		}
		request := httptest.NewRequest(method, "http://127.0.0.1:8080"+url, reader)
		request.AddCookie(&http.Cookie{Name: "anas_session", Value: session.Token})
		request.Header.Set("X-CSRF-Token", session.CSRFToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			route, params, err := router.FindRoute(request)
			if err != nil {
				t.Fatal(err)
			}
			input := &openapi3filter.ResponseValidationInput{RequestValidationInput: &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route}, Status: response.Code, Header: response.Header()}
			input.SetBodyBytes(response.Body.Bytes())
			if err := openapi3filter.ValidateResponse(ctx, input); err != nil {
				t.Fatalf("%s %s contract: %v", method, url, err)
			}
		}
		return response
	}
	spaceFor := func(user accounts.User) string {
		list, err := users.ListSpaces(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Kind == accounts.SpaceKindPrivate {
				return s.ID
			}
		}
		t.Fatal("private space missing")
		return ""
	}
	sourceSpace, targetSpace := spaceFor(owner), spaceFor(member)
	source, err := fs.Upload(accounts.WithSessionToken(ctx, ownerSession.Token), owner, sourceSpace, "", "hello.txt", strings.NewReader("immutable hello"))
	if err != nil {
		t.Fatal(err)
	}
	created := call(ownerSession, "POST", "/api/v1/shares", shares.CreateRequest{RequestID: "workflow-create", SourceIDs: []string{source.ID}, RecipientIDs: []string{member.ID}, Message: "请查看 👩🏽‍💻"})
	if created.Code != 202 {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var share shares.Share
	if err = json.Unmarshal(created.Body.Bytes(), &share); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/shares/" + share.ID
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := call(ownerSession, "GET", base, nil)
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		_ = json.Unmarshal(response.Body.Bytes(), &share)
		if share.Status != "preparing" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if share.Status != "published" || len(share.Entries) != 1 {
		t.Fatalf("share %+v", share)
	}
	// Changing the sender's source after publication cannot change the share copy.
	if _, err = fs.Move(accounts.WithSessionToken(ctx, ownerSession.Token), owner, source.ID, sourceSpace, "", "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = fs.Upload(accounts.WithSessionToken(ctx, ownerSession.Token), owner, sourceSpace, "", "hello.txt", strings.NewReader("changed original")); err != nil {
		t.Fatal(err)
	}
	content := call(memberSession, "GET", base+"/entries/"+share.Entries[0].ID+"/content", nil)
	if content.Code != 200 || content.Body.String() != "immutable hello" {
		t.Fatalf("independent content %d %s", content.Code, content.Body.String())
	}
	notifications := call(memberSession, "GET", "/api/v1/shares/notifications", nil)
	if notifications.Code != 200 || !strings.Contains(notifications.Body.String(), share.ID) {
		t.Fatalf("notification %s", notifications.Body.String())
	}
	viewed := call(memberSession, "POST", base+"/view", nil)
	if viewed.Code != 200 {
		t.Fatal(viewed.Body.String())
	}
	reply := call(memberSession, "POST", base+"/messages", map[string]any{"recipientId": member.ID, "requestId": "reply-1", "text": "收到 👍"})
	if reply.Code != 201 {
		t.Fatal(reply.Body.String())
	}
	conversation := call(ownerSession, "GET", base+"/messages?recipientId="+member.ID, nil)
	if conversation.Code != 200 || !strings.Contains(conversation.Body.String(), "收到 👍") {
		t.Fatalf("private reply %s", conversation.Body.String())
	}
	for _, requestID := range []string{"save-one", "save-two"} {
		response := call(memberSession, "POST", base+"/save", shares.SaveRequest{RequestID: requestID, EntryIDs: []string{share.Entries[0].ID}, Target: shares.Target{RootID: targetSpace}})
		if response.Code != 202 {
			t.Fatalf("save %d %s", response.Code, response.Body.String())
		}
		var job shares.Job
		_ = json.Unmarshal(response.Body.Bytes(), &job)
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			response = call(memberSession, "GET", "/api/v1/shares/jobs/"+job.ID, nil)
			_ = json.Unmarshal(response.Body.Bytes(), &job)
			if job.Status != "running" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if job.Status != "completed" {
			t.Fatalf("save job %+v", job)
		}
	}
	entries, err := fs.List(accounts.WithSessionToken(ctx, memberSession.Token), member, targetSpace, "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("saved renamed entries %+v %v", entries, err)
	}
	if entries[0].Name == entries[1].Name {
		t.Fatal("duplicate save overwrote destination")
	}
	sent := call(ownerSession, "GET", base, nil)
	if strings.Contains(sent.Body.String(), targetSpace) || !strings.Contains(sent.Body.String(), `"savedFiles":1`) {
		t.Fatalf("save status/privacy %s", sent.Body.String())
	}
	if revoked := call(ownerSession, "POST", base+"/revoke", nil); revoked.Code != 204 {
		t.Fatal(revoked.Body.String())
	}
	if expired := call(memberSession, "GET", base+"/entries/"+share.Entries[0].ID+"/content", nil); expired.Code != 410 {
		t.Fatalf("revoked content %d", expired.Code)
	}
	history := call(memberSession, "GET", base+"/messages?recipientId="+member.ID, nil)
	if history.Code != 200 || !strings.Contains(history.Body.String(), "收到 👍") {
		t.Fatal("revoke lost conversation history")
	}
}
