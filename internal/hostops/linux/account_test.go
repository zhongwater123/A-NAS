package linux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

// disabledAlice is an executor that knows alice as a disabled member, with
// the assistant installed and, when keyed, the administrator's model key saved.
func disabledAlice(t *testing.T, keyed bool) (*linux.Executor, *fakeHost) {
	t.Helper()
	executor, host, _ := disabledAliceIn(t, keyed)
	return executor, host
}

// disabledAliceIn is disabledAlice with the system root it made.
func disabledAliceIn(t *testing.T, keyed bool) (*linux.Executor, *fakeHost, string) {
	t.Helper()
	root := installedAssistant(t)
	if keyed {
		store := filepath.Join(root, "etc", "credstore.encrypted")
		if err := os.MkdirAll(store, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "a-nas-assistant-deepseek"), []byte("encrypted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host := newFakeHost()
	host.samba["alice"] = true
	executor := linux.NewExecutor(nil, host, linux.Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.SyncIdentities(context.Background(), []accounts.Identity{{Username: "alice", UID: 20100, Role: accounts.RoleMember}}); err != nil {
		t.Fatal(err)
	}
	host.commands = nil
	return executor, host, root
}

func TestEnablingAnAccountUnlocksItAndGivesBackSMBAndTheAssistant(t *testing.T) {
	executor, host := disabledAlice(t, true)
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "alice", SMB: true}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"usermod --unlock alice",
		"pdbedit --user alice",
		"smbpasswd -e alice",
		"systemctl unmask " + strings.Join(assistantUnits, " "),
		"systemctl enable --now --quiet anas-assistant-api@alice.socket anas-assistant-curator@alice.timer",
	}
	if strings.Join(host.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(host.commands, "\n"), strings.Join(want, "\n"))
	}
}

func TestEnablingAnAssistantWithoutTheKeyOnlyUnmasksIt(t *testing.T) {
	executor, host := disabledAlice(t, false)
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "alice", SMB: true}); err != nil {
		t.Fatal(err)
	}
	for _, command := range host.commands {
		if strings.HasPrefix(command, "systemctl enable") {
			t.Fatalf("an assistant the layout never prepared was enabled: %v", host.commands)
		}
	}
	if !host.ran("systemctl unmask " + strings.Join(assistantUnits, " ")) {
		t.Fatalf("the units stay masked: %v", host.commands)
	}
}

func TestEnablingAnAccountThatMustChooseAPasswordOnlyUnlocksIt(t *testing.T) {
	executor, host := disabledAlice(t, true)
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(host.commands, "\n") != "usermod --unlock alice" {
		t.Fatalf("commands: %v", host.commands)
	}
}

// An account whose password Samba never received gets SMB with its next
// password change, as identity synchronization lets it; enabling it again
// does not fail on that.
func TestEnablingAnAccountWithoutASambaEntryStillEnablesTheRest(t *testing.T) {
	executor, host := disabledAlice(t, true)
	delete(host.samba, "alice")
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "alice", SMB: true}); err != nil {
		t.Fatal(err)
	}
	if host.ran("smbpasswd -e alice") {
		t.Fatalf("enabled a Samba entry that does not exist: %v", host.commands)
	}
	if !host.ran("systemctl enable --now --quiet anas-assistant-api@alice.socket anas-assistant-curator@alice.timer") {
		t.Fatalf("the assistant stays off: %v", host.commands)
	}
}

// The assistant is not part of the account: one that cannot be prepared leaves the account
// enabled, and the next synchronization or saving of the key tries again.
func TestEnablingAnAccountWhoseAssistantCannotBePreparedSucceeds(t *testing.T) {
	executor, host, root := disabledAliceIn(t, true)
	if err := os.Remove(filepath.Join(root, "usr", "lib", "a-nas", "assistant", "managed", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "alice", SMB: true}); err != nil {
		t.Fatalf("EnableCredential() = %v, want the account enabled", err)
	}
	if !host.ran("smbpasswd -e alice") || !host.ran("systemctl unmask "+strings.Join(assistantUnits, " ")) {
		t.Fatalf("commands: %v", host.commands)
	}
	if host.ran("systemctl enable --now --quiet anas-assistant-api@alice.socket anas-assistant-curator@alice.timer") {
		t.Fatalf("an assistant without its settings was turned on: %v", host.commands)
	}
}

