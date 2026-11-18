//go:build linux

package montageservice

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
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
	store, err := montage.Open(context.Background(), root, montage.Options{Grants: noGrants{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accounts := uidAccounts{os.Getuid(): {UserID: "user:alice", Username: "alice"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveAssistant(ctx, listener, authenticatePeer(accounts, testLogger(), montageapi.Assistant(store, nil)), testLogger())
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
	if status, body := post("/v1/rounds/stale"); status != http.StatusOK || body != `{"rounds":[]}` {
		t.Fatalf("stale = %d %s", status, body)
	}
	if status, _ := post("/v1/rounds/claim"); status != http.StatusConflict {
		t.Fatalf("claim with nothing to plan = %d", status)
	}
	if status, _ := post("/v1/files"); status != http.StatusNotFound {
		t.Fatalf("an unknown route = %d", status)
	}
	// A UID that is no account in use is refused.
	delete(accounts, os.Getuid())
	if status, _ := post("/v1/rounds/stale"); status != http.StatusUnauthorized {
		t.Fatalf("an unknown UID = %d", status)
	}
}
