//go:build linux

package shareservice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
)

type sessions struct{}

func (sessions) ResolveSessionUser(ctx context.Context, token string) (accounts.SessionUser, error) {
	switch token {
	case "bob-session":
		return accounts.SessionUser{UserID: "bob", Username: "Bob"}, nil
	case "reset":
		return accounts.SessionUser{UserID: "bob", MustChangePassword: true}, nil
	default:
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
}
func (sessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) {
	return []accounts.Profile{{ID: "bob", Username: "Bob"}}, nil
}
func TestAuthenticationIgnoresProductClaimsAndPreservesVerifiedToken(t *testing.T) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, ok := accounts.UserFrom(r.Context())
		if !ok || user.ID != "bob" || accounts.SessionToken(r.Context()) != "bob-session" {
			t.Errorf("unverified user or token %+v", user)
		}
		w.WriteHeader(204)
	})
	handler := authenticate(sessions{}, nil, next)
	for _, test := range []struct {
		token  string
		status int
	}{{"forged", 401}, {"reset", 403}, {"bob-session", 204}} {
		r := httptest.NewRequest("GET", sharesapi.PathPrefix, nil)
		r.Header.Set(sharesapi.SessionHeader, test.token)
		r = r.WithContext(accounts.WithUser(r.Context(), accounts.User{ID: "admin"}))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("token %s: %d", test.token, w.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("handler calls %d", calls)
	}
}
func TestUnavailableVolumeNeverCreatesFallbackStore(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "offline", "shares")
	socket := filepath.Join(dir, "shares.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Root: root, SocketPath: socket, BridgeSocket: filepath.Join(dir, "missing-bridge"), Sessions: sessions{}, CheckStore: func(string) error { return errors.New("offline") }, RetryInterval: 10 * time.Millisecond})
	}()
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	var response *http.Response
	var err error
	for range 100 {
		req, _ := http.NewRequest("GET", "http://shares"+sharesapi.PathPrefix, nil)
		req.Header.Set(sharesapi.SessionHeader, "bob-session")
		response, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("offline status %d", response.StatusCode)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("fallback store created: %v", err)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
}
