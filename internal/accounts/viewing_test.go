package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestAdministrativeViewingIsAuthenticatedReadOnlyAuditedAndTemporary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	provisioner := &viewingRecorder{}
	service := accounts.NewService(store, provisioner, accounts.Options{Now: func() time.Time { return now }})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	aliceSpace := privateSpaceOf(t, service, alice)

	for name, test := range map[string]struct {
		actor    accounts.User
		owner    string
		password string
		reason   string
		want     error
	}{
		"member":         {alice, admin.ID, "alice password for testing", "curious", accounts.ErrForbidden},
		"own space":      {admin, admin.ID, "correct horse battery staple", "mine", accounts.ErrForbidden},
		"unknown user":   {admin, "user:missing", "correct horse battery staple", "who", accounts.ErrUserNotFound},
		"wrong password": {admin, alice.ID, "not the password", "recover files", accounts.ErrInvalidCredentials},
		"no reason":      {admin, alice.ID, "correct horse battery staple", "   ", accounts.ErrReasonRequired},
	} {
		if _, err := service.StartViewing(ctx, test.actor, test.owner, test.password, test.reason); !errors.Is(err, test.want) {
			t.Errorf("%s: StartViewing() error = %v, want %v", name, err, test.want)
		}
	}
	if len(provisioner.granted) != 0 {
		t.Fatalf("refused requests reached the Host Agent: %#v", provisioner.granted)
	}

	grant, err := service.StartViewing(ctx, admin, alice.ID, "correct horse battery staple", "Alice asked me to recover a document")
	if err != nil {
		t.Fatalf("StartViewing() error = %v", err)
	}
	if !grant.ExpiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("grant expires at %s, want 24 hours later", grant.ExpiresAt)
	}
	if len(provisioner.granted) != 1 || provisioner.granted[0].AdminUsername != "owner" || provisioner.granted[0].AdminUID != accounts.FirstUserUID {
		t.Fatalf("Host Agent request = %#v", provisioner.granted)
	}
	if _, err := service.StartViewing(ctx, admin, alice.ID, "correct horse battery staple", "again"); !errors.Is(err, accounts.ErrViewingActive) {
		t.Fatalf("second StartViewing() error = %v, want ErrViewingActive", err)
	}
	viewed := findSpace(t, service, admin, aliceSpace)
	if viewed.Viewing == nil || viewed.Viewing.GrantID != grant.ID {
		t.Fatalf("viewed space = %#v, want viewing access", viewed)
	}
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, false); !allowed {
		t.Fatal("administrator cannot read the viewed space")
	}
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, true); allowed {
		t.Fatal("administrator can write the viewed space")
	}
	notifications, err := service.Notifications(ctx, alice)
	if err != nil || len(notifications) != 1 || notifications[0].Kind != accounts.NotificationAdminViewing ||
		notifications[0].ActorUsername != "owner" || notifications[0].Reason != "Alice asked me to recover a document" ||
		notifications[0].ExpiresAt == nil || !notifications[0].ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatalf("owner notifications = %#v, %v", notifications, err)
	}
	if err := service.AcknowledgeNotification(ctx, alice, notifications[0].ID); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := service.Notifications(ctx, alice); len(remaining) != 0 {
		t.Fatalf("acknowledged notification is still shown: %#v", remaining)
	}
	events, err := service.ListAudit(ctx, admin)
	if err != nil || !hasAudit(events, "space.viewing_started", aliceSpace) {
		t.Fatalf("audit = %#v, %v; want space.viewing_started", events, err)
	}

	now = grant.ExpiresAt
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, false); allowed {
		t.Fatal("viewing access outlived its expiry")
	}
	if spaces, _ := service.ListSpaces(ctx, admin); containsSpace(spaces, aliceSpace) {
		t.Fatal("expired viewing still lists the space")
	}

	now = now.Add(time.Hour)
	second, err := service.StartViewing(ctx, admin, alice.ID, "correct horse battery staple", "follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EndViewing(ctx, admin, second.ID); err != nil {
		t.Fatalf("EndViewing() error = %v", err)
	}
	if len(provisioner.revoked) != 1 || provisioner.revoked[0] != second.ID {
		t.Fatalf("revoked = %#v", provisioner.revoked)
	}
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, false); allowed {
		t.Fatal("ended viewing still grants access")
	}
}