// With the key saved, synchronization prepares an enabled account's assistant before the
// registry holds the account, from the UID it is given, and still records it.
func TestSynchronizingAnAccountTheRegistryDoesNotHoldYetRecordsIt(t *testing.T) {
	executor, host := disabledAlice(t, true)
	identities := []accounts.Identity{
		{Username: "alice", UID: 20100, Role: accounts.RoleMember},
		{Username: "dave", UID: 20102, Role: accounts.RoleMember, Enabled: true},
	}
	if err := executor.SyncIdentities(context.Background(), identities); err != nil {
		t.Fatalf("SyncIdentities() = %v", err)
	}
	if !host.ran("systemctl enable --now --quiet anas-assistant-api@dave.socket anas-assistant-curator@dave.timer") {
		t.Fatalf("dave's assistant was not prepared: %v", host.commands)
	}
	host.commands = nil
	// Recorded: enabling dave again knows the account.
	if err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "dave"}); err != nil {
		t.Fatalf("EnableCredential(dave) = %v, want a known account", err)
	}
}

func TestEnablingAnUnknownAccountFails(t *testing.T) {
	executor, host := disabledAlice(t, true)
	err := executor.EnableCredential(context.Background(), accountid.EnableRequest{Username: "mallory", SMB: true})
	if !errors.Is(err, accountid.ErrUserNotFound) || len(host.commands) != 0 {
		t.Fatalf("err = %v, commands = %v", err, host.commands)
	}
}

// An enable that stopped part way is finished by the next synchronization.
func TestSynchronizationEnablesWhatAnEnabledAccountNeeds(t *testing.T) {
	executor, host := disabledAlice(t, true)
	if err := executor.SyncIdentities(context.Background(), []accounts.Identity{{Username: "alice", UID: 20100, Role: accounts.RoleMember, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"usermod --unlock alice", "smbpasswd -e alice", "systemctl unmask " + strings.Join(assistantUnits, " ")} {
		if !host.ran(command) {
			t.Errorf("synchronization did not run %q:\n%s", command, strings.Join(host.commands, "\n"))
		}
	}
}

// A password change after a reset enables what every synchronization kept
// masked until then.
func TestSettingAnEnabledCredentialUnmasksTheAssistant(t *testing.T) {
	executor, host := disabledAlice(t, true)
	err := executor.SetCredential(context.Background(), accountid.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice", Password: "alice password 12",
		Role: accountid.RoleMember, UID: 20100, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"smbpasswd -e alice", "systemctl unmask " + strings.Join(assistantUnits, " ")} {
		if !host.ran(command) {
			t.Errorf("SetCredential did not run %q:\n%s", command, strings.Join(host.commands, "\n"))
		}
	}
}

func TestRemovingAnAccountNeedsTheDataVolumeAndAValidRequest(t *testing.T) {
	executor, host := disabledAlice(t, true)
	ctx := context.Background()
	valid := accountid.AccountRemoval{Username: "alice", UID: 20100, PrivateSpaceID: "space:alice"}
	if _, err := executor.RemoveAccount(ctx, valid); !errors.Is(err, accountid.ErrVolumeUnavailable) {
		t.Fatalf("removal with the volume offline: %v", err)
	}
	for _, invalid := range []accountid.AccountRemoval{
		{Username: "alice", UID: 100, PrivateSpaceID: "space:alice"},
		{Username: "alice", UID: 20100, PrivateSpaceID: "space:shared"},
		{Username: "alice", UID: 20100, PrivateSpaceID: "space:alice", TransferTo: "alice", TransferToUID: 20100, Folder: "alice 的文件"},
		{Username: "alice", UID: 20100, PrivateSpaceID: "space:alice", TransferTo: "owner", TransferToUID: 20101, Folder: "../escape"},
		{Username: "alice", UID: 20100, PrivateSpaceID: "space:alice", TransferTo: "owner", TransferToUID: 20101, Folder: ".hidden"},
	} {
		if _, err := executor.RemoveAccount(ctx, invalid); err == nil || errors.Is(err, accountid.ErrVolumeUnavailable) {
			t.Errorf("RemoveAccount(%+v) = %v, want an invalid request", invalid, err)
		}
	}
	if len(host.commands) != 0 {
		t.Fatalf("a refused removal ran %v", host.commands)
	}
}
