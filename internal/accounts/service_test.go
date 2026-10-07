package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestFirstAdministratorCanBeCreatedAndAuthenticated(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := &credentialRecorder{}
	service := accounts.NewService(store, credentials, accounts.Options{
		Now: func() time.Time { return time.Date(2026, time.October, 7, 10, 0, 0, 0, time.UTC) },
	})

	admin, err := service.SetupAdministrator(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("SetupAdministrator() error = %v", err)
	}
	if got, want := admin.Role, accounts.RoleAdmin; got != want {
		t.Fatalf("role = %q, want %q", got, want)
	}
	if got, want := admin.Status, accounts.UserStatusActive; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := credentials.username, "owner"; got != want {
		t.Fatalf("provisioned username = %q, want %q", got, want)
	}
	if got, want := credentials.password, "correct horse battery staple"; got != want {
		t.Fatalf("provisioned password = %q, want supplied password", got)
	}

	session, err := service.Authenticate(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if session.Token == "" || session.CSRFToken == "" {
		t.Fatalf("session did not contain opaque credentials: %#v", session)
	}
	current, err := service.ResolveSession(context.Background(), session.Token)
	if err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	if got, want := current.User.ID, admin.ID; got != want {
		t.Fatalf("resolved user = %q, want %q", got, want)
	}
}

func TestFirstAdministratorSetupCanRetryAfterCredentialProvisioningFailure(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := &credentialRecorder{failure: errors.New("Samba unavailable")}
	service := accounts.NewService(store, credentials, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err == nil {
		t.Fatal("first setup unexpectedly succeeded")
	}
	credentials.failure = nil
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("retry setup error = %v", err)
	}
	if admin.Status != accounts.UserStatusActive {
		t.Fatalf("admin status = %q", admin.Status)
	}
	if _, err := service.Authenticate(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatalf("recovered administrator cannot authenticate: %v", err)
	}
}

func TestMembersCannotDiscoverAnotherMembersPrivateSpace(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &credentialRecorder{}, accounts.Options{})
	admin, err := service.SetupAdministrator(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("setup administrator: %v", err)
	}
	alice, err := service.CreateMember(context.Background(), admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	bob, err := service.CreateMember(context.Background(), admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}

	spaces, err := service.ListSpaces(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListSpaces() error = %v", err)
	}
	if got, want := len(spaces), 2; got != want {
		t.Fatalf("Alice space count = %d, want %d: %#v", got, want, spaces)
	}
	for _, space := range spaces {
		if space.OwnerUserID == bob.ID {
			t.Fatalf("Alice discovered Bob's private space: %#v", space)
		}
	}
	if allowed, err := service.CanAccessSpace(context.Background(), alice, spaces[0].ID, true); err != nil || !allowed {
		t.Fatalf("Alice cannot write a visible space: allowed=%v err=%v", allowed, err)
	}
}

func TestAdministratorCanResetAndDisableMemberCredential(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := &credentialRecorder{}
	service := accounts.NewService(store, credentials, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := service.Authenticate(ctx, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetCredential(ctx, admin, member.ID, "a newly reset password"); err != nil {
		t.Fatalf("ResetCredential() error = %v", err)
	}
	if credentials.password != "a newly reset password" {
		t.Fatalf("provisioned password = %q", credentials.password)
	}
	if _, err := service.ResolveSession(ctx, oldSession.Token); err != accounts.ErrSessionNotFound {
		t.Fatalf("old session error = %v, want ErrSessionNotFound", err)
	}
	if _, err := service.Authenticate(ctx, "alice", "a newly reset password"); err != nil {
		t.Fatalf("new credential did not authenticate: %v", err)
	}
	if err := service.DisableUser(ctx, admin, member.ID); err != nil {
		t.Fatalf("DisableUser() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, "alice", "a newly reset password"); err != accounts.ErrInvalidCredentials {
		t.Fatalf("disabled authentication error = %v", err)
	}
}

type credentialRecorder struct {
	username string
	password string
	failure  error
}

func (r *credentialRecorder) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	r.username = request.Username
	r.password = request.Password
	return r.failure
}

func (r *credentialRecorder) DisableCredential(context.Context, string) error { return nil }
