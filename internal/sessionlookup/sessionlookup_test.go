package sessionlookup_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
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
	server := &http.Server{Handler: sessionlookup.NewHandler(sessions{"alice-token": alice}, nil, nil)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := sessionlookup.NewClient(socket)
	ctx := context.Background()

	if user, err := client.ResolveSessionUser(ctx, "alice-token"); err != nil || !reflect.DeepEqual(user, alice) {
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

// directory also looks accounts up by UID, as accounts.SessionDirectory does.
type directory struct {
	sessions
	uids map[int]accounts.SessionUser
}

func (d directory) ResolveAccountUID(_ context.Context, uid int) (accounts.SessionUser, error) {
	if uid == 29998 {
		return accounts.SessionUser{}, errors.New("database is locked")
	}
	user, ok := d.uids[uid]
	if !ok {
		return accounts.SessionUser{}, accounts.ErrUserNotFound
	}
	return user, nil
}

func serve(t *testing.T, resolver sessionlookup.Resolver, accountResolver sessionlookup.AccountResolver) *sessionlookup.Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "sessions.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: sessionlookup.NewHandler(resolver, accountResolver, nil)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return sessionlookup.NewClient(socket)
}

func TestClientResolvesAccountsByUID(t *testing.T) {
	alice := accounts.SessionUser{UserID: "user:alice", Username: "alice", Role: accounts.RoleMember}
	lookup := directory{sessions: sessions{}, uids: map[int]accounts.SessionUser{20101: alice}}
	client := serve(t, lookup, lookup)
	ctx := context.Background()

	if user, err := client.ResolveAccountUID(ctx, 20101); err != nil || !reflect.DeepEqual(user, alice) {
		t.Fatalf("ResolveAccountUID() = %+v, %v", user, err)
	}
	for _, uid := range []int{20102, 0, -1} {
		if _, err := client.ResolveAccountUID(ctx, uid); err == nil {
			t.Fatalf("ResolveAccountUID(%d) resolved", uid)
		}
	}
	if _, err := client.ResolveAccountUID(ctx, 20102); !errors.Is(err, accounts.ErrUserNotFound) {
		t.Fatalf("unknown UID error = %v, want ErrUserNotFound", err)
	}
	if _, err := client.ResolveAccountUID(ctx, 29998); err == nil || errors.Is(err, accounts.ErrUserNotFound) {
		t.Fatalf("lookup failure error = %v, want an outage rather than an unknown account", err)
	}
}

func TestAccountLookupIsOffUnlessGiven(t *testing.T) {
	// The directory could look accounts up, but the handler was not given it.
	client := serve(t, directory{sessions: sessions{}, uids: map[int]accounts.SessionUser{}}, nil)
	if _, err := client.ResolveAccountUID(context.Background(), 20101); err == nil || errors.Is(err, accounts.ErrUserNotFound) {
		t.Fatalf("error = %v, want the lookup to be unavailable", err)
	}
}

func (s sessions) ActiveProfiles(context.Context) ([]accounts.Profile, error) {
	return []accounts.Profile{{ID: "user:alice", Username: "alice", Nickname: "Alice"}}, nil
}

func TestClientListsRecipientProfilesThroughSocket(t *testing.T) {
	got, err := serve(t, sessions{}, nil).ActiveProfiles(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "user:alice" || got[0].Nickname != "Alice" {
		t.Fatalf("profiles = %+v, %v", got, err)
	}
}
