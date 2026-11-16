//go:build rootintegration

package linux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// TestRootAccountRemoval removes a member on a real Btrfs volume with
// Samba: once handing the personal space to the administrator, once deleting
// it, and checks that the name can be used again.
func TestRootAccountRemoval(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	for i, user := range []struct {
		name string
		role accountid.Role
	}{{"keeper", accountid.RoleAdmin}, {"alice", accountid.RoleMember}, {"bob", accountid.RoleMember}} {
		if err := executor.SetCredential(ctx, accountid.CredentialRequest{
			UserID: "user:" + user.name, PrivateSpaceID: "space:" + user.name, Username: user.name,
			Password: user.name + " password for tests", Role: user.role, UID: 20120 + i, Enabled: true,
		}); err != nil {
			t.Fatalf("SetCredential(%s) error = %v", user.name, err)
		}
	}
	expect := func(what string, allowed bool, result outcome) {
		t.Helper()
		if result.ok != allowed {
			t.Errorf("%s: allowed=%v, want %v\n%s", what, result.ok, allowed, result.output)
		}
	}
	gone := func(what, path string) {
		t.Helper()
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", what, err)
		}
	}
	shared := filepath.Join(mount, "spaces", "shared")
	alicePrivate := filepath.Join(mount, "spaces", "private", "alice")
	keeperPrivate := filepath.Join(mount, "spaces", "private", "keeper")
	expect("alice fills her space", true, asUser("alice", "sh", "-c", strings.Join([]string{
		"mkdir " + alicePrivate + "/docs",
		"echo a > " + alicePrivate + "/docs/a.txt",
		"ln -s /etc/shadow " + alicePrivate + "/link",
		"echo deleted > " + alicePrivate + "/.a-nas-trash/alice/old.txt",
		"echo deleted > " + shared + "/.a-nas-trash/alice/shared-old.txt",
		"echo agent > " + shared + "/Agent空间/alice/note.txt",
		"echo shared > " + shared + "/alice-shared.txt",
	}, " && ")))
	if err := executor.DisableCredential(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	removal := accountid.AccountRemoval{
		Username: "alice", UID: 20121, PrivateSpaceID: "space:alice",
		TransferTo: "keeper", TransferToUID: 20120, Folder: "alice 的文件",
	}
	if err := executor.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	handed := filepath.Join(keeperPrivate, "alice 的文件")
	expect("keeper reads the handed-over file", true, asUser("keeper", "cat", filepath.Join(handed, "docs", "a.txt")))
	expect("keeper writes into the handed-over folder", true, asUser("keeper", "sh", "-c", "echo k > "+filepath.Join(handed, "docs", "k.txt")))
	expect("keeper reads the Agent空间 note", true, asUser("keeper", "cat", filepath.Join(handed, "Agent空间", "note.txt")))
	expect("bob cannot read the handed-over files", false, asUser("bob", "cat", filepath.Join(handed, "docs", "a.txt")))
	var stat syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(handed, "docs", "a.txt"), &stat); err != nil || stat.Uid != 20120 || stat.Gid != 20120 {
		t.Errorf("handed-over file owner = %d:%d, %v; want keeper", stat.Uid, stat.Gid, err)
	}
	if target, err := os.Readlink(filepath.Join(handed, "link")); err != nil || target != "/etc/shadow" {
		t.Errorf("the link was not copied as a link: %q, %v", target, err)
	}
	expect("keeper cannot read through the copied link", false, asUser("keeper", "cat", filepath.Join(handed, "link")))
	gone("the trash", filepath.Join(handed, ".a-nas-trash"))
	gone("alice's personal space", alicePrivate)
	gone("alice's Shared trash", filepath.Join(shared, ".a-nas-trash", "alice"))
	gone("alice's Agent空间 folder", filepath.Join(shared, "Agent空间", "alice"))
	if err := syscall.Lstat(filepath.Join(shared, "alice-shared.txt"), &stat); err != nil || stat.Uid != 20121 {
		t.Errorf("alice's shared file = uid %d, %v; want kept with her UID", stat.Uid, err)
	}
	for _, lookup := range [][]string{{"getent", "passwd", "alice"}, {"getent", "group", "alice"}, {"pdbedit", "--user", "alice"}} {
		if exec.Command(lookup[0], lookup[1:]...).Run() == nil {
			t.Errorf("%v still finds alice", lookup)
		}
	}
	if executor.identityKnown("alice") {
		t.Error("alice is still in the identity registry")
	}
	if _, err := executor.privateSpaceRoot("space:alice"); err == nil {
		t.Error("alice's space is still registered")
	}
	if _, err := executor.RepairDataVolumePermissions(ctx); err != nil {
		t.Fatal(err)
	}
	gone("a personal space repaired back", alicePrivate)
	gone("a Shared trash folder repaired back", filepath.Join(shared, ".a-nas-trash", "alice"))

	// Sent again, as after a lost answer, it finds everything done.
	if err := executor.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
	gone("a second handover", filepath.Join(keeperPrivate, "alice 的文件 (2)"))

	// Deleting instead hands nothing over.
	if err := executor.RemoveAccount(ctx, accountid.AccountRemoval{Username: "bob", UID: 20122, PrivateSpaceID: "space:bob"}); err != nil {
		t.Fatalf("RemoveAccount(bob) error = %v", err)
	}
	gone("bob's personal space", filepath.Join(mount, "spaces", "private", "bob"))
	gone("a folder for bob", filepath.Join(keeperPrivate, "bob 的文件"))

	// The name can be used again, with a new UID and nothing of the old one.
	if err := executor.SetCredential(ctx, accountid.CredentialRequest{
		UserID: "user:alice-2", PrivateSpaceID: "space:alice-2", Username: "alice",
		Password: "new alice password", Role: accountid.RoleMember, UID: 20123, Enabled: true,
	}); err != nil {
		t.Fatalf("SetCredential(new alice) error = %v", err)
	}
	if entries, err := os.ReadDir(alicePrivate); err != nil || len(entries) != 1 || entries[0].Name() != ".a-nas-trash" {
		t.Errorf("the new alice's space = %v, %v; want only its trash", entries, err)
	}
	expect("the new alice cannot read the old one's shared file as its owner", false,
		asUser("alice", "sh", "-c", "test -O "+filepath.Join(shared, "alice-shared.txt")))
}
