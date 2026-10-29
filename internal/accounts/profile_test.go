package accounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
)

type profileFixture struct {
	service *accounts.Service
	admin   accounts.User
	alice   accounts.User
}

func newProfileFixture(t *testing.T) profileFixture {
	t.Helper()
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &credentialRecorder{}, accounts.Options{PasswordCost: cheapPasswords})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return profileFixture{service: service, admin: admin, alice: alice}
}

func TestNicknamesAreTidiedCheckedAndShownToEveryone(t *testing.T) {
	ctx := context.Background()
	f := newProfileFixture(t)

	user, err := f.service.SetNickname(ctx, f.alice, "  小  爱 ")
	if err != nil {
		t.Fatalf("SetNickname() error = %v", err)
	}
	if got, want := user.Nickname, "小 爱"; got != want {
		t.Fatalf("nickname = %q, want %q", got, want)
	}
	session, err := f.service.Authenticate(ctx, "alice", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if got := session.User.Nickname; got != "小 爱" {
		t.Fatalf("signed-in nickname = %q", got)
	}
	resolved, err := f.service.ResolveSession(ctx, session.Token)
	if err != nil || resolved.User.Nickname != "小 爱" {
		t.Fatalf("resolved nickname = %q, err = %v", resolved.User.Nickname, err)
	}

	// Members see every account's name, not just administrators.
	profiles, err := f.service.Profiles(ctx, f.alice)
	if err != nil {
		t.Fatalf("Profiles() error = %v", err)
	}
	if len(profiles) != 2 || profiles[0].Username != "alice" || profiles[0].Nickname != "小 爱" || profiles[1].Username != "owner" || profiles[1].Nickname != "" {
		t.Fatalf("profiles = %#v", profiles)
	}

	for _, test := range []struct {
		nickname string
		want     error
	}{
		{nickname: strings.Repeat("长", accounts.MaxNicknameLength+1), want: accounts.ErrInvalidNickname},
		{nickname: "line\nbreak", want: accounts.ErrInvalidNickname},
		{nickname: "evil\u202ename", want: accounts.ErrInvalidNickname},
		{nickname: "OWNER", want: accounts.ErrNicknameUnavailable},
	} {
		if _, err := f.service.SetNickname(ctx, f.alice, test.nickname); !errors.Is(err, test.want) {
			t.Errorf("SetNickname(%q) error = %v, want %v", test.nickname, err, test.want)
		}
	}
	if _, err := f.service.SetNickname(ctx, f.admin, "小 爱"); !errors.Is(err, accounts.ErrNicknameUnavailable) {
		t.Fatalf("taking another account's nickname error = %v", err)
	}
	// An administrator cannot create a member under a name someone goes by.
	if _, err := f.service.CreateMember(ctx, f.admin, "Mom", "yet another passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SetNickname(ctx, f.admin, "mom"); !errors.Is(err, accounts.ErrNicknameUnavailable) {
		t.Fatalf("nickname equal to a username error = %v", err)
	}
	if _, err := f.service.SetNickname(ctx, f.admin, "爸爸"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateMember(ctx, f.admin, "爸爸", "yet another passphrase"); err == nil {
		t.Fatal("created a member named like an existing nickname")
	}

	// Emoji joined into one picture keep their joiner; blank clears.
	if user, err := f.service.SetNickname(ctx, f.alice, "👩‍💻"); err != nil || user.Nickname != "👩‍💻" {
		t.Fatalf("emoji nickname = %q, err = %v", user.Nickname, err)
	}
	if user, err := f.service.SetNickname(ctx, f.alice, "   "); err != nil || user.Nickname != "" {
		t.Fatalf("cleared nickname = %q, err = %v", user.Nickname, err)
	}
}

func TestNicknameChangesAreRecordedInTheAccountLog(t *testing.T) {
	ctx := context.Background()
	f := newProfileFixture(t)
	if _, err := f.service.SetNickname(ctx, f.alice, "小爱"); err != nil {
		t.Fatal(err)
	}
	// Saving the same name again is not a change.
	if _, err := f.service.SetNickname(ctx, f.alice, "小爱"); err != nil {
		t.Fatal(err)
	}
	events, err := f.service.Activity(ctx, f.alice, activity.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var changes []activity.Event
	for _, event := range events {
		if event.Action == "user.nickname_changed" {
			changes = append(changes, event)
		}
	}
	if len(changes) != 1 || changes[0].Detail != `{"from":"","to":"小爱"}` || changes[0].ActorUserID != f.alice.ID {
		t.Fatalf("nickname events = %#v", changes)
	}
}

func TestAvatarsAreStoredVersionedAndRemoved(t *testing.T) {
	ctx := context.Background()
	f := newProfileFixture(t)
	if _, err := f.service.Avatar(ctx, f.alice.ID); !errors.Is(err, accounts.ErrAvatarNotFound) {
		t.Fatalf("Avatar() before upload error = %v", err)
	}
	user, err := f.service.SetAvatar(ctx, f.alice, []byte("first picture"), "image/jpeg")
	if err != nil {
		t.Fatalf("SetAvatar() error = %v", err)
	}
	first := user.AvatarVersion
	if len(first) != 16 {
		t.Fatalf("avatar version = %q", first)
	}
	stored, err := f.service.Avatar(ctx, f.alice.ID)
	if err != nil || string(stored.Image) != "first picture" || stored.Version != first || stored.ContentType != "image/jpeg" {
		t.Fatalf("stored avatar = %#v, err = %v", stored, err)
	}
	user, err = f.service.SetAvatar(ctx, f.alice, []byte("second picture"), "image/jpeg")
	if err != nil || user.AvatarVersion == first {
		t.Fatalf("replaced avatar version = %q, err = %v", user.AvatarVersion, err)
	}
	profiles, err := f.service.Profiles(ctx, f.admin)
	if err != nil || profiles[0].AvatarVersion != user.AvatarVersion {
		t.Fatalf("profiles = %#v, err = %v", profiles, err)
	}
	if user, err = f.service.RemoveAvatar(ctx, f.alice); err != nil || user.AvatarVersion != "" {
		t.Fatalf("RemoveAvatar() = %#v, err = %v", user, err)
	}
	if _, err := f.service.Avatar(ctx, f.alice.ID); !errors.Is(err, accounts.ErrAvatarNotFound) {
		t.Fatalf("Avatar() after removal error = %v", err)
	}
	events, err := f.service.Activity(ctx, f.alice, activity.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Action]++
	}
	if counts["user.avatar_changed"] != 2 || counts["user.avatar_removed"] != 1 {
		t.Fatalf("avatar events = %v", counts)
	}

	// A disabled account keeps its name for what it wrote but cannot edit it.
	if err := f.service.DisableUser(ctx, f.admin, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	disabled := f.alice
	disabled.Status = accounts.UserStatusDisabled
	if _, err := f.service.SetNickname(ctx, disabled, "x"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("disabled SetNickname() error = %v", err)
	}
	if _, err := f.service.Profiles(ctx, disabled); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("disabled Profiles() error = %v", err)
	}
	if profiles, err := f.service.Profiles(ctx, f.admin); err != nil || len(profiles) != 2 {
		t.Fatalf("profiles after disabling = %#v, err = %v", profiles, err)
	}
}
