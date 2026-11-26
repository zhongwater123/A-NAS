//go:build linux

package videocut

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type uidAccounts map[int]accountid.SessionUser

func (a uidAccounts) ResolveAccountUID(_ context.Context, uid int) (accountid.SessionUser, error) {
	if user, ok := a[uid]; ok {
		return user, nil
	}
	return accountid.SessionUser{}, accountid.ErrUserNotFound
}

// The assistant socket outlives every store: it answers from the store that
// is open and unavailable while none is, and a store is not closed under a
// call it is answering.
func TestTheAssistantSocketFollowsTheStore(t *testing.T) {
	dir, err := os.MkdirTemp("", "montage-assistant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root := filepath.Join(dir, "montage")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path, markers := filepath.Join(dir, "assistant.sock"), filepath.Join(dir, "run", "plans")
	var usable atomic.Bool
	usable.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Root: root, SocketPath: filepath.Join(dir, "montage.sock"), Sessions: sessions{}, Grants: noGrants{},
			Accounts: uidAccounts{os.Getuid(): {UserID: "user:alice", Username: "alice"}}, AssistantSocket: path, PlanMarkers: markers,
			Logger: testLogger(), RetryInterval: 20 * time.Millisecond,
			CheckStore: func(string) error {
				if usable.Load() {
					return nil
				}
				return ErrStoreUnavailable
			}})
	}()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}
	defer stop()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}}
	waitFor := func(want int) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			response, err := client.Post("http://montage/v1/rounds/claim", "application/json", strings.NewReader("{}"))
			if err == nil {
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode == want {
					return strings.TrimSpace(string(body))
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("the assistant socket did not answer %d (%v)", want, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if body := waitFor(http.StatusConflict); !strings.Contains(body, "no_plan_wanted") {
		t.Fatalf("claim = %s", body)
	}
	if info, err := os.Stat(markers); err != nil || !info.IsDir() {
		t.Fatalf("the planning markers' directory: %v", err)
	}
	usable.Store(false)
	if body := waitFor(http.StatusServiceUnavailable); !strings.Contains(body, protocol.Unavailable) {
		t.Fatalf("without a store = %s", body)
	}
	usable.Store(true)
	waitFor(http.StatusConflict)
	// The service stops with the socket's server: nothing is left listening.
	stop()
	if _, err := client.Post("http://montage/v1/rounds/claim", "application/json", strings.NewReader("{}")); err == nil {
		t.Fatal("the assistant socket still answers after the service stopped")
	}
}
