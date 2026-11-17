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
	"time"

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
	// The root tests share one container's accounts: these names and UIDs
	// are this test's alone.
	for i, user := range []struct {
		name string
		role accountid.Role
	}{{"rmkeeper", accountid.RoleAdmin}, {"rmalice", accountid.RoleMember}, {"rmbob", accountid.RoleMember}} {
		if err := executor.SetCredential(ctx, accountid.CredentialRequest{
			UserID: "user:" + user.name, PrivateSpaceID: "space:" + user.name, Username: user.name,
			Password: user.name + " password for tests", Role: user.role, UID: 20170 + i, Enabled: true,
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
	alicePrivate := filepath.Join(mount, "spaces", "private", "rmalice")
	keeperPrivate := filepath.Join(mount, "spaces", "private", "rmkeeper")
	expect("rmalice fills her space", true, asUser("rmalice", "sh", "-c", strings.Join([]string{
		"mkdir " + alicePrivate + "/docs",
		"echo a > " + alicePrivate + "/docs/a.txt",
		"ln -s /etc/shadow " + alicePrivate + "/link",
		"echo deleted > " + alicePrivate + "/.a-nas-trash/rmalice/old.txt",
		"echo deleted > " + shared + "/.a-nas-trash/rmalice/shared-old.txt",
		"echo agent > " + shared + "/Agent空间/rmalice/note.txt",
		"echo shared > " + shared + "/rmalice-shared.txt",
	}, " && ")))
	if err := executor.DisableCredential(ctx, "rmalice"); err != nil {
		t.Fatal(err)
	}
	// Something still runs as her, as a File Broker worker does for five idle
	// minutes; userdel would refuse the account.
	lingering := exec.Command("setpriv", "--reuid=rmalice", "--regid=rmalice", "--init-groups", "--", "sleep", "600")
	if err := lingering.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() {
		_ = lingering.Wait()
		close(ended)
	}()

	removal := accountid.AccountRemoval{
		Username: "rmalice", UID: 20171, PrivateSpaceID: "space:rmalice",
		TransferTo: "rmkeeper", TransferToUID: 20170, Folder: "rmalice 的文件",
	}
	if err := executor.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		_ = lingering.Process.Kill()
		t.Error("a process of the removed account still runs")
	}
	handed := filepath.Join(keeperPrivate, "rmalice 的文件")
	expect("rmkeeper reads the handed-over file", true, asUser("rmkeeper", "cat", filepath.Join(handed, "docs", "a.txt")))
	expect("rmkeeper writes into the handed-over folder", true, asUser("rmkeeper", "sh", "-c", "echo k > "+filepath.Join(handed, "docs", "k.txt")))
	expect("rmkeeper reads the Agent空间 note", true, asUser("rmkeeper", "cat", filepath.Join(handed, "Agent空间", "note.txt")))
	expect("rmbob cannot read the handed-over files", false, asUser("rmbob", "cat", filepath.Join(handed, "docs", "a.txt")))
	var stat syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(handed, "docs", "a.txt"), &stat); err != nil || stat.Uid != 20170 || stat.Gid != 20170 {
		t.Errorf("handed-over file owner = %d:%d, %v; want rmkeeper", stat.Uid, stat.Gid, err)
	}
	if target, err := os.Readlink(filepath.Join(handed, "link")); err != nil || target != "/etc/shadow" {
		t.Errorf("the link was not copied as a link: %q, %v", target, err)
	}
	expect("rmkeeper cannot read through the copied link", false, asUser("rmkeeper", "cat", filepath.Join(handed, "link")))
	gone("the trash", filepath.Join(handed, ".a-nas-trash"))
	gone("rmalice's personal space", alicePrivate)
	gone("rmalice's Shared trash", filepath.Join(shared, ".a-nas-trash", "rmalice"))
	gone("rmalice's Agent空间 folder", filepath.Join(shared, "Agent空间", "rmalice"))
	if err := syscall.Lstat(filepath.Join(shared, "rmalice-shared.txt"), &stat); err != nil || stat.Uid != 20171 {
		t.Errorf("rmalice's shared file = uid %d, %v; want kept with her UID", stat.Uid, err)
	}
	for _, lookup := range [][]string{{"getent", "passwd", "rmalice"}, {"getent", "group", "rmalice"}, {"pdbedit", "--user", "rmalice"}} {
		if exec.Command(lookup[0], lookup[1:]...).Run() == nil {
			t.Errorf("%v still finds rmalice", lookup)
		}
	}
	if executor.identityKnown("rmalice") {
		t.Error("rmalice is still in the identity registry")
	}
	if _, err := executor.privateSpaceRoot("space:rmalice"); err == nil {
		t.Error("rmalice's space is still registered")
	}
	if _, err := executor.RepairDataVolumePermissions(ctx); err != nil {
		t.Fatal(err)
	}
	gone("a personal space repaired back", alicePrivate)
	gone("a Shared trash folder repaired back", filepath.Join(shared, ".a-nas-trash", "rmalice"))

	// Sent again, as after a lost answer, it finds everything done.
	if err := executor.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
	gone("a second handover", filepath.Join(keeperPrivate, "rmalice 的文件 (2)"))

	// Deleting instead hands nothing over.
	if err := executor.RemoveAccount(ctx, accountid.AccountRemoval{Username: "rmbob", UID: 20172, PrivateSpaceID: "space:rmbob"}); err != nil {
		t.Fatalf("RemoveAccount(rmbob) error = %v", err)
	}
	gone("rmbob's personal space", filepath.Join(mount, "spaces", "private", "rmbob"))
	gone("a folder for rmbob", filepath.Join(keeperPrivate, "rmbob 的文件"))

	// The name can be used again, with a new UID and nothing of the old one.
	if err := executor.SetCredential(ctx, accountid.CredentialRequest{
		UserID: "user:rmalice-2", PrivateSpaceID: "space:rmalice-2", Username: "rmalice",
		Password: "new rmalice password", Role: accountid.RoleMember, UID: 20173, Enabled: true,
	}); err != nil {
		t.Fatalf("SetCredential(new rmalice) error = %v", err)
	}
	if entries, err := os.ReadDir(alicePrivate); err != nil || len(entries) != 1 || entries[0].Name() != ".a-nas-trash" {
		t.Errorf("the new rmalice's space = %v, %v; want only its trash", entries, err)
	}
	expect("the new rmalice cannot read the old one's shared file as its owner", false,
		asUser("rmalice", "sh", "-c", "test -O "+filepath.Join(shared, "rmalice-shared.txt")))
}
