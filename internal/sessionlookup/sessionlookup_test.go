package sessionlookup_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

type sessions map[string]accounts.SessionUser

func (s sessions) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	if token == "broken" {
		return accounts.SessionUser{}, errors.New("database is locked")
	}
	user, ok := s[token]
	if !ok {
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
	return user, nil
}

func TestClientResolvesThroughTheSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "sessions.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	alice := accounts.SessionUser{UserID: "user:alice", Username: "alice", Role: accounts.RoleMember}
	server := &http.Server{Handler: sessionlookup.NewHandler(sessions{"alice-token": alice}, nil)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := sessionlookup.NewClient(socket)
	ctx := context.Background()

	if user, err := client.ResolveSessionUser(ctx, "alice-token"); err != nil || user != alice {
		t.Fatalf("ResolveSessionUser() = %+v, %v", user, err)
	}
	if _, err := client.ResolveSessionUser(ctx, "forged"); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("forged token error = %v, want ErrSessionNotFound", err)
	}
	if _, err := client.ResolveSessionUser(ctx, "broken"); err == nil || errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("lookup failure error = %v, want an outage rather than an invalid session", err)
	}
	if _, err := sessionlookup.NewClient(filepath.Join(t.TempDir(), "missing.sock")).ResolveSessionUser(ctx, "alice-token"); err == nil {
		t.Fatalf("an absent Host Agent resolved a session")
	}
}
