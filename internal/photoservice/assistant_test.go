package photoservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
)

// uidAccounts is the Host Agent's lookup of active accounts by Linux UID.
type uidAccounts map[int]accounts.SessionUser

func (a uidAccounts) ResolveAccountUID(_ context.Context, uid int) (accounts.SessionUser, error) {
	if user, ok := a[uid]; ok {
		if user.UserID == "outage" {
			return accounts.SessionUser{}, errors.New("host agent unreachable")
		}
		return user, nil
	}
	return accounts.SessionUser{}, accounts.ErrUserNotFound
}

// startWithAssistant runs the service with its assistant socket. The test
// process connects to it, so its own UID stands for the member whose
// assistant calls.
func startWithAssistant(t *testing.T, member accounts.SessionUser) (running, string) {
	t.Helper()
	var socket string
	accountsByUID := uidAccounts{}
	if member.UserID != "" {
		accountsByUID[os.Getuid()] = member
	}
	r := start(t, func(c *photoservice.Config) {
		socket = filepath.Join(filepath.Dir(c.SocketPath), "assistant.sock")
		c.AssistantSocketPath, c.Accounts = socket, accountsByUID
	})
	r.ready.Store(true)
	waitFor(t, func() bool { return r.get(t, "/api/v1/photos/libraries", "alice-token").StatusCode == http.StatusOK })
	return r, socket
}

