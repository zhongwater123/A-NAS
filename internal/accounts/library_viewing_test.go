package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestLibraryViewingIsGrantedApartFromSpaceViewing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
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

	for name, start := range map[string]func() error{
		"wrong password": func() error {
			_, err := service.StartLibraryViewing(ctx, admin, alice.ID, "not my password", "album")
			return err
		},
		"no reason": func() error {
			_, err := service.StartLibraryViewing(ctx, admin, alice.ID, "correct horse battery staple", "  ")
			return err
		},
		"member": func() error {
			_, err := service.StartLibraryViewing(ctx, alice, admin.ID, "alice password for testing", "album")
			return err
		},
	} {
		if err := start(); err == nil {
			t.Errorf("%s: StartLibraryViewing() succeeded", name)
		}
	}

	grant, err := service.StartLibraryViewing(ctx, admin, alice.ID, "correct horse battery staple", "find the wedding photos")
	if err != nil {
		t.Fatalf("StartLibraryViewing() error = %v", err)
	}
	if grant.Scope != accounts.ViewingScopeLibrary || !grant.ExpiresAt.Equal(now.Add(accounts.ViewingDuration)) {
		t.Fatalf("grant = %+v", grant)
	}
	if len(provisioner.granted) != 0 {
		t.Fatalf("library viewing changed ACLs: %+v", provisioner.granted)
	}
	if _, err := service.StartLibraryViewing(ctx, admin, alice.ID, "correct horse battery staple", "again"); !errors.Is(err, accounts.ErrViewingActive) {
		t.Fatalf("second grant error = %v, want ErrViewingActive", err)
	}
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, false); allowed {
		t.Fatal("a library grant opened the private space")
	}
	viewings, err := service.LibraryViewings(ctx, admin)
	if err != nil || len(viewings) != 1 || viewings[0].GrantID != grant.ID || viewings[0].OwnerUserID != alice.ID {
		t.Fatalf("LibraryViewings() = %+v, %v", viewings, err)
	}
	if viewings, _ := service.LibraryViewings(ctx, alice); len(viewings) != 0 {
		t.Fatalf("a member has library viewings: %+v", viewings)
	}
	notifications, err := service.Notifications(ctx, alice)
	if err != nil || len(notifications) != 1 || notifications[0].Kind != accounts.NotificationAdminLibraryViewing ||
		notifications[0].Reason != "find the wedding photos" || notifications[0].ActorUsername != "owner" {
		t.Fatalf("owner notifications = %+v, %v", notifications, err)
	}
	// A space grant for the same member is separate and still possible.
	if _, err := service.StartViewing(ctx, admin, alice.ID, "correct horse battery staple", "files too"); err != nil {
		t.Fatalf("StartViewing() alongside a library grant error = %v", err)
	}

	if err := service.EndViewing(ctx, admin, grant.ID); err != nil {
		t.Fatalf("EndViewing(library) error = %v", err)
	}
	if slices.Contains(provisioner.revoked, grant.ID) {
		t.Fatalf("ending a library grant asked the Host Agent to revoke ACLs")
	}
	if viewings, _ := service.LibraryViewings(ctx, admin); len(viewings) != 0 {
		t.Fatalf("ended grant still listed: %+v", viewings)
	}
	if allowed, _ := service.CanAccessSpace(ctx, admin, aliceSpace, false); !allowed {
		t.Fatal("ending the library grant ended the space grant")
	}

	if _, err := service.StartLibraryViewing(ctx, admin, alice.ID, "correct horse battery staple", "later"); err != nil {
		t.Fatalf("StartLibraryViewing() after ending error = %v", err)
	}
	now = now.Add(accounts.ViewingDuration)
	if viewings, _ := service.LibraryViewings(ctx, admin); len(viewings) != 0 {
		t.Fatalf("expired grant still listed: %+v", viewings)
	}

	events, err := service.ListAudit(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, event := range events {
		actions[event.Action]++
	}
	if actions["photos.viewing_started"] != 2 || actions["photos.viewing_ended"] != 1 {
		t.Fatalf("audit actions = %v", actions)
	}
}
