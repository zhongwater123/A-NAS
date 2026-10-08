package photoservice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
)

type sessions map[string]accounts.SessionUser

func (s sessions) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	if token == "outage" {
		return accounts.SessionUser{}, errors.New("host agent unreachable")
	}
	if user, ok := s[token]; ok {
		return user, nil
	}
	return accounts.SessionUser{}, accounts.ErrSessionNotFound
}

var knownSessions = sessions{
	"alice-token": {UserID: "user:alice", Username: "alice", Role: accounts.RoleMember},
	"bob-token":   {UserID: "user:bob", Username: "bob", Role: accounts.RoleMember},
	"reset-token": {UserID: "user:carol", Username: "carol", Role: accounts.RoleMember, MustChangePassword: true},
	"viewer-token": {UserID: "user:admin", Username: "admin", Role: accounts.RoleAdmin, Viewing: []accounts.LibraryViewing{
		{GrantID: "viewing:1", OwnerUserID: "user:alice", ExpiresAt: time.Now().Add(time.Hour)},
	}},
}

type running struct {
	socket string
	root   string
	ready  *atomic.Bool
	done   chan error
	cancel context.CancelFunc
}

func start(t *testing.T) running {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "photos")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r := running{socket: filepath.Join(dir, "photos.sock"), root: filepath.Join(dir, "store"), ready: &atomic.Bool{}, done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		r.done <- photoservice.Run(ctx, photoservice.Config{
			Root: r.root, SocketPath: r.socket, Sessions: knownSessions, RetryInterval: 10 * time.Millisecond,
			CheckStore: func(string) error {
				if !r.ready.Load() {
					return photoservice.ErrStoreUnavailable
				}
				return nil
			},
			Photos: photos.Options{DisableCapacityReserve: true},
		})
	}()
	t.Cleanup(func() { cancel(); <-r.done })
	waitFor(t, func() bool { _, err := os.Stat(r.socket); return err == nil })
	return r
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r running) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", r.socket)
	}}}
	request, _ := http.NewRequest(http.MethodGet, "http://photos"+path, nil)
	if token != "" {
		request.Header.Set(photosapi.SessionHeader, token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func errorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return body.Error.Code
}

func TestServesOnceTheStoreIsReadyAndConfirmsEverySession(t *testing.T) {
	r := start(t)
	if response := r.get(t, "/api/v1/photos/libraries", "alice-token"); response.StatusCode != http.StatusServiceUnavailable || errorCode(t, response) != "photos_unavailable" {
		t.Fatalf("before the store is ready: %d", response.StatusCode)
	}
	if _, err := os.Stat(r.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unavailable store was created")
	}
	r.ready.Store(true)
	waitFor(t, func() bool { return r.get(t, "/api/v1/photos/libraries", "alice-token").StatusCode == http.StatusOK })

	for token, want := range map[string]int{
		"":            http.StatusUnauthorized,
		"forged":      http.StatusUnauthorized,
		"reset-token": http.StatusForbidden,
		"outage":      http.StatusServiceUnavailable,
	} {
		if response := r.get(t, "/api/v1/photos/libraries", token); response.StatusCode != want {
			t.Errorf("token %q status = %d, want %d", token, response.StatusCode, want)
		}
	}

	r.cancel()
	if err := <-r.done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	r.done <- nil
	if _, err := os.Stat(r.socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind after shutdown")
	}
}

func TestProductProxyForwardsOnlyTheVerifiedSessionToken(t *testing.T) {
	r := start(t)
	r.ready.Store(true)
	waitFor(t, func() bool { return r.get(t, "/api/v1/photos/libraries", "alice-token").StatusCode == http.StatusOK })
	proxy := photosapi.NewProxy(r.socket, nil)

	call := func(method, path, contentType string, body []byte, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		// A browser cannot choose who it is by sending the header itself.
		request.Header.Set(photosapi.SessionHeader, "alice-token")
		request.AddCookie(&http.Cookie{Name: "anas_session", Value: "cookie-secret"})
		request = request.WithContext(accounts.WithSessionToken(request.Context(), token))
		recorder := httptest.NewRecorder()
		proxy.ServeHTTP(recorder, request)
		return recorder
	}

	libraries := call(http.MethodGet, "/api/v1/photos/libraries", "", nil, "bob-token")
	var list struct {
		Items []photos.Library `json:"items"`
	}
	if err := json.Unmarshal(libraries.Body.Bytes(), &list); err != nil || libraries.Code != http.StatusOK {
		t.Fatalf("libraries = %d %s", libraries.Code, libraries.Body.String())
	}
	var private photos.Library
	for _, lib := range list.Items {
		if lib.Kind == photos.LibraryKindPrivate {
			private = lib
		}
	}
	if private.OwnerUserID != "user:bob" {
		t.Fatalf("private library owner = %q, want the session's user", private.OwnerUserID)
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "through-proxy.png")
	_, _ = part.Write(encoded.Bytes())
	_ = writer.Close()
	upload := call(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/uploads", writer.FormDataContentType(), body.Bytes(), "bob-token")
	if upload.Code != http.StatusCreated || !strings.Contains(upload.Body.String(), `"uploadedBy":"user:bob"`) {
		t.Fatalf("upload through proxy = %d %s", upload.Code, upload.Body.String())
	}

	unreachable := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/photos/libraries", nil)
	photosapi.NewProxy(filepath.Join(t.TempDir(), "absent.sock"), nil).ServeHTTP(unreachable, request)
	if unreachable.Code != http.StatusServiceUnavailable || !strings.Contains(unreachable.Body.String(), "photos_unavailable") {
		t.Fatalf("unreachable photo service = %d %s", unreachable.Code, unreachable.Body.String())
	}
}

func TestRequireOwnedStoreRejectsAnythingButTheVolumeStore(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "photos")
	if err := photoservice.RequireOwnedStore(missing); !errors.Is(err, photoservice.ErrStoreUnavailable) {
		t.Fatalf("missing store error = %v", err)
	}
	if err := os.Mkdir(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := photoservice.RequireOwnedStore(missing); !errors.Is(err, photoservice.ErrStoreUnavailable) {
		t.Fatalf("shared-mode store error = %v", err)
	}
	if err := os.Chmod(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	// A private directory on the system disk is still not the data volume.
	if err := photoservice.RequireOwnedStore(missing); !errors.Is(err, photoservice.ErrStoreUnavailable) {
		t.Fatalf("store outside Btrfs error = %v", err)
	}
}

func TestAdministratorSeesOnlyTheGrantedLibraryReadOnly(t *testing.T) {
	r := start(t)
	r.ready.Store(true)
	waitFor(t, func() bool { return r.get(t, "/api/v1/photos/libraries", "alice-token").StatusCode == http.StatusOK })
	_ = r.get(t, "/api/v1/photos/libraries", "bob-token")

	var list struct {
		Items []photos.Library `json:"items"`
	}
	if err := json.NewDecoder(r.get(t, "/api/v1/photos/libraries", "viewer-token").Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var viewed []photos.Library
	for _, lib := range list.Items {
		if lib.Viewing != nil {
			viewed = append(viewed, lib)
		}
	}
	if len(viewed) != 1 || viewed[0].OwnerUserID != "user:alice" || viewed[0].OwnerName != "alice" || viewed[0].Viewing.GrantID != "viewing:1" {
		t.Fatalf("viewed libraries = %+v", viewed)
	}
}

// privateLibrary returns the caller's private library ID, or "" while the
// photo API is unavailable.
func (r running) privateLibrary(t *testing.T, token string) string {
	t.Helper()
	response := r.get(t, "/api/v1/photos/libraries", token)
	if response.StatusCode != http.StatusOK {
		return ""
	}
	var list struct {
		Items []photos.Library `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, lib := range list.Items {
		if lib.Kind == photos.LibraryKindPrivate && lib.Viewing == nil {
			return lib.ID
		}
	}
	return ""
}

func TestClosesTheStoreWhileItIsLostAndReopensIt(t *testing.T) {
	r := start(t)
	r.ready.Store(true)
	var library string
	waitFor(t, func() bool { library = r.privateLibrary(t, "alice-token"); return library != "" })

	// The volume goes offline: requests stop reaching the Catalog.
	r.ready.Store(false)
	waitFor(t, func() bool {
		response := r.get(t, "/api/v1/photos/libraries", "alice-token")
		return response.StatusCode == http.StatusServiceUnavailable && errorCode(t, response) == "photos_unavailable"
	})

	// It returns: the same Catalog is opened again.
	r.ready.Store(true)
	var reopened string
	waitFor(t, func() bool { reopened = r.privateLibrary(t, "alice-token"); return reopened != "" })
	if reopened != library {
		t.Fatalf("library after the store returned = %s, want %s", reopened, library)
	}
}

func TestFollowsTheStorePathWhenItNamesAnotherDirectory(t *testing.T) {
	r := start(t)
	r.ready.Store(true)
	var library string
	waitFor(t, func() bool { library = r.privateLibrary(t, "alice-token"); return library != "" })

	// The volume is remounted elsewhere and another one takes its place: the
	// open Catalog must not keep serving from the old directory.
	if err := os.Rename(r.root, r.root+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.root, 0o700); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		current := r.privateLibrary(t, "alice-token")
		return current != "" && current != library
	})
	if _, err := os.Stat(filepath.Join(r.root, "catalog.db")); err != nil {
		t.Fatalf("the new store was not opened: %v", err)
	}
}
