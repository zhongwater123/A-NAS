package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
)

// lifecycleHost stands in for the Host Agent's credential, enabling and
// removal operations, recording what it was asked.
type lifecycleHost struct {
	set       []accounts.CredentialRequest
	disabled  []string
	enabled   []accounts.EnableRequest
	removed   []accounts.AccountRemoval
	enableErr error
	// handedTo is the folder RemoveAccount says the files went to.
	handedTo string
}

func (h *lifecycleHost) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	h.set = append(h.set, request)
	return nil
}

func (h *lifecycleHost) DisableCredential(_ context.Context, username string) error {
	h.disabled = append(h.disabled, username)
	return nil
}

func (h *lifecycleHost) EnableCredential(_ context.Context, request accounts.EnableRequest) error {
	h.enabled = append(h.enabled, request)
	return h.enableErr
}

func (h *lifecycleHost) RemoveAccount(_ context.Context, removal accounts.AccountRemoval) (string, error) {
	h.removed = append(h.removed, removal)
	return h.handedTo, nil
}

func (h *lifecycleHost) SyncIdentities(context.Context, []accounts.Identity) error { return nil }

type lifecycleFixture struct {
	service      *accounts.Service
	host         *lifecycleHost
	admin, alice accounts.User
	// database is control.db, which the Host Agent reads too.
	database string
}

const alicePassword = "alice password for testing"

