package assistantlogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serveLogin answers Hermes's password login on a Unix socket, as `hermes
// serve` with dashboard.basic_auth does, and returns a dialer to it.
func serveLogin(t *testing.T, cookie *http.Cookie) func() net.Conn {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Provider, Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/auth/password-login" || r.Host != Host || body.Provider != "basic" || body.Username != "alice" || body.Password != "secret" {
			http.Error(w, `{"detail":"Invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		if cookie != nil {
			http.SetCookie(w, cookie)
		}
		_, _ = io.WriteString(w, `{"ok":true,"next":"/"}`)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return func() net.Conn {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
}

func TestLoginReturnsTheAccessTokenAndWhenItExpires(t *testing.T) {
	dial := serveLogin(t, &http.Cookie{Name: "hermes_session_at", Value: "access", MaxAge: 3600})
	token, expires, err := Login(context.Background(), dial(), "alice", "secret")
	if err != nil || token != "access" {
		t.Fatalf("Login() = %q, %v", token, err)
	}
	if left := time.Until(expires); left < 59*time.Minute || left > time.Hour {
		t.Fatalf("expires in %s, want an hour", left)
	}
	if _, _, err := Login(context.Background(), dial(), "alice", "wrong"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Login() with a wrong password = %v, want ErrRefused", err)
	}
}

func TestLoginWithoutASessionCookieIsRefused(t *testing.T) {
	dial := serveLogin(t, nil)
	if _, _, err := Login(context.Background(), dial(), "alice", "secret"); !errors.Is(err, ErrRefused) {
		t.Fatalf("Login() = %v, want ErrRefused", err)
	}
}

func TestCacheLogsInOncePerTokenAndAgainWhenAskedOrExpiring(t *testing.T) {
	var cache Cache
	var logins atomic.Int32
	lifetime := time.Hour
	login := func(context.Context) (string, time.Time, error) {
		n := logins.Add(1)
		return "token-" + string(rune('0'+n)), time.Now().Add(lifetime), nil
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if token, err := cache.Token(ctx, "alice", false, login); err != nil || token != "token-1" {
				t.Errorf("Token() = %q, %v", token, err)
			}
		}()
	}
	wg.Wait()
	if logins.Load() != 1 {
		t.Fatalf("logins = %d, want one for concurrent callers", logins.Load())
	}
	if token, _ := cache.Token(ctx, "alice", true, login); token != "token-2" {
		t.Fatalf("fresh Token() = %q, want a new login", token)
	}
	// A token about to expire is replaced before it is handed out.
	lifetime = time.Minute
	cache.Forget("alice")
	if token, _ := cache.Token(ctx, "alice", false, login); token != "token-3" {
		t.Fatalf("Token() = %q", token)
	}
	if token, _ := cache.Token(ctx, "alice", false, login); token != "token-4" {
		t.Fatalf("Token() near expiry = %q, want a new login", token)
	}
	failed := errors.New("instance down")
	if _, err := cache.Token(ctx, "bob", false, func(context.Context) (string, time.Time, error) { return "", time.Time{}, failed }); !errors.Is(err, failed) {
		t.Fatalf("Token() = %v, want the login's error", err)
	}
}
