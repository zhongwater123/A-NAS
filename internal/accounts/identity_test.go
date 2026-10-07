package accounts_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestAccountsReceiveNeverReusedUIDsInTheReservedRange(t *testing.T) {
	ctx := context.Background()
	service, credentials := newIdentityService(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	credentials.conflicts = map[string]bool{"root": true}
	if _, err := service.CreateMember(ctx, admin, "root", "a password for testing"); !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("CreateMember(root) error = %v, want ErrIdentityConflict", err)
	}
	if _, err := service.CreateMember(ctx, admin, "alice", "alice password for testing"); err != nil {
		t.Fatalf("CreateMember(alice) error = %v", err)
	}

	if got, want := credentials.uids, []int{20100, 20101, 20102}; !slices.Equal(got, want) {
		t.Fatalf("provisioned UIDs = %v, want %v (the conflicting UID must not be reused)", got, want)
	}
	users, err := service.ListUsers(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.Username == "root" {
			t.Fatalf("conflicting account was kept: %#v", user)
		}
	}
	if _, err := service.Authenticate(ctx, "root", "a password for testing"); err == nil {
		t.Fatal("conflicting account can sign in")
	}
}

func TestFirstAdministratorCanChooseAnotherNameAfterAConflict(t *testing.T) {
	ctx := context.Background()
	service, credentials := newIdentityService(t, filepath.Join(t.TempDir(), "control.db"))
	credentials.conflicts = map[string]bool{"anas-dev": true}
	if _, err := service.SetupAdministrator(ctx, "anas-dev", "correct horse battery staple"); !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("SetupAdministrator(anas-dev) error = %v, want ErrIdentityConflict", err)
	}
	if required, err := service.SetupRequired(ctx); err != nil || !required {
		t.Fatalf("SetupRequired() = %v, %v; want setup still required", required, err)
	}
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatalf("SetupAdministrator(owner) error = %v", err)
	}
}

func TestResettingADisabledAccountDoesNotReenableIt(t *testing.T) {
	ctx := context.Background()
	service, credentials := newIdentityService(t, filepath.Join(t.TempDir(), "control.db"))
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DisableUser(ctx, admin, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetCredential(ctx, admin, member.ID, "a newly reset password"); err != nil {
		t.Fatalf("ResetCredential() error = %v", err)
	}
	if credentials.last.Enabled {
		t.Fatal("reset asked the Host Agent to enable a disabled account")
	}
	if _, err := service.Authenticate(ctx, "alice", "a newly reset password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("disabled account authenticated after reset: %v", err)
	}
	credentials.failure = errors.New("Host Agent unavailable")
	_ = service.ResetCredential(ctx, admin, member.ID, "another reset password")
	credentials.failure = nil
	if err := service.ResetCredential(ctx, admin, member.ID, "a third reset password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, "alice", "a third reset password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("failed reset turned a disabled account into an active one: %v", err)
	}
}

func TestExistingAccountsAreBackfilledAndSynchronized(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE, role TEXT NOT NULL,
    status TEXT NOT NULL, password_hash TEXT NOT NULL, created_at TEXT NOT NULL);
INSERT INTO users VALUES
    ('user:b', 'bob', 'member', 'disabled', 'x', '2026-10-02T00:00:00Z'),
    ('user:a', 'admin', 'admin', 'active', 'x', '2026-10-01T00:00:00Z'),
    ('user:c', 'carol', 'member', 'error', 'x', '2026-10-03T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	service, credentials := newIdentityService(t, path)
	if err := service.SyncIdentities(ctx); err != nil {
		t.Fatalf("SyncIdentities() error = %v", err)
	}
	want := []accounts.Identity{
		{Username: "admin", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
		{Username: "bob", UID: 20101, Role: accounts.RoleMember, Enabled: false},
	}
	if !slices.Equal(credentials.synced, want) {
		t.Fatalf("synchronized identities = %#v, want %#v", credentials.synced, want)
	}
}

func newIdentityService(t *testing.T, path string) (*accounts.Service, *identityRecorder) {
	t.Helper()
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := &identityRecorder{}
	return accounts.NewService(store, credentials, accounts.Options{}), credentials
}

type identityRecorder struct {
	conflicts map[string]bool
	failure   error
	uids      []int
	last      accounts.CredentialRequest
	synced    []accounts.Identity
}

func (r *identityRecorder) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	r.uids = append(r.uids, request.UID)
	r.last = request
	if r.conflicts[request.Username] {
		return accounts.ErrIdentityConflict
	}
	return r.failure
}

func (*identityRecorder) DisableCredential(context.Context, string) error { return nil }

func (r *identityRecorder) SyncIdentities(_ context.Context, identities []accounts.Identity) error {
	r.synced = identities
	return nil
}
