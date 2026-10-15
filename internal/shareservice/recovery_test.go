//go:build linux

package shareservice

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A tiny wire peer isolates the supervisor policy from broker filesystem work.
// Real bridge/Host Agent epoch validation is covered by sharefiles integration.
func TestSupervisorRehandshakesWhenBridgeGenerationChanges(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(dir, "bridge.sock")
	listener, err := net.Listen("unix", bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var epoch, resets, checks atomic.Int64
	epoch.Store(1)
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
					checks.Add(1)
				}
				reply := map[string]string{}
				if req.Op == "reset" {
					resets.Add(1)
					reply["id"] = fmt.Sprint(epoch.Load())
				} else if req.Op != "check" || req.Generation != fmt.Sprint(epoch.Load()) {
					reply["code"] = "inactive"
				}
				body, _ = json.Marshal(reply)
				binary.BigEndian.PutUint32(header, uint32(len(body)))
				_, _ = conn.Write(header)
				_, _ = conn.Write(body)
			}()
		}
	}()
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
		req, _ := http.NewRequest("GET", "http://shares"+sharesapi.PathPrefix, nil)
		req.Header.Set(sharesapi.SessionHeader, "bob-session")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
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
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop")
	}
}
