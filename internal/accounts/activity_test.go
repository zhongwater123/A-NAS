package accounts_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
)

type sessionDetail struct {
	Via      string `json:"via"`
	Address  string `json:"address"`
	Username string `json:"username"`
	Reason   string `json:"reason"`
}

func detailOf(t *testing.T, event activity.Event) sessionDetail {
	t.Helper()
	var detail sessionDetail
	if err := json.Unmarshal([]byte(event.Detail), &detail); err != nil {
		t.Fatalf("detail of %s = %q: %v", event.Action, event.Detail, err)
	}
	return detail
}

func TestSignInsFailuresAndSignOutsAreRecordedWithTheirOrigin(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &viewingRecorder{}, accounts.Options{PasswordCost: cheapPasswords, Now: ticking()})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	lan := accounts.WithClientOrigin(ctx, accounts.ClientOrigin{Via: accounts.ViaLAN, Address: "172.18.45.12"})
	session, err := service.Authenticate(lan, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(lan, "alice", "wrong password guess"); err == nil {
		t.Fatal("wrong password accepted")
	}
	longName := strings.Repeat("x", 100)
	if _, err := service.Authenticate(lan, longName, "anything at all here"); err == nil {
		t.Fatal("unknown account accepted")
	}
	loopback := accounts.WithClientOrigin(ctx, accounts.ClientOrigin{Via: accounts.ViaLoopback})
	console, err := service.AuthenticateLocalConsole(loopback, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EndSession(lan, session.Token); err != nil {
		t.Fatal(err)
	}
	if err := service.EndSession(loopback, console.Token); err != nil {
		t.Fatal(err)
	}
	if err := service.DisableUser(ctx, admin, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(lan, "alice", "alice password for testing"); err == nil {
		t.Fatal("disabled account accepted")
	}

	events, err := service.Activity(ctx, admin, activity.Query{Categories: []activity.Category{activity.CategorySession}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	type summary struct{ actor, action, via, address, username, reason string }
	var got []summary
	for _, event := range slices.Backward(events) {
		detail := detailOf(t, event)
		got = append(got, summary{event.ActorUserID, event.Action, detail.Via, detail.Address, detail.Username, detail.Reason})
	}
	want := []summary{
		{alice.ID, "session.signed_in", "lan", "172.18.45.12", "", ""},
		{alice.ID, "session.sign_in_failed", "lan", "172.18.45.12", "", "invalid_password"},
		{"", "session.sign_in_failed", "lan", "172.18.45.12", strings.Repeat("x", 64), "unknown_user"},
		{admin.ID, "session.signed_in", "local-console", "", "", ""},
		{alice.ID, "session.signed_out", "lan", "172.18.45.12", "", ""},
		{admin.ID, "session.signed_out", "local-console", "", "", ""},
		{alice.ID, "session.sign_in_failed", "lan", "172.18.45.12", "", "disabled"},
	}
	if len(got) != len(want) {
		t.Fatalf("session events = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("session event %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestMembersOnlySeeActivityAboutThemselves(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &viewingRecorder{}, accounts.Options{PasswordCost: cheapPasswords, Now: ticking()})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := service.CreateMember(ctx, admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, "bob", "bob password for testing"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartViewing(ctx, admin, alice.ID, "correct horse battery staple", "find a lost photo"); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetCredential(ctx, admin, bob.ID, "bob reset password 1"); err != nil {
		t.Fatal(err)
	}

	// Alice asks for everyone's events; the service still narrows to hers.
	events, err := service.Activity(ctx, alice, activity.Query{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, event := range events {
		actions = append(actions, event.Action)
		if event.ActorUserID == bob.ID || event.ResourceID == bob.ID {
			t.Fatalf("alice sees bob's event %+v", event)
		}
	}
	if got, want := strings.Join(actions, ","), "space.viewing_started,session.signed_in,user.created"; got != want {
		t.Fatalf("alice's activity = %s, want %s", got, want)
	}

	all, err := service.Activity(ctx, admin, activity.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= len(events) {
		t.Fatalf("administrator sees %d events, alice %d", len(all), len(events))
	}

	directory, err := service.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if directory.Users[alice.ID].Username != "alice" || directory.Users[admin.ID].Role != accounts.RoleAdmin {
		t.Fatalf("directory users = %+v", directory.Users)
	}
	aliceSpace := privateSpaceOf(t, service, alice)
	if space := directory.Spaces[aliceSpace]; space.OwnerUserID != alice.ID || space.Kind != accounts.SpaceKindPrivate {
		t.Fatalf("directory space = %+v", space)
	}
}

// ticking advances a second on every reading, so events never share a
// millisecond and the log order is the order they happened in.
func ticking() func() time.Time {
	now := time.Date(2026, time.October, 12, 8, 0, 0, 0, time.UTC)
	return func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}
