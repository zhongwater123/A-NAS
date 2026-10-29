// Package assistantlogin signs in to a member's `hermes serve` with Hermes's
// own password login (dashboard basic auth, ADR 0024 decision 10). The
// instance's root-only secret is the member's password there; serve keeps only
// its scrypt hash. A connection that logged in carries the member's identity,
// which Hermes's browser control requires, and serve refuses its legacy
// session token once this login gate is on.
package assistantlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Host is where `hermes serve` listens inside the sandbox; it refuses a Host
// that is not a loopback address.
const Host = "127.0.0.1:9119"

// accessCookie is the session cookie Hermes's login sets; its value is the
// access token, which serve also takes as a bearer and as the WebSocket's
// ?token=.
const accessCookie = "hermes_session_at"

// ErrRefused reports that serve did not accept the login.
var ErrRefused = errors.New("the assistant refused the login")

// Login posts Hermes's password login over conn, which it uses up, and returns
// the access token and when it expires.
func Login(ctx context.Context, conn net.Conn, username, password string) (string, time.Time, error) {
	body, err := json.Marshal(map[string]string{"provider": "basic", "username": username, "password": password})
	if err != nil {
		return "", time.Time{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+Host+"/auth/password-login", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := OverConn(conn).Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("%w: %d", ErrRefused, response.StatusCode)
	}
	now := time.Now()
	for _, cookie := range response.Cookies() {
		// Behind a path prefix Hermes prefixes the cookie names.
		if !strings.HasSuffix(cookie.Name, accessCookie) || cookie.Value == "" {
			continue
		}
		switch {
		case cookie.MaxAge > 0:
			return cookie.Value, now.Add(time.Duration(cookie.MaxAge) * time.Second), nil
		case !cookie.Expires.IsZero():
			return cookie.Value, cookie.Expires, nil
		default:
			return cookie.Value, now.Add(time.Hour), nil
		}
	}
	return "", time.Time{}, fmt.Errorf("%w: no session in the answer", ErrRefused)
}

// OverConn is an HTTP client whose one request goes over conn.
func OverConn(conn net.Conn) *http.Client {
	used := false
	return &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, errors.New("assistant connection already used")
			}
			used = true
			return conn, nil
		},
		DisableKeepAlives: true,
	}}
}

// Cache keeps one access token per member until shortly before it expires, so
// a member's requests log in once: Hermes limits password attempts per
// address, and every one comes from loopback.
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
	logins  map[string]*sync.Mutex
}

type entry struct {
	token   string
	expires time.Time
}

// refreshBefore is how long before it expires a token is replaced.
const refreshBefore = 5 * time.Minute

// Token returns the member's cached access token, or logs in with login when
// there is none, it is about to expire, or fresh asks for a new one (serve
// refused the last: it restarted and signs with a new key). Concurrent
// callers for one member share one login.
func (c *Cache) Token(ctx context.Context, username string, fresh bool, login func(context.Context) (string, time.Time, error)) (string, error) {
	c.mu.Lock()
	if c.logins == nil {
		c.entries, c.logins = map[string]entry{}, map[string]*sync.Mutex{}
	}
	lock := c.logins[username]
	if lock == nil {
		lock = &sync.Mutex{}
		c.logins[username] = lock
	}
	stale := c.entries[username]
	c.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	c.mu.Lock()
	current := c.entries[username]
	c.mu.Unlock()
	// Another caller may have logged in while this one waited.
	renewed := current.token != "" && current.token != stale.token
	if current.token != "" && time.Until(current.expires) > refreshBefore && (!fresh || renewed) {
		return current.token, nil
	}
	token, expires, err := login(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.entries[username] = entry{token: token, expires: expires}
	c.mu.Unlock()
	return token, nil
}

// Forget drops a member's token, as when their assistant is removed.
func (c *Cache) Forget(username string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, username)
}
