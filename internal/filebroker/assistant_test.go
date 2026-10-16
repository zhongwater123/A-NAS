package filebroker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestAssistantConnectionsGoOnlyToTheSessionsOwnMember(t *testing.T) {
	h := newAssistantHarness(t)
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "forged")); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("forged ConnectAssistant() error = %v, want ErrForbidden", err)
	}
	if _, err := h.client.ConnectAssistant(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConnectAssistant() without a session error = %v, want ErrUnavailable", err)
	}
	// The administrator has no assistant here, and asking for one never
	// reaches another member's.
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "admin-token")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("admin ConnectAssistant() error = %v, want ErrNotExist", err)
	}
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"))
	if err != nil {
		t.Fatalf("ConnectAssistant() error = %v", err)
	}
	defer conn.Close()
	if conn.Username != "alice" || conn.Token != "alice-serve-token" {
		t.Fatalf("grant = %q %q, want alice and her token", conn.Username, conn.Token)
	}
	if got := roundTrip(t, conn, "hello"); got != "alice:hello" {
		t.Fatalf("instance answered %q", got)
	}
}

func TestAssistantConnectionIsCutWhenTheSessionEnds(t *testing.T) {
	h := newAssistantHarness(t)
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before")
	h.revoke("member-token")
	// The Product Service's copy stops working too, even if it never closes it.
	_ = conn.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Conn.Read(make([]byte, 16)); err == nil {
		t.Fatal("the connection outlived its session")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the connection was not cut within 5 s of the session ending")
	}
	select {
	case <-h.instanceClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("the instance side did not see the connection end")
	}
}

func TestStoppingTheBrokerCutsAssistantConnections(t *testing.T) {
	h := newAssistantHarness(t)
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before")
	h.server.Close()
	_ = conn.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Conn.Read(make([]byte, 16)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read after the broker stopped: %v, want the connection cut", err)
	}
}

func TestAssistantIsOffWithoutSockets(t *testing.T) {
	h := newAssistantHarness(t, func(config *Config) { config.AssistantSockets = "" })
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConnectAssistant() error = %v, want ErrUnavailable", err)
	}
}

func TestAssistantConnectionCarriesHTTP(t *testing.T) {
	h := newAssistantHarness(t, func(config *Config) {
		// alice's instance answers HTTP, as `hermes serve` does.
		sockets := shortTempDir(t)
		listener, err := net.Listen("unix", filepath.Join(sockets, "alice.sock"))
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"sessions":[],"host":"`+r.Host+`"}`)
		}))
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		config.AssistantSockets = sockets
	})
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"))
	if err != nil {
		t.Fatal(err)
	}
	used := false
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) {
		if used {
			return nil, errors.New("dialled twice")
		}
		used = true
		return conn, nil
	}}}
	response, err := client.Get("http://127.0.0.1:9119/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"host":"127.0.0.1:9119"`) {
		t.Fatalf("response = %d %s", response.StatusCode, body)
	}
}

func roundTrip(t *testing.T, conn net.Conn, line string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(reply)
}

type assistantHarness struct {
	*terminalHarness
	server         *Server
	instanceClosed chan struct{}
}

// newAssistantHarness serves alice's assistant socket as an echo server that
// prefixes her name; the owner, an administrator, has no assistant.
func newAssistantHarness(t *testing.T, adjust ...func(*Config)) *assistantHarness {
	t.Helper()
	h := &assistantHarness{
		terminalHarness: &terminalHarness{identities: map[string]accounts.Identity{
			"admin-token":  {Username: "owner", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
			"member-token": {Username: "alice", UID: 20101, Role: accounts.RoleMember, Enabled: true},
		}},
		instanceClosed: make(chan struct{}, 1),
	}
	sockets, tokens := shortTempDir(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(tokens, "alice.env"), []byte("HERMES_DASHBOARD_SESSION_TOKEN=alice-serve-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, err := net.Listen("unix", filepath.Join(sockets, "alice.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	go func() {
		for {
			conn, err := instance.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { h.instanceClosed <- struct{}{} }()
				defer conn.Close()
				lines := bufio.NewScanner(conn)
				for lines.Scan() {
					_, _ = io.WriteString(conn, "alice:"+lines.Text()+"\n")
				}
			}()
		}
	}()
	config := Config{
		Sessions: h, VolumeRoot: t.TempDir(), VolumeReady: func() bool { return true },
		TerminalRecheck:  50 * time.Millisecond,
		AssistantSockets: sockets, AssistantTokens: tokens,
		skipCredentials: true,
	}
	for _, change := range adjust {
		change(&config)
	}
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	h.server = server
	socket := filepath.Join(shortTempDir(t), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
	})
	h.client = NewClient(socket)
	return h
}

// shortTempDir keeps Unix socket paths under the 108-byte limit that long
// test names would exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "anas-fb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
