//go:build rootintegration

package linux

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TestRootAppIdentityAndFolders creates an app identity and its folders on
// a real Btrfs volume and checks what the app account can reach.
func TestRootAppIdentityAndFolders(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	if err := executor.SetCredential(ctx, accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "alice password for tests", Role: accounts.RoleMember, UID: 20111, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	expect := func(what string, allowed bool, result outcome) {
		t.Helper()
		if result.ok != allowed {
			t.Errorf("%s: allowed=%v, want %v\n%s", what, result.ok, allowed, result.output)
		}
	}
	appData := filepath.Join(mount, "apps", "memos", "data")
	media := filepath.Join(mount, "spaces", "shared", "Media")
	private := filepath.Join(mount, "spaces", "private", "alice")
	expect("alice writes her space", true, asUser("alice", "sh", "-c", "echo secret > "+filepath.Join(private, "secret.txt")))

	identity, err := executor.AppIdentity(ctx, "memos")
	if err != nil || identity.UID != 30000 {
		t.Fatalf("AppIdentity() = %+v, %v", identity, err)
	}
	if err := executor.PrepareApp(ctx, "memos", []string{appData, media}, true); err != nil {
		t.Fatalf("PrepareApp() error = %v", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(appData, &stat); err != nil || stat.Uid != 30000 || stat.Gid != 30000 {
		t.Fatalf("app data folder owner = %d:%d, %v; want app-memos", stat.Uid, stat.Gid, err)
	}
	if groups := run(t, "id", "--name", "--groups", "app-memos"); !strings.Contains(groups, "a-nas-users") {
		t.Fatalf("app-memos groups = %q, want Shared access through a-nas-users", groups)
	}
	expect("app writes its data", true, asContainer("app-memos", appData, "echo db > /mnt/db"))
	expect("app writes the Shared folder it mounts", true, asContainer("app-memos", media, "echo song > /mnt/song.txt"))
	expect("alice edits the app's Shared file", true, asUser("alice", "sh", "-c", "echo more >> "+filepath.Join(media, "song.txt")))
	expect("app reads alice's private space", false, asUser("app-memos", "cat", filepath.Join(private, "secret.txt")))
	expect("alice reads the app's private data", false, asUser("alice", "cat", filepath.Join(appData, "db")))
	expect("apps folder is not listable by members", false, asUser("alice", "ls", filepath.Join(mount, "apps")))
	if acl := asUser("root", "getfacl", "--omit-header", filepath.Join(media, "song.txt")).output; !strings.Contains(acl, "group:a-nas-users:rw") {
		t.Errorf("the app's Shared file did not inherit the Shared ACL:\n%s", acl)
	}

	if err := executor.ReleaseApp(ctx, "memos"); err != nil {
		t.Fatalf("ReleaseApp() error = %v", err)
	}
	expect("released app writes Shared", false, asContainer("app-memos", media, "echo more >> /mnt/song.txt"))
	expect("released app keeps its own data", true, asContainer("app-memos", appData, "cat /mnt/db"))
	if again, err := executor.AppIdentity(ctx, "memos"); err != nil || again != identity {
		t.Fatalf("identity after release = %+v, %v; want the same UID", again, err)
	}
}

// asContainer runs command as user with hostDir bind-mounted at /mnt in a
// private mount namespace, the way Docker gives a container a bind mount:
// the host folders above hostDir are not traversed.
func asContainer(user, hostDir, command string) outcome {
	return asUser("root", "unshare", "--mount", "--propagation", "private", "sh", "-c",
		`mount --bind "$1" /mnt && exec setpriv --reuid="$2" --regid="$2" --init-groups -- sh -c "$3"`,
		"container", hostDir, user, command)
}