func newLifecycleFixture(t *testing.T, delay time.Duration) lifecycleFixture {
	t.Helper()
	database := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host := &lifecycleHost{}
	service := accounts.NewService(store, host, accounts.Options{PasswordCost: cheapPasswords, DeletionDelay: delay})
	admin, err := service.SetupAdministrator(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(context.Background(), admin, "alice", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycleFixture{service: service, host: host, admin: admin, alice: alice, database: database}
}

func (f lifecycleFixture) user(t *testing.T, id string) (accounts.User, bool) {
	t.Helper()
	users, err := f.service.ListUsers(context.Background(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == id {
			return user, true
		}
	}
	return accounts.User{}, false
}

func (f lifecycleFixture) disable(t *testing.T) {
	t.Helper()
	if err := f.service.DisableUser(context.Background(), f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
}

func (f lifecycleFixture) schedule(t *testing.T, personalSpace, library accounts.DeletionChoice) accounts.User {
	t.Helper()
	user, err := f.service.ScheduleDeletion(context.Background(), f.admin, f.alice.ID, accounts.DeletionRequest{
		Password: "correct horse battery staple", PersonalSpace: personalSpace, Library: library,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (f lifecycleFixture) audited(t *testing.T, action string) bool {
	t.Helper()
	events, err := f.service.Activity(context.Background(), f.admin, activity.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(events, func(event activity.Event) bool { return event.Action == action })
}

func TestEnablingADisabledMemberGivesItsSignInBackWithItsPassword(t *testing.T) {
	f := newLifecycleFixture(t, 0)
	ctx := context.Background()
	f.disable(t)
	if _, err := f.service.Authenticate(ctx, "alice", alicePassword); err == nil {
		t.Fatal("a disabled member signed in")
	}
	if err := f.service.EnableUser(ctx, f.alice, f.alice.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("a member enabled an account: %v", err)
	}
	if err := f.service.EnableUser(ctx, f.admin, f.admin.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("the administrator enabled itself: %v", err)
	}
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if user, _ := f.user(t, f.alice.ID); user.Status != accounts.UserStatusActive {
		t.Fatalf("status = %s", user.Status)
	}
	if want := []accounts.EnableRequest{{Username: "alice", SMB: true}}; !slices.Equal(f.host.enabled, want) {
		t.Fatalf("host asked %+v, want %+v", f.host.enabled, want)
	}
	if _, err := f.service.Authenticate(ctx, "alice", alicePassword); err != nil {
		t.Fatalf("the enabled member cannot sign in with its password: %v", err)
	}
	if !f.audited(t, "user.enabled") {
		t.Fatal("enabling was not audited")
	}
}

func TestEnablingAMemberThatMustChooseAPasswordKeepsSMBOff(t *testing.T) {
	f := newLifecycleFixture(t, 0)
	ctx := context.Background()
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary password 1"); err != nil {
		t.Fatal(err)
	}
	f.disable(t)
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if want := []accounts.EnableRequest{{Username: "alice", SMB: false}}; !slices.Equal(f.host.enabled, want) {
		t.Fatalf("host asked %+v, want %+v", f.host.enabled, want)
	}
}

func TestAHostFailureLeavesTheAccountEnabledAndEnablingAgainRetries(t *testing.T) {
	f := newLifecycleFixture(t, 0)
	ctx := context.Background()
	f.disable(t)
	f.host.enableErr = errors.New("Samba is unavailable")
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); !errors.Is(err, accounts.ErrHostEnablePending) {
		t.Fatalf("err = %v", err)
	}
	if user, _ := f.user(t, f.alice.ID); user.Status != accounts.UserStatusActive {
		t.Fatalf("status = %s", user.Status)
	}
	f.host.enableErr = nil
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.host.enabled) != 2 {
		t.Fatalf("host asked %d times", len(f.host.enabled))
	}
}

func TestOnlyADisabledMemberCanBeDeletedWithThePasswordAgain(t *testing.T) {
	f := newLifecycleFixture(t, 0)
	ctx := context.Background()
	request := accounts.DeletionRequest{Password: "correct horse battery staple", PersonalSpace: accounts.DeletionTransfer, Library: accounts.DeletionDelete}
	if _, err := f.service.ScheduleDeletion(ctx, f.admin, f.alice.ID, request); !errors.Is(err, accounts.ErrNotDisabled) {
		t.Fatalf("an active member was deleted: %v", err)
	}
	f.disable(t)
	wrong := request
	wrong.Password = "not the password"
	if _, err := f.service.ScheduleDeletion(ctx, f.admin, f.alice.ID, wrong); !errors.Is(err, accounts.ErrInvalidCredentials) {
		t.Fatalf("a wrong password deleted a member: %v", err)
	}
	invalid := request
	invalid.Library = "export"
	if _, err := f.service.ScheduleDeletion(ctx, f.admin, f.alice.ID, invalid); !errors.Is(err, accounts.ErrInvalidInput) {
		t.Fatalf("an unknown choice was taken: %v", err)
	}
	if _, err := f.service.ScheduleDeletion(ctx, f.admin, f.admin.ID, request); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("the administrator deleted itself: %v", err)
	}
	before := time.Now()
	deleted, err := f.service.ScheduleDeletion(ctx, f.admin, f.alice.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Deletion == nil || deleted.Deletion.DueAt.Before(before.Add(accounts.DefaultDeletionDelay)) ||
		deleted.Deletion.PersonalSpace != accounts.DeletionTransfer || deleted.Deletion.Library != accounts.DeletionDelete || deleted.Deletion.Started {
		t.Fatalf("deletion = %+v", deleted.Deletion)
	}
	if user, _ := f.user(t, f.alice.ID); user.Deletion == nil || user.Status != accounts.UserStatusDisabled {
		t.Fatalf("the list shows %+v", user)
	}
	if _, err := f.service.ScheduleDeletion(ctx, f.admin, f.alice.ID, request); !errors.Is(err, accounts.ErrDeletionPending) {
		t.Fatalf("a second deletion: %v", err)
	}
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); !errors.Is(err, accounts.ErrDeletionPending) {
		t.Fatalf("an account waiting to be deleted was enabled: %v", err)
	}
	if err := f.service.ResetCredential(ctx, f.admin, f.alice.ID, "temporary password 1"); !errors.Is(err, accounts.ErrDeletionPending) {
		t.Fatalf("an account waiting to be deleted got a password: %v", err)
	}
	if !f.audited(t, "user.deletion_scheduled") {
		t.Fatal("the deletion was not audited")
	}
}

func TestCancellingADeletionKeepsTheAccountDisabled(t *testing.T) {
	f := newLifecycleFixture(t, 0)
	ctx := context.Background()
	f.disable(t)
	f.schedule(t, accounts.DeletionDelete, accounts.DeletionDelete)
	if err := f.service.CancelDeletion(ctx, f.alice, f.alice.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("a member cancelled a deletion: %v", err)
	}
	if err := f.service.CancelDeletion(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	user, _ := f.user(t, f.alice.ID)
	if user.Deletion != nil || user.Status != accounts.UserStatusDisabled {
		t.Fatalf("after cancelling: %+v", user)
	}
	if err := f.service.CancelDeletion(ctx, f.admin, f.alice.ID); !errors.Is(err, accounts.ErrNoDeletion) {
		t.Fatalf("cancelling twice: %v", err)
	}
	if !f.audited(t, "user.deletion_cancelled") {
		t.Fatal("cancelling was not audited")
	}
	if err := f.service.EnableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatalf("the kept account cannot be enabled: %v", err)
	}
}

func TestADueDeletionRemovesTheAccountAfterEveryStepAndFreesItsName(t *testing.T) {
	f := newLifecycleFixture(t, time.Millisecond)
	ctx := context.Background()
	f.disable(t)
	f.schedule(t, accounts.DeletionTransfer, accounts.DeletionTransfer)
	time.Sleep(5 * time.Millisecond)

	var ran []string
	var seen accounts.Removal
	failPhotos := errors.New("the photo service is unavailable")
	failShares := errors.New("the share service is unavailable")
	sharesFail := true
	steps := []accounts.DeletionStep{
		{Name: "photos", Remove: func(_ context.Context, removal accounts.Removal) (accounts.Handover, error) {
			ran = append(ran, "photos")
			seen = removal
			return accounts.Handover{}, failPhotos
		}},
		f.service.HostRemoval(),
		accounts.ForgetStep("shares", func(context.Context, accounts.Removal) error {
			ran = append(ran, "shares")
			if sharesFail {
				return failShares
			}
			return nil
		}),
	}
	f.service.RunDueDeletions(ctx, steps)
	if seen.Username != "alice" || seen.Administrator.ID != f.admin.ID || seen.PrivateSpaceID == "" || seen.UID < accounts.FirstUserUID {
		t.Fatalf("the steps saw %+v", seen)
	}
	user, _ := f.user(t, f.alice.ID)
	if user.Deletion == nil || !user.Deletion.Started || !user.Deletion.Retrying {
		t.Fatalf("a failed deletion shows %+v", user.Deletion)
	}
	if err := f.service.CancelDeletion(ctx, f.admin, f.alice.ID); !errors.Is(err, accounts.ErrDeletionStarted) {
		t.Fatalf("a started deletion was cancelled: %v", err)
	}
	if _, err := f.service.StartViewing(ctx, f.admin, f.alice.ID, "correct horse battery staple", "check"); !errors.Is(err, accounts.ErrDeletionStarted) {
		t.Fatalf("viewing a space being removed: %v", err)
	}
	// The Host Agent reads the removal from control.db itself, not from the
	// Product Service's request.
	directory := sessiondir.New(f.database)
	t.Cleanup(func() { _ = directory.Close() })
	removal, err := directory.AccountRemoval(ctx, f.alice.ID)
	if err != nil || removal.Username != "alice" || removal.UID != seen.UID || removal.PrivateSpaceID != seen.PrivateSpaceID ||
		removal.TransferTo != "owner" || removal.TransferToUID < accounts.FirstUserUID || removal.Folder != "alice 的文件" {
		t.Fatalf("the Host Agent reads %+v, %v", removal, err)
	}

	// The administrator already had a folder and an album of those names:
	// the steps say which names theirs got, and the next attempt keeps them.
	steps[0].Remove = func(context.Context, accounts.Removal) (accounts.Handover, error) {
		ran = append(ran, "photos")
		return accounts.Handover{Album: "alice 的照片 (2)"}, nil
	}
	f.host.handedTo = "alice 的文件 (2)"
	f.service.RunDueDeletions(ctx, steps)
	sharesFail = false
	f.service.RunDueDeletions(ctx, steps)
	if want := []string{"photos", "photos", "shares", "shares"}; !slices.Equal(ran, want) {
		t.Fatalf("steps ran %v, want %v", ran, want)
	}
	if len(f.host.removed) != 1 || f.host.removed[0].UserID != f.alice.ID || f.host.removed[0].Username != "alice" ||
		f.host.removed[0].TransferTo != "owner" || f.host.removed[0].Folder != "alice 的文件" {
		t.Fatalf("the Host Agent was asked %+v", f.host.removed)
	}
	if _, found := f.user(t, f.alice.ID); found {
		t.Fatal("the deleted account is still listed")
	}
	if _, err := directory.AccountRemoval(ctx, f.alice.ID); !errors.Is(err, accounts.ErrNotDeleting) {
		t.Fatalf("the Host Agent still reads a removal: %v", err)
	}
	if !f.audited(t, "user.deleted") {
		t.Fatal("the deletion was not audited")
	}
	notices, err := f.service.Notifications(ctx, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0].Kind != accounts.NotificationAccountDeleted || notices[0].ActorUsername != "alice" ||
		notices[0].Folder != "alice 的文件 (2)" || notices[0].Album != "alice 的照片 (2)" {
		t.Fatalf("notifications = %+v", notices)
	}
	again, err := f.service.CreateMember(ctx, f.admin, "alice", "another alice password")
	if err != nil {
		t.Fatalf("the name of a deleted account cannot be used again: %v", err)
	}
	if again.ID == f.alice.ID {
		t.Fatal("the new account took the deleted one's ID")
	}
	newest := f.host.set[len(f.host.set)-1]
	if newest.Username != "alice" || newest.UID == seen.UID {
		t.Fatalf("the new account got UID %d, the deleted one had %d", newest.UID, seen.UID)
	}
}

// A transfer that found nothing to hand over makes no folder or album, and
// the administrator is told of none.
func TestADeletionThatHandedNothingOverSendsNoNotice(t *testing.T) {
	f := newLifecycleFixture(t, time.Millisecond)
	ctx := context.Background()
	f.disable(t)
	f.schedule(t, accounts.DeletionDelete, accounts.DeletionTransfer)
	time.Sleep(5 * time.Millisecond)
	f.service.RunDueDeletions(ctx, []accounts.DeletionStep{
		{Name: "photos", Remove: func(context.Context, accounts.Removal) (accounts.Handover, error) { return accounts.Handover{}, nil }},
		f.service.HostRemoval(),
	})
	if _, found := f.user(t, f.alice.ID); found {
		t.Fatal("the deleted account is still listed")
	}
	if len(f.host.removed) != 1 || f.host.removed[0].TransferTo != "" {
		t.Fatalf("the Host Agent was asked %+v", f.host.removed)
	}
	if notices, err := f.service.Notifications(ctx, f.admin); err != nil || len(notices) != 0 {
		t.Fatalf("notifications = %+v, %v", notices, err)
	}
}

func TestADeletionThatIsNotDueYetWaits(t *testing.T) {
	f := newLifecycleFixture(t, time.Hour)
	f.disable(t)
	f.schedule(t, accounts.DeletionDelete, accounts.DeletionDelete)
	ran := false
	wait := f.service.RunDueDeletions(context.Background(), []accounts.DeletionStep{
		accounts.ForgetStep("photos", func(context.Context, accounts.Removal) error { ran = true; return nil }),
	})
	if ran {
		t.Fatal("a deletion ran before it was due")
	}
	if wait <= 0 || wait > time.Hour {
		t.Fatalf("wait = %s", wait)
	}
	if user, _ := f.user(t, f.alice.ID); user.Deletion == nil || user.Deletion.Started {
		t.Fatalf("deletion = %+v", user.Deletion)
	}
	// Until it starts, the Host Agent and the services refuse to remove it.
	directory := sessiondir.New(f.database)
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := directory.AccountRemoval(context.Background(), f.alice.ID); !errors.Is(err, accounts.ErrNotDeleting) {
		t.Fatalf("the Host Agent reads a removal that has not started: %v", err)
	}
	if _, err := directory.AccountDeletion(context.Background(), f.alice.ID); !errors.Is(err, accounts.ErrNotDeleting) {
		t.Fatalf("the services read a deletion that has not started: %v", err)
	}
}
