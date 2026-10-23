package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestSessionDirectoryResolvesOnlyLiveSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	now := time.Now().UTC()
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
	profiles, err := directory.ActiveProfiles(ctx)
	if err != nil || len(profiles) != 2 {
		t.Fatalf("active profiles before disable = %v, %v", profiles, err)
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

	profiles, err = directory.ActiveProfiles(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].ID != admin.ID {
		t.Fatalf("active profiles after disable = %v, %v", profiles, err)
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

func TestSessionDirectoryResolvesActiveAccountsByUID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMember(ctx, admin, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	alice, err := service.UserIDForUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartLibraryViewing(ctx, admin, alice, "correct horse battery staple", "family album request"); err != nil {
		t.Fatal(err)
	}
	aliceSession, err := service.Authenticate(ctx, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	directory := accounts.NewSessionDirectory(path)
	t.Cleanup(func() { _ = directory.Close() })
	aliceIdentity, err := directory.ResolveSessionIdentity(ctx, aliceSession.Token)
	if err != nil {
		t.Fatal(err)
	}

	// The administrator's grant does not travel with a lookup by UID.
	user, err := directory.ResolveAccountUID(ctx, 20100)
	if err != nil || !reflect.DeepEqual(user, accounts.SessionUser{UserID: admin.ID, Username: "owner", Role: accounts.RoleAdmin}) {
		t.Fatalf("ResolveAccountUID(owner) = %#v, %v", user, err)
	}
	user, err = directory.ResolveAccountUID(ctx, aliceIdentity.UID)
	if err != nil || !reflect.DeepEqual(user, accounts.SessionUser{UserID: alice, Username: "alice", Role: accounts.RoleMember}) {
		t.Fatalf("ResolveAccountUID(alice) = %#v, %v", user, err)
	}
	if _, err := directory.ResolveAccountUID(ctx, 29999); !errors.Is(err, accounts.ErrUserNotFound) {
		t.Fatalf("unallocated UID error = %v, want ErrUserNotFound", err)
	}
	if err := service.DisableUser(ctx, admin, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ResolveAccountUID(ctx, aliceIdentity.UID); !errors.Is(err, accounts.ErrUserNotFound) {
		t.Fatalf("disabled account error = %v, want ErrUserNotFound", err)
	}
}

func TestSessionThatMustChangeItsPasswordActsOnNoFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetCredential(ctx, admin, alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(ctx, "alice", "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	directory := accounts.NewSessionDirectory(path)
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := directory.ResolveSessionIdentity(ctx, session.Token); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("ResolveSessionIdentity(must change password) error = %v, want ErrSessionNotFound", err)
	}
	if user, err := directory.ResolveSessionUser(ctx, session.Token); err != nil || !user.MustChangePassword {
		t.Fatalf("ResolveSessionUser(must change password) = %#v, %v; want the restriction for the photo service", user, err)
	}
	if remembered := service.RememberedSessions(ctx); len(remembered) != 0 {
		t.Fatalf("background work may use a session that must change its password: %#v", remembered)
	}

	if err := service.ChangePassword(ctx, session.User, "temporary password 1", "alice chosen password"); err != nil {
		t.Fatal(err)
	}
	if identity, err := directory.ResolveSessionIdentity(ctx, session.Token); err != nil || identity.Username != "alice" {
		t.Fatalf("ResolveSessionIdentity(after change) = %#v, %v", identity, err)
	}
	if remembered := service.RememberedSessions(ctx); len(remembered) != 1 || remembered[0].Token != session.Token {
		t.Fatalf("remembered sessions after change = %#v", remembered)
	}
}

// A lookup that fails, here because its caller gave up, must not take the
// lookups of everyone else sharing the directory down with it (#166): the
// File Broker, terminals, assistants and the photo service all read through
// one handle.
func TestOneFailedSessionLookupLeavesTheOthersWorking(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	directory := accounts.NewSessionDirectory(path)
	t.Cleanup(func() { _ = directory.Close() })
	abandoned, cancel := context.WithCancel(ctx)
	cancel()
	failures := make(chan error, 1)
	var lookups sync.WaitGroup
	for range 8 {
		lookups.Go(func() {
			for range 200 {
				if _, err := directory.ResolveSessionIdentity(ctx, session.Token); err != nil {
					select {
					case failures <- err:
					default:
					}
					return
				}
			}
		})
	}
	for range 200 {
		if _, err := directory.ResolveSessionIdentity(abandoned, session.Token); err == nil || errors.Is(err, accounts.ErrSessionNotFound) {
			t.Fatalf("abandoned lookup error = %v, want the context's error", err)
		}
	}
	lookups.Wait()
	select {
	case err := <-failures:
		t.Fatalf("a live session's lookup failed beside an abandoned one: %v", err)
	default:
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
