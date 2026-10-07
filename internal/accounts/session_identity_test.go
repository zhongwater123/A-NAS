package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestSessionDirectoryResolvesOnlyLiveSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{Now: func() time.Time { return now }})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, admin, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	ownerSession, err := service.Authenticate(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	aliceSession, err := service.Authenticate(ctx, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}

	directory := accounts.NewSessionDirectory(path)
	t.Cleanup(func() { _ = directory.Close() })
	identity, err := directory.ResolveSessionIdentity(ctx, ownerSession.Token)
	if err != nil {
		t.Fatalf("ResolveSessionIdentity(owner) error = %v", err)
	}
	if identity != (accounts.Identity{Username: "owner", UID: 20100, Role: accounts.RoleAdmin, Enabled: true}) {
		t.Fatalf("owner identity = %#v", identity)
	}
	for name, token := range map[string]string{"empty": "", "forged": "not-a-session-token"} {
		if _, err := directory.ResolveSessionIdentity(ctx, token); !errors.Is(err, accounts.ErrSessionNotFound) {
			t.Errorf("%s token error = %v, want ErrSessionNotFound", name, err)
		}
	}
	alice, err := service.UserIDForUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	user, err := directory.ResolveSessionUser(ctx, ownerSession.Token)
	if err != nil || !reflect.DeepEqual(user, accounts.SessionUser{UserID: admin.ID, Username: "owner", Role: accounts.RoleAdmin}) {
		t.Fatalf("ResolveSessionUser(owner) = %#v, %v", user, err)
	}
	grant, err := service.StartLibraryViewing(ctx, admin, alice, "correct horse battery staple", "family album request")
	if err != nil {
		t.Fatalf("StartLibraryViewing() error = %v", err)
	}
	user, err = directory.ResolveSessionUser(ctx, ownerSession.Token)
	if err != nil || len(user.Viewing) != 1 || user.Viewing[0].GrantID != grant.ID || user.Viewing[0].OwnerUserID != alice {
		t.Fatalf("ResolveSessionUser(owner).Viewing = %#v, %v; want the library grant", user.Viewing, err)
	}
	if _, err := directory.ResolveSessionUser(ctx, "not-a-session-token"); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("ResolveSessionUser(forged) error = %v, want ErrSessionNotFound", err)
	}
	if err := service.DisableUser(ctx, admin, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ResolveSessionIdentity(ctx, aliceSession.Token); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("disabled account's session error = %v, want ErrSessionNotFound", err)
	}
	if _, err := directory.ResolveSessionUser(ctx, aliceSession.Token); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("disabled account's session user error = %v, want ErrSessionNotFound", err)
	}

	remembered := service.RememberedSessions(ctx)
	if len(remembered) != 1 || remembered[0].User.Username != "owner" || remembered[0].Token != ownerSession.Token {
		t.Fatalf("remembered sessions = %#v, want only the owner's live session", remembered)
	}
	now = ownerSession.ExpiresAt.Add(time.Second)
	if _, err := directory.ResolveSessionIdentity(ctx, ownerSession.Token); err != nil {
		t.Fatalf("directory clock is independent of the service clock: %v", err)
	}
	if remembered := service.RememberedSessions(ctx); len(remembered) != 0 {
		t.Fatalf("expired sessions are still remembered: %#v", remembered)
	}
}

func TestSessionTokenTravelsInTheContext(t *testing.T) {
	ctx := accounts.WithSessionToken(context.Background(), "token")
	if got := accounts.SessionToken(ctx); got != "token" {
		t.Fatalf("SessionToken() = %q", got)
	}
	if got := accounts.SessionToken(context.Background()); got != "" {
		t.Fatalf("SessionToken() without a token = %q", got)
	}
}
