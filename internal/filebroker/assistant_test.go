package filebroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestAssistantConnectionsGoOnlyToTheSessionsOwnMember(t *testing.T) {
	h := newAssistantHarness(t)
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "forged"), false); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("forged ConnectAssistant() error = %v, want ErrForbidden", err)
	}
	if _, err := h.client.ConnectAssistant(context.Background(), false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConnectAssistant() without a session error = %v, want ErrUnavailable", err)
	}
	// The administrator has no assistant here, and asking for one never
	// reaches another member's.
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "admin-token"), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("admin ConnectAssistant() error = %v, want ErrNotExist", err)
	}
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false)
	if err != nil {
		t.Fatalf("ConnectAssistant() error = %v", err)
	}
	defer conn.Close()
	// The broker logged in with her instance's secret; the Product Service gets
	// only the access token.
	if conn.Username != "alice" || conn.Token != "alice-access-1" {
		t.Fatalf("grant = %q %q, want alice and an access token", conn.Username, conn.Token)
	}
	if got := roundTrip(t, conn, "hello"); got != "alice:hello" {
		t.Fatalf("instance answered %q", got)
	}
}

func TestAssistantConnectionIsCutWhenTheSessionEnds(t *testing.T) {
	h := newAssistantHarness(t)
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false)
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

// Like a terminal, an assistant connection is not cut by a session directory
// that cannot be read for a moment (#166).
func TestAssistantConnectionOutlivesAPassingSessionDirectoryError(t *testing.T) {
	h := newAssistantHarness(t)
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before")
	h.setUnreadable(errors.New("database is locked"))
	time.Sleep(120 * time.Millisecond)
	h.setUnreadable(nil)
	if reply := roundTrip(t, conn, "after"); reply != "alice:after" {
		t.Fatalf("reply after the passing error = %q", reply)
	}
}

func TestStoppingTheBrokerCutsAssistantConnections(t *testing.T) {
	h := newAssistantHarness(t)
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false)
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
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false); !errors.Is(err, ErrUnavailable) {
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
			if r.URL.Path == "/auth/password-login" {
				http.SetCookie(w, &http.Cookie{Name: "hermes_session_at", Value: "access", MaxAge: 3600})
				_, _ = io.WriteString(w, `{"ok":true}`)
				return
			}
			_, _ = io.WriteString(w, `{"sessions":[],"host":"`+r.Host+`","auth":"`+r.Header.Get("Authorization")+`"}`)
		}))
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		config.AssistantSockets = sockets
	})
	conn, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false)
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
	request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9119/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+conn.Token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"host":"127.0.0.1:9119"`) || !strings.Contains(string(body), `"auth":"Bearer access"`) {
		t.Fatalf("response = %d %s", response.StatusCode, body)
	}
}

func TestAssistantLoginIsKeptUntilTheInstanceRefusesIt(t *testing.T) {
	h := newAssistantHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "member-token")
	for range 3 {
		conn, err := h.client.ConnectAssistant(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if conn.Token != "alice-access-1" {
			t.Fatalf("token = %q, want the first login's", conn.Token)
		}
		_ = conn.Close()
	}
	if got := h.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 for three connections", got)
	}
	// The Product Service asks for a fresh login when the instance refused the
	// token (it restarted and signs with a new key).
	conn, err := h.client.ConnectAssistant(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Token != "alice-access-2" || h.logins.Load() != 2 {
		t.Fatalf("fresh token = %q after %d logins", conn.Token, h.logins.Load())
	}
}

func TestAssistantWithAWrongSecretIsUnavailable(t *testing.T) {
	h := newAssistantHarness(t)
	if err := os.WriteFile(filepath.Join(h.tokens, "alice.env"), []byte("HERMES_DASHBOARD_SESSION_TOKEN=not-hers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.ConnectAssistant(accounts.WithSessionToken(context.Background(), "member-token"), false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConnectAssistant() error = %v, want ErrUnavailable", err)
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
	tokens         string
	logins         atomic.Int32
}

// newAssistantHarness serves alice's assistant socket as an echo server that
// prefixes her name, after answering Hermes's password login with an access
// token numbered by login; the owner, an administrator, has no assistant.
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
	h.tokens = tokens
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
				defer conn.Close()
				reader := bufio.NewReader(conn)
				first, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.HasPrefix(first, "POST /auth/password-login ") {
					h.login(conn, reader)
					return
				}
				defer func() { h.instanceClosed <- struct{}{} }()
				_, _ = io.WriteString(conn, "alice:"+strings.TrimSpace(first)+"\n")
				lines := bufio.NewScanner(reader)
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

// login answers Hermes's password login: alice with her instance's secret
// gets an access token numbered by login.
func (h *assistantHarness) login(conn net.Conn, reader *bufio.Reader) {
	length := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return
	}
	var login struct{ Provider, Username, Password string }
	_ = json.Unmarshal(body, &login)
	if login.Provider != "basic" || login.Username != "alice" || login.Password != "alice-serve-token" {
		_, _ = io.WriteString(conn, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	n := h.logins.Add(1)
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nSet-Cookie: hermes_session_at=alice-access-%d; Max-Age=43200; Path=/\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"ok\":true}", n)
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
