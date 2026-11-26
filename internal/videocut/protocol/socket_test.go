//go:build linux

package protocol

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type uidAccounts map[int]accountid.SessionUser

func (a uidAccounts) ResolveAccountUID(_ context.Context, uid int) (accountid.SessionUser, error) {
	if user, ok := a[uid]; ok {
		return user, nil
	}
	return accountid.SessionUser{}, accountid.ErrUserNotFound
}

// The assistant socket knows the member by the peer's UID alone, and its
// directory must keep everyone else out.
func TestTheAssistantSocketKnowsTheMemberByUID(t *testing.T) {
	dir, err := os.MkdirTemp("", "montage-assistant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assistantSocket(filepath.Join(dir, "assistant.sock")); err == nil {
		t.Fatal("a socket in a directory open to others")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "assistant.sock")
	listener, systemd, err := assistantSocket(path)
	if err != nil || systemd {
		t.Fatalf("assistantSocket() = %v, %v", systemd, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("socket %v, %v", info, err)
	}
	root := filepath.Join(t.TempDir(), "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := app.Open(context.Background(), root, app.Options{Grants: noGrants{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	members := uidAccounts{os.Getuid(): {UserID: "user:alice", Username: "alice"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveAssistant(ctx, listener, authenticatePeer(members, UIDOf, testLogger(), Assistant(store, nil)), testLogger())
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	post := func(route string) (int, string) {
		t.Helper()
		response, err := client.Post("http://montage"+route, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, strings.TrimSpace(string(body))
	}
	if status, body := post("/v1/rounds/claim"); status != http.StatusConflict || !strings.Contains(body, "no_plan_wanted") {
		t.Fatalf("claim with nothing to plan = %d %s", status, body)
	}
	if status, _ := post("/v1/files"); status != http.StatusNotFound {
		t.Fatalf("an unknown route = %d", status)
	}
	// A UID that is no account in use is refused.
	delete(members, os.Getuid())
	if status, _ := post("/v1/rounds/claim"); status != http.StatusUnauthorized {
		t.Fatalf("an unknown UID = %d", status)
	}
}

// Root's planner is heard only for an account it names, which must be in use
// under that name; a member's UID is the member whatever it names.
func TestRootIsHeardOnlyForTheAccountItNames(t *testing.T) {
	resolver := uidAccounts{20101: {UserID: "user:alice", Username: "alice"}, 20102: {UserID: "user:bob", Username: "bob"}}
	// mallory's UID is alice's: the account in use under it is not the one named.
	uids := map[string]int{"alice": 20101, "bob": 20102, "carol": 20103, "mallory": 20101}
	uidOf := func(name string) (int, error) {
		if uid, ok := uids[name]; ok {
			return uid, nil
		}
		return 0, errors.New("no such user")
	}
	seen := ""
	handler := authenticatePeer(resolver, uidOf, testLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := accounts.UserFrom(r.Context())
		seen = user.Username
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func(uid int, credentials bool, named string) int {
		t.Helper()
		seen = ""
		request := httptest.NewRequest(http.MethodPost, "/v1/rounds/claim", strings.NewReader("{}"))
		if named != "" {
			request.Header.Set(AccountHeader, named)
		}
		if credentials {
			request = request.WithContext(context.WithValue(request.Context(), peerUIDKey{}, uid))
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for _, c := range []struct {
		uid         int
		credentials bool
		named, want string
		status      int
	}{
		{0, true, "", "", http.StatusForbidden},
		{0, true, "../alice", "", http.StatusForbidden},
		{0, true, "alice", "alice", http.StatusNoContent},
		{0, true, "carol", "", http.StatusUnauthorized},
		{0, true, "mallory", "", http.StatusUnauthorized},
		{0, true, "nobody", "", http.StatusUnauthorized},
		{20102, true, "alice", "bob", http.StatusNoContent},
		{0, false, "alice", "", http.StatusForbidden},
	} {
		if status := serve(c.uid, c.credentials, c.named); status != c.status || seen != c.want {
			t.Errorf("UID %d (%v) naming %q = %d as %q, want %d as %q", c.uid, c.credentials, c.named, status, seen, c.status, c.want)
		}
	}
}

// noGrants is a photo library these tests never reach: the store only has
// to open.
type noGrants struct{ app.Grants }
