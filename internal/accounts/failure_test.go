package accounts_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// hostDouble stands in for the Host Agent: it remembers the last credential
// it was given and the viewing grants in force, and fails when told to.
type hostDouble struct {
	last       accounts.CredentialRequest
	disableErr error
	grants     map[string]bool
}

func (h *hostDouble) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	h.last = request
	return nil
}

func (h *hostDouble) DisableCredential(context.Context, string) error { return h.disableErr }

func (h *hostDouble) GrantViewing(_ context.Context, request accounts.ViewingRequest) error {
	h.grants[request.ID] = true
	return nil
}

func (h *hostDouble) RevokeViewing(_ context.Context, grantID string) error {
	delete(h.grants, grantID)
	return nil
}

type faultFixture struct {
	service      *accounts.Service
	db           *sql.DB
	host         *hostDouble
	admin, alice accounts.User
}

// newFaultFixture opens a second connection to the account database, through
// which a test injects failures with triggers.
func newFaultFixture(t *testing.T) faultFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host := &hostDouble{grants: map[string]bool{}}
	service := accounts.NewService(store, host, accounts.Options{})
	admin, err := service.SetupAdministrator(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(context.Background(), admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return faultFixture{service: service, db: db, host: host, admin: admin, alice: alice}
}

func (f faultFixture) exec(t *testing.T, statement string) {
	t.Helper()
	if _, err := f.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func (f faultFixture) count(t *testing.T, query string) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDisablingEndsWebAccessEvenWhenTheHostAgentFails(t *testing.T) {
	ctx := context.Background()
	f := newFaultFixture(t)
	session, err := f.service.Authenticate(ctx, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	f.host.disableErr = errors.New("injected Host Agent failure")
	if err := f.service.DisableUser(ctx, f.admin, f.alice.ID); !errors.Is(err, accounts.ErrSMBDisablePending) {
		t.Fatalf("DisableUser() error = %v, want ErrSMBDisablePending", err)
	}
	if _, err := f.service.ResolveSession(ctx, session.Token); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Fatalf("existing session error = %v, want ErrSessionNotFound", err)
	}
	if _, err := f.service.Authenticate(ctx, "alice", "alice password for testing"); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("new sign-in error = %v, want ErrInvalidCredentials", err)
	}
	// Disabling again retries only the SMB credential.
	f.host.disableErr = nil
	if err := f.service.DisableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatalf("retried DisableUser() error = %v", err)
	}
	if got := f.count(t, "SELECT count(*) FROM audit_events WHERE action = 'user.disabled'"); got != 1 {
		t.Fatalf("user.disabled audit events = %d, want 1", got)
	}
}

func TestAFailedPasswordChangePutsSMBBackOnTheCurrentPassword(t *testing.T) {
	ctx := context.Background()
	f := newFaultFixture(t)
	f.exec(t, `CREATE TRIGGER fault BEFORE UPDATE OF password_hash ON users BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err := f.service.ChangePassword(ctx, f.alice, "alice password for testing", "alice new password"); err == nil {
		t.Fatal("ChangePassword() succeeded despite the failed write")
	}
	if f.host.last.Password != "alice password for testing" || !f.host.last.Enabled {
		t.Fatalf("SMB credential after the failure = %q enabled=%v, want the current password enabled", f.host.last.Password, f.host.last.Enabled)
	}
	if _, err := f.service.Authenticate(ctx, "alice", "alice password for testing"); err != nil {
		t.Fatalf("current password no longer signs in: %v", err)
	}
	if got := f.count(t, "SELECT count(*) FROM audit_events WHERE action = 'user.password_changed'"); got != 0 {
		t.Fatalf("password_changed audit events = %d, want 0", got)
	}

	// After a reset, SMB stays disabled until the change succeeds.
	f.exec(t, "DROP TRIGGER fault")
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `CREATE TRIGGER fault BEFORE UPDATE OF password_hash ON users BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err := f.service.ChangePassword(ctx, f.alice, "temporary password 1", "alice new password"); err == nil {
		t.Fatal("ChangePassword() after reset succeeded despite the failed write")
	}
	if f.host.last.Password != "temporary password 1" || f.host.last.Enabled {
		t.Fatalf("SMB credential after the failure = %q enabled=%v, want the temporary password disabled", f.host.last.Password, f.host.last.Enabled)
	}
}

func TestAViewingNobodyWasToldAboutIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	f := newFaultFixture(t)
	spaces, err := f.service.ListSpaces(ctx, f.alice)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate {
			private = space.ID
		}
	}
	f.exec(t, `CREATE TRIGGER fault BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if _, err := f.service.StartViewing(ctx, f.admin, f.alice.ID, "correct horse battery staple", "find a lost file"); err == nil {
		t.Fatal("StartViewing() succeeded without notifying the owner")
	}
	if readable, err := f.service.CanAccessSpace(ctx, f.admin, private, false); err != nil || readable {
		t.Fatalf("private space readable after the failure = %v, %v", readable, err)
	}
	if len(f.host.grants) != 0 {
		t.Fatalf("Host Agent grants after the failure = %v, want none", f.host.grants)
	}
	if _, err := f.service.StartLibraryViewing(ctx, f.admin, f.alice.ID, "correct horse battery staple", "find a lost photo"); err == nil {
		t.Fatal("StartLibraryViewing() succeeded without notifying the owner")
	}
	if viewings, err := f.service.LibraryViewings(ctx, f.admin); err != nil || len(viewings) != 0 {
		t.Fatalf("library viewings after the failure = %#v, %v", viewings, err)
	}
	if got := f.count(t, "SELECT count(*) FROM audit_events WHERE action LIKE '%viewing_started'"); got != 0 {
		t.Fatalf("viewing_started audit events = %d, want 0", got)
	}
}

func TestAResetLeavesItsTraceOrDoesNotHappen(t *testing.T) {
	ctx := context.Background()
	f := newFaultFixture(t)
	f.exec(t, `CREATE TRIGGER fault BEFORE INSERT ON audit_events WHEN NEW.action = 'user.credential_reset' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary password 1"); err == nil {
		t.Fatal("ResetCredential() succeeded without its audit event")
	}
	if _, err := f.service.Authenticate(ctx, "alice", "alice password for testing"); err != nil {
		t.Fatalf("the password changed without a trace: %v", err)
	}
	if got := f.count(t, "SELECT count(*) FROM notifications"); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
	if reset, err := f.service.LastCredentialReset(ctx, f.alice); err != nil || reset != nil {
		t.Fatalf("LastCredentialReset() = %#v, %v; want none", reset, err)
	}

	f.exec(t, "DROP TRIGGER fault")
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}
	if reset, err := f.service.LastCredentialReset(ctx, f.alice); err != nil || reset == nil || reset.ActorUsername != "owner" || reset.At.IsZero() {
		t.Fatalf("LastCredentialReset() = %#v, %v", reset, err)
	}
	if reset, err := f.service.LastCredentialReset(ctx, f.admin); err != nil || reset != nil {
		t.Fatalf("the administrator's own LastCredentialReset() = %#v, %v; want none", reset, err)
	}
}