func assistantRequest(t *testing.T, socket, method, path string, header http.Header) *http.Response {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	request, _ := http.NewRequest(method, "http://photos"+path, strings.NewReader(""))
	for key, values := range header {
		request.Header[key] = values
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func decodeItems[T any](t *testing.T, response *http.Response) []T {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var list struct {
		Items []T `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func sharedLibrary(t *testing.T, r running, token string) string {
	t.Helper()
	for _, lib := range decodeItems[photos.Library](t, r.get(t, "/api/v1/photos/libraries", token)) {
		if lib.Kind == photos.LibraryKindShared {
			return lib.ID
		}
	}
	t.Fatal("no shared library")
	return ""
}

func TestAssistantIsTheMemberThePeerUIDNames(t *testing.T) {
	// The administrator is viewing alice's private library (viewer-token), and even
	// if the account lookup handed the grant on, the assistant would not use it.
	r, socket := startWithAssistant(t, accounts.SessionUser{UserID: "user:admin", Username: "admin", Role: accounts.RoleAdmin,
		Viewing: []accounts.LibraryViewing{{GrantID: "viewing:1", OwnerUserID: "user:alice", ExpiresAt: time.Now().Add(time.Hour)}}})
	alicePrivate := r.privateLibrary(t, "alice-token")
	adminPrivate := r.privateLibrary(t, "viewer-token")
	shared := sharedLibrary(t, r, "alice-token")
	for token, upload := range map[string][2]string{
		"alice-token":  {alicePrivate, "beach-alice.png"},
		"viewer-token": {adminPrivate, "beach-admin.png"},
		"bob-token":    {shared, "beach-shared.png"},
	} {
		if recorder := r.upload(t, token, upload[0], upload[1]); recorder.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", upload[1], recorder.Code, recorder.Body)
		}
	}
	// A session token sent along changes nothing: the peer UID decides.
	asAlice := http.Header{photosapi.SessionHeader: {"alice-token"}}

	var ids []string
	for _, lib := range decodeItems[photos.Library](t, assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/libraries", asAlice)) {
		if lib.Viewing != nil {
			t.Fatalf("assistant lists %+v through a viewing grant", lib)
		}
		ids = append(ids, lib.ID)
	}
	if want := []string{adminPrivate, shared}; !slices.Equal(ids, want) {
		t.Fatalf("libraries = %v, want the administrator's own and the shared one %v", ids, want)
	}
	var names []string
	for _, asset := range decodeItems[photos.Asset](t, assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/search?q=beach", nil)) {
		names = append(names, asset.Name)
	}
	slices.Sort(names)
	if want := []string{"beach-admin.png", "beach-shared.png"}; !slices.Equal(names, want) {
		t.Fatalf("search = %v, want %v", names, want)
	}
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/search?q=beach&viewing="+alicePrivate, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("search in the viewed library = %d, want 404", response.StatusCode)
	}
	// Every read that takes a library or a viewed library stays within the scope.
	var album photos.Album
	recorder := r.upload(t, "alice-token", alicePrivate, "beach-alice-2.png")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload: %d", recorder.Code)
	}
	var aliceAsset photos.Asset
	_ = json.NewDecoder(recorder.Body).Decode(&aliceAsset)
	if err := sessionJSON(t, r, "alice-token", http.MethodPost, "/api/v1/photos/libraries/"+alicePrivate+"/albums", `{"name":"Trip"}`, &album); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/photos/libraries/" + alicePrivate + "/timeline",
		"/api/v1/photos/libraries/" + alicePrivate + "/timeline/months",
		"/api/v1/photos/libraries/" + alicePrivate + "/albums",
		"/api/v1/photos/albums/" + album.ID + "/assets",
		"/api/v1/photos/assets/" + aliceAsset.ID,
		"/api/v1/photos/assets/" + aliceAsset.ID + "/similar",
		"/api/v1/photos/tags?viewing=" + alicePrivate,
		"/api/v1/photos/tags/x/assets?viewing=" + alicePrivate,
		"/api/v1/photos/search?q=beach&library=" + alicePrivate,
		"/api/v1/photos/assets/" + sharedAsset(t, socket) + "/similar?viewing=" + alicePrivate,
	} {
		if response := assistantRequest(t, socket, http.MethodGet, path, asAlice); response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.StatusCode)
		}
	}
}

// sharedAsset returns the ID of a photo in the shared library, as the assistant sees it.
func sharedAsset(t *testing.T, socket string) string {
	t.Helper()
	for _, asset := range decodeItems[photos.Asset](t, assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/search?q=beach-shared", nil)) {
		return asset.ID
	}
	t.Fatal("no shared photo")
	return ""
}

// sessionJSON sends one JSON request through the Product Service's proxy.
func sessionJSON(t *testing.T, r running, token, method, path, body string, out any) error {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(accounts.WithSessionToken(request.Context(), token))
	recorder := httptest.NewRecorder()
	photosapi.NewProxy(r.socket, nil).ServeHTTP(recorder, request)
	if recorder.Code >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, recorder.Code, recorder.Body)
	}
	return json.NewDecoder(recorder.Body).Decode(out)
}

func TestAssistantSocketServesOnlyTheToolsReads(t *testing.T) {
	r, socket := startWithAssistant(t, accounts.SessionUser{UserID: "user:alice", Username: "alice", Role: accounts.RoleMember})
	alicePrivate := r.privateLibrary(t, "alice-token")
	recorder := r.upload(t, "alice-token", alicePrivate, "sea.png")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", recorder.Code, recorder.Body)
	}
	var asset photos.Asset
	if err := json.NewDecoder(recorder.Body).Decode(&asset); err != nil {
		t.Fatal(err)
	}

	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/assets/"+asset.ID, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("details = %d, want 200", response.StatusCode)
	}
	// Its pixels leave only as the view the assistant's model looks at.
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/view", nil); response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("view = %d %s, want a JPEG", response.StatusCode, response.Header.Get("Content-Type"))
	}
	// And what its original records about it.
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/metadata", nil); response.StatusCode != http.StatusOK {
		t.Fatalf("metadata = %d, want 200", response.StatusCode)
	}
	for _, request := range [][2]string{
		{http.MethodGet, "/api/v1/photos/assets/" + asset.ID + "/thumbnail"},
		{http.MethodGet, "/api/v1/photos/assets/" + asset.ID + "/original"},
		{http.MethodGet, photosapi.AIStatusPath},
		{http.MethodGet, photosapi.ActivityPath},
		{http.MethodGet, "/api/v1/photos/libraries/" + alicePrivate + "/trash"},
		{http.MethodPost, "/api/v1/photos/libraries/" + alicePrivate + "/uploads"},
		{http.MethodPost, "/api/v1/photos/image-searches"},
		{http.MethodPatch, "/api/v1/photos/assets/" + asset.ID},
		{http.MethodDelete, "/api/v1/photos/assets/" + asset.ID},
		{http.MethodPost, "/api/v1/photos/assets/" + asset.ID + "/tags"},
	} {
		response := assistantRequest(t, socket, request[0], request[1], nil)
		if response.StatusCode != http.StatusNotFound || errorCode(t, response) != "not_found" {
			t.Errorf("%s %s = %d, want 404 not_found", request[0], request[1], response.StatusCode)
		}
	}
	if response := r.get(t, "/api/v1/photos/assets/"+asset.ID, "alice-token"); response.StatusCode != http.StatusOK {
		t.Fatalf("the member's photo after the assistant's attempts: %d", response.StatusCode)
	}
}

func TestAssistantSocketNeedsAPrivateDirectory(t *testing.T) {
	dir, err := os.MkdirTemp("", "photos")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err = photoservice.Run(context.Background(), photoservice.Config{
		Root: filepath.Join(dir, "store"), SocketPath: filepath.Join(dir, "photos.sock"), Sessions: knownSessions,
		AssistantSocketPath: filepath.Join(dir, "assistant.sock"), Accounts: uidAccounts{},
	})
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("Run() with a shared directory error = %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "assistant.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the assistant socket was created: %v", err)
	}
}

func TestAssistantNeedsAnActiveAccountForItsUID(t *testing.T) {
	_, socket := startWithAssistant(t, accounts.SessionUser{})
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/libraries", http.Header{photosapi.SessionHeader: {"alice-token"}}); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown UID = %d, want 401", response.StatusCode)
	}

	_, socket = startWithAssistant(t, accounts.SessionUser{UserID: "outage"})
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/libraries", nil); response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("lookup outage = %d, want 503", response.StatusCode)
	}

	_, socket = startWithAssistant(t, accounts.SessionUser{UserID: "user:carol", Username: "carol", MustChangePassword: true})
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/libraries", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("member who must change their password = %d, want 403", response.StatusCode)
	}
}
