//go:build linux

package shareservice

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
)

// bridgePeer is a tiny wire peer of the file bridge, which isolates the
// supervisor policy from broker filesystem work. Real bridge/Host Agent
// epoch validation is covered by sharefiles integration.
type bridgePeer struct {
	epoch, resets, checks atomic.Int64
}

// startBridge serves a bridgePeer at epoch 1 on path until the test ends.
func startBridge(t *testing.T, path string) *bridgePeer {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	peer := &bridgePeer{}
	peer.epoch.Store(1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				header := make([]byte, 4)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint32(header))
				if _, err := io.ReadFull(conn, body); err != nil {
					return
				}
				var req struct{ Op, Generation string }
				if json.Unmarshal(body, &req) != nil {
					return
				}
				if req.Op == "check" {
					peer.checks.Add(1)
				}
				reply := map[string]any{}
				if req.Op == "reset" {
					peer.resets.Add(1)
					reply["id"] = fmt.Sprint(peer.epoch.Load())
				} else if req.Generation != fmt.Sprint(peer.epoch.Load()) {
					reply["code"] = "generation_changed"
				} else if req.Op == "select" {
					reply["items"] = []files.ShareSource{{ID: "folder", Path: "empty", Kind: files.EntryKindDirectory}}
				} else if req.Op != "check" {
					reply["code"] = "invalid"
				}
				body, _ = json.Marshal(reply)
				binary.BigEndian.PutUint32(header, uint32(len(body)))
				_, _ = conn.Write(header)
				_, _ = conn.Write(body)
			}()
		}
	}()
	return peer
}

type sharingSessions struct{ sessions }

func (sharingSessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) {
	return []accounts.Profile{{ID: "bob", Username: "Bob"}, {ID: "alice", Username: "Alice"}}, nil
}

func TestRestartRejectsCreateUntilBridgeRecovers(t *testing.T) {
	dir, err := os.MkdirTemp("", "shares-restart-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root := filepath.Join(dir, "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(dir, "bridge.sock")
	peer := startBridge(t, bridge)
	socket := filepath.Join(dir, "shares.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// Freeze periodic recovery: only a request can discover the restart.
		done <- Run(ctx, Config{Root: root, SocketPath: socket, BridgeSocket: bridge, Sessions: sharingSessions{}, CheckStore: func(string) error { return nil }, RetryInterval: time.Hour})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("service did not stop")
		}
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://shares"+sharesapi.PathPrefix+path, strings.NewReader(body))
		req.Header.Set(serviceproxy.SessionHeader, "bob-session")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	waitReady := func() {
		t.Helper()
		for range 400 {
			if status, _ := call("GET", "/members", ""); status == 200 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("bridge did not recover without a supervisor tick")
	}
	waitReady()
	peer.epoch.Add(1)
	body := `{"requestId":"restart-create","sourceIds":["folder"],"recipientIds":["alice"]}`
	status, data := call("POST", "", body)
	if status != http.StatusServiceUnavailable {
		if status != http.StatusAccepted {
			t.Fatalf("create = %d %s", status, data)
		}
		// On the old code this captures #362's exact failed/withdrawn symptom.
		var share shares.Share
		_ = json.Unmarshal(data, &share)
		for range 100 {
			_, data = call("GET", "/"+share.ID, "")
			_ = json.Unmarshal(data, &share)
			if share.Status != "preparing" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("create with stale bridge = HTTP %d, share=%s; want 503 before accepting work", status, data)
	}
	if !strings.Contains(string(data), "shares_unavailable") {
		t.Fatalf("error = %s", data)
	}
	waitReady()
	status, data = call("POST", "", body)
	if status != http.StatusAccepted {
		t.Fatalf("retry = %d %s", status, data)
	}
	var share shares.Share
	if err := json.Unmarshal(data, &share); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		_, data = call("GET", "/"+share.ID, "")
		if err := json.Unmarshal(data, &share); err != nil {
			t.Fatal(err)
		}
		if share.Status != "preparing" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if share.Status != "published" {
		t.Fatalf("retry did not publish: %s", data)
	}
	peer.epoch.Add(1)
	status, data = call("POST", "/"+share.ID+"/save", `{"requestId":"restart-save","entryIds":["folder"],"target":{"rootId":"target"}}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("save with stale bridge = %d %s; want 503", status, data)
	}
	waitReady()
	peer.epoch.Add(1)
	status, data = call("GET", "/members", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("members with stale bridge = %d %s; want 503", status, data)
	}
	waitReady()
}

func TestSupervisorRehandshakesWhenBridgeGenerationChanges(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(dir, "bridge.sock")
	peer := startBridge(t, bridge)
	epoch, resets, checks := &peer.epoch, &peer.resets, &peer.checks
	socket := filepath.Join(dir, "shares.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Root: root, SocketPath: socket, BridgeSocket: bridge, Sessions: sessions{}, CheckStore: func(string) error { return nil }, RetryInterval: 10 * time.Millisecond})
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ready := func() bool {
		req, _ := http.NewRequest(http.MethodGet, "http://shares"+sharesapi.PathPrefix, nil)
		req.Header.Set(serviceproxy.SessionHeader, "bob-session")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	wait := func(condition func() bool) {
		t.Helper()
		for range 600 {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("supervisor recovery timed out (resets=%d, checks=%d, epoch=%d)", resets.Load(), checks.Load(), epoch.Load())
	}
	wait(ready)
	epoch.Add(1)
	wait(func() bool { return resets.Load() >= 2 && ready() })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop")
	}
}

// slowSessions holds the first lookup of the token "slow" until release
// closes, and answers it as bob's; the store looks it up again for each
// operation.
type slowSessions struct {
	sessions
	arrived, release chan struct{}
	first            *sync.Once
}

func (s slowSessions) ResolveSessionUser(ctx context.Context, token string) (accounts.SessionUser, error) {
	if token == "slow" {
		s.first.Do(func() {
			close(s.arrived)
			<-s.release
		})
		token = "bob-session"
	}
	return s.sessions.ResolveSessionUser(ctx, token)
}

// Stopping lets a request in progress finish against the open store before
// it closes and interrupts the save jobs, as the Product Service drains its
// requests before closing its stores (#192).
func TestStoppingServesRequestsInProgressBeforeClosingTheStore(t *testing.T) {
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "shares")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root := filepath.Join(dir, "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(dir, "bridge.sock")
	startBridge(t, bridge)
	socket := filepath.Join(dir, "shares.sock")
	slow := slowSessions{arrived: make(chan struct{}), release: make(chan struct{}), first: &sync.Once{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Root: root, SocketPath: socket, BridgeSocket: bridge, Sessions: slow, CheckStore: func(string) error { return nil }, RetryInterval: 10 * time.Millisecond})
	}()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	list := func(token string) (int, error) {
		req, _ := http.NewRequest(http.MethodGet, "http://shares"+sharesapi.PathPrefix, nil)
		req.Header.Set(serviceproxy.SessionHeader, token)
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	waitUntil := func(condition func() bool) {
		t.Helper()
		for range 600 {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("condition not met in time")
	}
	waitUntil(func() bool { status, _ := list("bob-session"); return status == http.StatusOK })

	type answer struct {
		status int
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		status, err := list("slow")
		answered <- answer{status, err}
	}()
	<-slow.arrived
	cancel()
	// Once the service no longer accepts connections, it is stopping.
	waitUntil(func() bool {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	})
	close(slow.release)
	if got := <-answered; got.err != nil || got.status != http.StatusOK {
		t.Fatalf("request in progress while stopping = %d, %v; want 200", got.status, got.err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
