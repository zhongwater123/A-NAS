package photoservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	// The administrator is viewing alice's private library (viewer-token).
	r, socket := startWithAssistant(t, accounts.SessionUser{UserID: "user:admin", Username: "admin", Role: accounts.RoleAdmin})
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
	if response := assistantRequest(t, socket, http.MethodGet, "/api/v1/photos/libraries/"+alicePrivate+"/timeline", asAlice); response.StatusCode != http.StatusNotFound {
		t.Fatalf("timeline of the viewed library = %d, want 404", response.StatusCode)
	}
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
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want it out of the assistant's reach", request[0], request[1], response.StatusCode)
		}
	}
	if response := r.get(t, "/api/v1/photos/assets/"+asset.ID, "alice-token"); response.StatusCode != http.StatusOK {
		t.Fatalf("the member's photo after the assistant's attempts: %d", response.StatusCode)
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
