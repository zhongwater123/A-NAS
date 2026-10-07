package filebroker

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/terminal"
)

func TestTerminalShellRunsThroughTheBrokerForAdministratorsOnly(t *testing.T) {
	broker := newTerminalHarness(t, "echo ready; read line; echo got:$line")
	if _, err := broker.client.StartShell(accounts.WithSessionToken(context.Background(), "member-token"), 80, 24); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("member StartShell() error = %v, want ErrForbidden", err)
	}
	if _, err := broker.client.StartShell(accounts.WithSessionToken(context.Background(), "forged"), 80, 24); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("forged StartShell() error = %v, want ErrForbidden", err)
	}
	shell, err := broker.client.StartShell(accounts.WithSessionToken(context.Background(), "admin-token"), 100, 30)
	if err != nil {
		t.Fatalf("StartShell() error = %v", err)
	}
	defer shell.Close()
	output := bufio.NewReader(shell)
	waitForLine(t, output, "ready")
	if err := shell.Resize(120, 40); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if _, err := shell.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	waitForLine(t, output, "got:hello")
	select {
	case <-shell.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shell exit was not reported")
	}
	if code := shell.ExitCode(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestClosingATerminalHangsTheShellUp(t *testing.T) {
	broker := newTerminalHarness(t, "trap '' HUP; echo ready; sleep 60")
	shell, err := broker.client.StartShell(accounts.WithSessionToken(context.Background(), "admin-token"), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitForLine(t, bufio.NewReader(shell), "ready")
	started := time.Now()
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shell.Done():
	default:
		t.Fatal("Close() returned before the broker reported the exit")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("a shell ignoring SIGHUP took %s to stop", elapsed)
	}
}

func TestTerminalEndsWhenTheSessionStopsBeingValid(t *testing.T) {
	broker := newTerminalHarness(t, "echo ready; sleep 60")
	shell, err := broker.client.StartShell(accounts.WithSessionToken(context.Background(), "admin-token"), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	waitForLine(t, bufio.NewReader(shell), "ready")
	broker.revoke("admin-token")
	select {
	case <-shell.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the shell outlived its session")
	}
}

func waitForLine(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, want) {
			return
		}
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
	}
	t.Fatalf("timed out waiting for %q", want)
}

type terminalHarness struct {
	client     *Client
	mu         sync.Mutex
	identities map[string]accounts.Identity
}

func (h *terminalHarness) ResolveSessionIdentity(_ context.Context, token string) (accounts.Identity, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	identity, ok := h.identities[token]
	if !ok {
		return accounts.Identity{}, accounts.ErrSessionNotFound
	}
	return identity, nil
}

func (h *terminalHarness) revoke(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.identities, token)
}

func newTerminalHarness(t *testing.T, script string) *terminalHarness {
	t.Helper()
	h := &terminalHarness{identities: map[string]accounts.Identity{
		"admin-token":  {Username: "owner", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
		"member-token": {Username: "alice", UID: 20101, Role: accounts.RoleMember, Enabled: true},
	}}
	volume := t.TempDir()
	server, err := NewServer(Config{
		Sessions: h, VolumeRoot: volume, VolumeReady: func() bool { return true },
		ShellCommand:    func() *exec.Cmd { return exec.Command("/bin/sh", "-c", script) },
		LookupUID:       func(string) (int, error) { return 20100, nil },
		TerminalRecheck: 50 * time.Millisecond,
		skipCredentials: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
		_ = os.Remove(socket)
	})
	h.client = NewClient(socket)
	return h
}

var _ terminal.Spawner = (*Client)(nil)
