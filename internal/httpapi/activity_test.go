package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
	"github.com/zhongwater123/A-NAS/internal/personalization"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

type activityPage struct {
	Items []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Action   string `json:"action"`
		Actor    *struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Nickname string `json:"nickname"`
		} `json:"actor"`
		Subject *struct {
			Username string `json:"username"`
		} `json:"subject"`
		Detail string `json:"detail"`
		Space  *struct {
			Kind  string `json:"kind"`
			Owner *struct {
				Username string `json:"username"`
			} `json:"owner"`
		} `json:"space"`
		Redacted bool `json:"redacted"`
	} `json:"items"`
	Next        string   `json:"next"`
	Scope       string   `json:"scope"`
	Unavailable []string `json:"unavailable"`
}

func (p activityPage) actions() []string {
	var actions []string
	for _, item := range p.Items {
		actions = append(actions, item.Action)
	}
	return actions
}

type activityFixture struct {
	t        *testing.T
	handler  http.Handler
	accounts *accounts.Service
	files    *files.Service
	photos   *photos.Service
	client   *contractClient
}

// newActivityFixture serves the product with the photo API that photoAPI
// returns for the photo library and the account service.
func newActivityFixture(t *testing.T, photoAPI func(*photos.Service, *accounts.Service) http.Handler) *activityFixture {
	t.Helper()
	dir := t.TempDir()
	now := steadyClock()
	store, err := accounts.OpenSQLite(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{Now: now})
	fileStore, err := files.OpenSQLite(filepath.Join(dir, "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileStore.Close() })
	fileService := files.NewLocalService(fileStore, filepath.Join(dir, "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true, Now: now})
	photoService, err := photos.Open(filepath.Join(dir, "photos"), photos.Options{DisableCapacityReserve: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	settings, err := personalization.Open(filepath.Join(dir, "personalization.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Close() })
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: accountService, Files: fileService,
		Photos: photoAPI(photoService, accountService), Personalization: settings, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &activityFixture{t: t, handler: handler, accounts: accountService, files: fileService, photos: photoService,
		client: &contractClient{t: t, handler: handler}}
}

// signIn signs in from a LAN browser, or sets the device up from its own
// screen, checks the exchange against the contract and returns a client
// acting as that user.
func (f *activityFixture) signIn(path, username, password, address string, want int) *contractClient {
	f.t.Helper()
	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path, strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
		request.Header.Set("Content-Type", "application/json")
		if path == "/api/v1/setup/admin" {
			fromLocalConsole(request)
		} else {
			request.Header.Set("X-Forwarded-For", address)
		}
		return request
	}
	request := newRequest()
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != want {
		f.t.Fatalf("sign in %s status = %d, want %d; body=%s", username, recorder.Code, want, recorder.Body.String())
	}
	openapitest.Check(f.t, newRequest(), recorder)
	if want >= 300 {
		return nil
	}
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &session)
	client := *f.client
	client.cookie, client.csrf, client.forwardedFor = recorder.Result().Cookies()[0], session.CSRFToken, request.Header.Get("X-Forwarded-For")
	return &client
}

func (c *contractClient) activity(query string) activityPage {
	c.t.Helper()
	response := c.do(http.MethodGet, "/api/v1/activity"+query, "", nil, http.StatusOK)
	var page activityPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		c.t.Fatal(err)
	}
	return page
}

func privateSpace(t *testing.T, service *accounts.Service, user accounts.User) accounts.Space {
	t.Helper()
	spaces, err := service.ListSpaces(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate && space.OwnerUserID == user.ID {
			return space
		}
	}
	t.Fatalf("no private space for %s", user.Username)
	return accounts.Space{}
}

func sharedSpace(t *testing.T, service *accounts.Service, user accounts.User) accounts.Space {
	t.Helper()
	spaces, err := service.ListSpaces(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			return space
		}
	}
	t.Fatal("no shared space")
	return accounts.Space{}
}

func TestActivityLogShowsEveryoneToAdministratorsWithoutPrivateNames(t *testing.T) {
	ctx := context.Background()
	f := newActivityFixture(t, func(service *photos.Service, accountService *accounts.Service) http.Handler {
		return localPhotos(t, service, serviceSessions{accountService})
	})
	asAdmin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	session, err := f.accounts.ResolveSession(ctx, asAdmin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	admin := session.User
	alice, err := f.accounts.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	f.signIn("/api/v1/session", "alice", "not alice's password", "172.18.45.66", http.StatusUnauthorized)
	asAlice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	report, err := f.files.Upload(ctx, alice, privateSpace(t, f.accounts, alice).ID, "", "体检报告.pdf", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.Upload(ctx, alice, sharedSpace(t, f.accounts, alice).ID, "", "家庭旅行.pdf", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.Upload(ctx, admin, privateSpace(t, f.accounts, admin).ID, "", "体检记录.xlsx", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	importPhoto(t, f.photos, photos.Principal{UserID: alice.ID}, "海边.png")
	importPhoto(t, f.photos, photos.Principal{UserID: admin.ID, Admin: true}, "全家福.png")

	all := asAdmin.activity("")
	if all.Scope != "all" {
		t.Fatalf("scope = %q", all.Scope)
	}
	byDetail := map[string]bool{}
	var sawFailure, sawAliceSignIn, sawRedacted, sawOwnPhoto bool
	for _, item := range all.Items {
		byDetail[item.Detail] = true
		switch {
		case item.Action == "session.sign_in_failed":
			sawFailure = item.Actor != nil && item.Actor.Username == "alice" && strings.Contains(item.Detail, `"reason":"invalid_password"`) &&
				strings.Contains(item.Detail, `"address":"172.18.45.66"`)
		case item.Action == "session.signed_in" && item.Actor != nil && item.Actor.Username == "alice":
			sawAliceSignIn = strings.Contains(item.Detail, `"via":"lan"`) && item.Subject != nil && item.Subject.Username == "alice"
		case item.Action == "file.uploaded" && item.Redacted:
			sawRedacted = item.Detail == "" && item.Space != nil && item.Space.Kind == "private" && item.Space.Owner.Username == "alice"
		case item.Action == "photo.imported":
			sawOwnPhoto = item.Detail == "全家福.png"
		}
	}
	if !sawFailure || !sawAliceSignIn || !sawRedacted || !sawOwnPhoto {
		t.Fatalf("failure/sign-in/redacted/own photo = %v/%v/%v/%v; items = %+v", sawFailure, sawAliceSignIn, sawRedacted, sawOwnPhoto, all.Items)
	}
	for _, hidden := range []string{"体检报告.pdf", "海边.png"} {
		if byDetail[hidden] {
			t.Fatalf("administrator sees %s", hidden)
		}
	}
	if !byDetail["家庭旅行.pdf"] || !byDetail["体检记录.xlsx"] {
		t.Fatalf("shared and own names missing: %v", byDetail)
	}
	// Neither the name nor the ID of alice's private file reaches the
	// administrator anywhere in the response, and searching for the ID finds
	// nothing.
	if body := asAdmin.do(http.MethodGet, "/api/v1/activity", "", nil, http.StatusOK).Body.String(); strings.Contains(body, "体检报告") || strings.Contains(body, report.ID) {
		t.Fatalf("administrator's activity reveals alice's private file %s: %s", report.ID, body)
	}
	if found := asAdmin.activity("?q=" + url.QueryEscape(report.ID)); len(found.Items) != 0 {
		t.Fatalf("search for alice's private file ID = %+v", found.Items)
	}
	// The unredacted audit export is gone (#163).
	auditRequest := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	auditRequest.AddCookie(asAdmin.cookie)
	audit := httptest.NewRecorder()
	f.handler.ServeHTTP(audit, auditRequest)
	if audit.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/audit status = %d, want 404; body=%s", audit.Code, audit.Body.String())
	}
	if found := asAdmin.activity("?q=" + url.QueryEscape("体检")); len(found.Items) != 1 || found.Items[0].Detail != "体检记录.xlsx" {
		t.Fatalf("search for 体检 = %+v", found.Items)
	}
	if byName := asAdmin.activity("?category=session&q=alice"); strings.Join(byName.actions(), ",") != "session.signed_in,session.sign_in_failed" {
		t.Fatalf("alice's sign-in events = %v", byName.actions())
	}

	// Alice asks for the administrator's events and still gets her own.
	mine := asAlice.activity("?actor=" + url.QueryEscape(admin.ID))
	if mine.Scope != "self" {
		t.Fatalf("member scope = %q", mine.Scope)
	}
	if got, want := strings.Join(mine.actions(), ","), "photo.imported,file.uploaded,file.uploaded,session.signed_in,session.sign_in_failed,user.created"; got != want {
		t.Fatalf("alice's activity = %s, want %s", got, want)
	}
	for _, item := range mine.Items {
		if item.Redacted || (item.Actor != nil && item.Actor.Username == "owner" && item.Action != "user.created") {
			t.Fatalf("alice sees %+v", item)
		}
	}

	// Pages of two add up to the whole log, in the same order.
	var paged []string
	query := "?limit=2"
	for range 20 {
		page := asAdmin.activity(query)
		for _, item := range page.Items {
			paged = append(paged, item.ID)
		}
		if page.Next == "" {
			break
		}
		query = "?limit=2&before=" + url.QueryEscape(page.Next)
	}
	var whole []string
	for _, item := range all.Items {
		whole = append(whole, item.ID)
	}
	if strings.Join(paged, ",") != strings.Join(whole, ",") {
		t.Fatalf("paged = %v, whole = %v", paged, whole)
	}

	for _, bad := range []string{"?category=kernel", "?actor=user:nobody", "?before=forged"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/activity"+bad, nil)
		request.AddCookie(asAdmin.cookie)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET activity%s status = %d, want 400", bad, recorder.Code)
		}
	}
	asAlice.do(http.MethodDelete, "/api/v1/session", "", nil, http.StatusNoContent)
	if signedOut := asAdmin.activity("?category=session&limit=1"); signedOut.Items[0].Action != "session.signed_out" || signedOut.Items[0].Actor.Username != "alice" {
		t.Fatalf("latest session event = %+v", signedOut.Items)
	}
}

func TestActivityLogReportsAnUnavailablePhotoLibrary(t *testing.T) {
	f := newActivityFixture(t, func(*photos.Service, *accounts.Service) http.Handler { return photosapi.New(nil, nil) })
	asAdmin := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "172.18.45.10", http.StatusCreated)
	page := asAdmin.activity("")
	if len(page.Unavailable) != 1 || page.Unavailable[0] != "photo" {
		t.Fatalf("unavailable = %v", page.Unavailable)
	}
	if got := strings.Join(page.actions(), ","); got != "session.signed_in,user.created" {
		t.Fatalf("actions = %v", got)
	}
	if page := asAdmin.activity("?category=file"); len(page.Unavailable) != 0 {
		t.Fatalf("file activity reports %v unavailable", page.Unavailable)
	}
}

func importPhoto(t *testing.T, service *photos.Service, principal photos.Principal, name string) {
	t.Helper()
	libraries, err := service.Libraries(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, library := range libraries {
		if library.Kind == photos.LibraryKindPrivate {
			private = library.ID
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 2, len(name)))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(context.Background(), principal, photos.ImportRequest{LibraryID: private, Name: name, Content: &encoded}); err != nil {
		t.Fatal(err)
	}
}

// steadyClock is shared by every module of a fixture and moves a second on
// each reading, so the merged log has one order no matter how fast the
// events were made.
func steadyClock() func() time.Time {
	var mu sync.Mutex
	now := time.Date(2026, time.October, 12, 8, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
}