func TestResetPasswordMustBeChangedBeforeAnythingElse(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	provisioner := &viewingRecorder{}
	service := accounts.NewService(store, provisioner, accounts.Options{})
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
	if provisioner.last.Enabled {
		t.Fatal("SMB was enabled with the password the administrator chose")
	}
	session, err := service.Authenticate(ctx, "alice", "temporary password 1")
	if err != nil {
		t.Fatal(err)
	}
	if !session.User.MustChangePassword {
		t.Fatal("reset member is not required to change the password")
	}
	notifications, err := service.Notifications(ctx, session.User)
	if err != nil || len(notifications) != 1 || notifications[0].Kind != accounts.NotificationCredentialReset || notifications[0].ActorUsername != "owner" {
		t.Fatalf("notifications = %#v, %v", notifications, err)
	}
	if err := service.SyncIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	for _, identity := range provisioner.synced {
		if identity.Username == "alice" && identity.Enabled {
			t.Fatal("identity sync re-enabled SMB before the password change")
		}
	}
	if err := service.ChangePassword(ctx, session.User, "wrong current password", "alice chosen password"); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("ChangePassword(wrong current) error = %v", err)
	}
	if err := service.ChangePassword(ctx, session.User, "temporary password 1", "temporary password 1"); !errors.Is(err, accounts.ErrPasswordUnchanged) {
		t.Fatalf("ChangePassword(same) error = %v", err)
	}
	if err := service.ChangePassword(ctx, session.User, "temporary password 1", "alice chosen password"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if !provisioner.last.Enabled || provisioner.last.Password != "alice chosen password" {
		t.Fatalf("SMB credential after change = %#v", provisioner.last)
	}
	current, err := service.ResolveSession(ctx, session.Token)
	if err != nil || current.User.MustChangePassword {
		t.Fatalf("session after change = %#v, %v", current, err)
	}
	if err := service.ResetCredential(ctx, admin, admin.ID, "administrator new password"); err != nil {
		t.Fatal(err)
	}
	if self, err := service.Authenticate(ctx, "owner", "administrator new password"); err != nil || self.User.MustChangePassword {
		t.Fatalf("an administrator resetting their own password is forced to change it: %#v, %v", self, err)
	}
}

func privateSpaceOf(t *testing.T, service *accounts.Service, user accounts.User) string {
	t.Helper()
	spaces, err := service.ListSpaces(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate && space.OwnerUserID == user.ID {
			return space.ID
		}
	}
	t.Fatalf("no private space for %s", user.Username)
	return ""
}

func findSpace(t *testing.T, service *accounts.Service, user accounts.User, spaceID string) accounts.Space {
	t.Helper()
	spaces, err := service.ListSpaces(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.ID == spaceID {
			return space
		}
	}
	t.Fatalf("space %s is not listed", spaceID)
	return accounts.Space{}
}

func containsSpace(spaces []accounts.Space, spaceID string) bool {
	for _, space := range spaces {
		if space.ID == spaceID {
			return true
		}
	}
	return false
}

func hasAudit(events []accounts.AuditEvent, action, resourceID string) bool {
	for _, event := range events {
		if event.Action == action && event.ResourceID == resourceID {
			return true
		}
	}
	return false
}

type viewingRecorder struct {
	identityRecorder
	granted []accounts.ViewingRequest
	revoked []string
}

func (r *viewingRecorder) GrantViewing(_ context.Context, request accounts.ViewingRequest) error {
	r.granted = append(r.granted, request)
	return nil
}

func (r *viewingRecorder) RevokeViewing(_ context.Context, id string) error {
	r.revoked = append(r.revoked, id)
	return nil
}
