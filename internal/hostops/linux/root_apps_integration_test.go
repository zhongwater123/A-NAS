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

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/appid"
	appstoreagent "github.com/zhongwater123/A-NAS/internal/appstore/agent"
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
	if output, err := exec.Command("getent", "passwd", "app-memos").CombinedOutput(); err == nil {
		t.Fatalf("showing a plan created the account: %s", output)
	}
	if err := executor.PrepareApp(ctx, "memos", []string{appData, media}, true); err != nil {
		t.Fatalf("PrepareApp() error = %v", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(appData, &stat); err != nil || stat.Uid != 30000 || stat.Gid != 30000 {
		t.Fatalf("app data folder owner = %d:%d, %v; want app-memos", stat.Uid, stat.Gid, err)
	}
	// Docker gives a container only the groups of its Compose service, so the
	// host account's groups never matter; it keeps none.
	if groups := run(t, "id", "--name", "--groups", "app-memos"); strings.Contains(groups, "a-nas-users") {
		t.Fatalf("app-memos groups = %q, want none", groups)
	}
	expect("app writes its data", true, asContainer("app-memos", "", appData, "echo db > /mnt/db"))
	expect("app without the plan's Shared group writes Shared", false, asContainer("app-memos", "", media, "echo song > /mnt/song.txt"))
	expect("app with the plan's Shared group writes Shared", true, asContainer("app-memos", accounts.UsersGroup, media, "echo song > /mnt/song.txt"))
	expect("alice edits the app's Shared file", true, asUser("alice", "sh", "-c", "echo more >> "+filepath.Join(media, "song.txt")))
	expect("app reads alice's private space", false, asUser("app-memos", "cat", filepath.Join(private, "secret.txt")))
	expect("alice reads the app's private data", false, asUser("alice", "cat", filepath.Join(appData, "db")))
	expect("apps folder is not listable by members", false, asUser("alice", "ls", filepath.Join(mount, "apps")))
	if acl := asUser("root", "getfacl", "--omit-header", filepath.Join(media, "song.txt")).output; !strings.Contains(acl, "group:a-nas-users:rw") {
		t.Errorf("the app's Shared file did not inherit the Shared ACL:\n%s", acl)
	}

	// The app (or anyone who writes Shared) swaps a folder for a link; root
	// must not create or own anything through it.
	victim := t.TempDir()
	run(t, "ln", "-s", victim, filepath.Join(mount, "spaces", "shared", "Planted"))
	run(t, "ln", "-s", victim, filepath.Join(mount, "apps", "memos", "cache"))
	for _, folder := range []string{filepath.Join(mount, "spaces", "shared", "Planted", "x"), filepath.Join(mount, "apps", "memos", "cache", "x")} {
		if err := executor.PrepareApp(ctx, "memos", []string{folder}, true); err == nil {
			t.Errorf("PrepareApp(%s) through a planted link succeeded", folder)
		}
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Errorf("PrepareApp created %v through a planted link", entries)
	}
	if err := syscall.Stat(victim, &stat); err != nil || stat.Uid != 0 {
		t.Errorf("PrepareApp gave the link target to UID %d, %v", stat.Uid, err)
	}

	// An account that joined a-nas-users before ADR 0022 leaves it.
	run(t, "usermod", "--append", "--groups", accounts.UsersGroup, "app-memos")
	if err := executor.ReleaseApp(ctx, "memos"); err != nil {
		t.Fatalf("ReleaseApp() error = %v", err)
	}
	if groups := run(t, "id", "--name", "--groups", "app-memos"); strings.Contains(groups, "a-nas-users") {
		t.Fatalf("released app-memos groups = %q, want none", groups)
	}
	expect("released app keeps its own data", true, asContainer("app-memos", "", appData, "cat /mnt/db"))
	if again, err := executor.AppIdentity(ctx, "memos"); err != nil || again != identity {
		t.Fatalf("identity after release = %+v, %v; want the same UID", again, err)
	}
}

// The container agent reads app identities from the host's account database,
// where the Host Agent creates them, and accepts only accounts of that shape
// (issue #176).
func TestRootContainerAgentReadsAppIdentitiesFromTheHost(t *testing.T) {
	resetAgentState(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: filepath.Join(t.TempDir(), "offline")})
	identities := appstoreagent.SystemIdentities{}
	if identity, ok, err := identities.App("agentcheck"); ok || err != nil {
		t.Fatalf("App() before the account exists = %+v, %v, %v", identity, ok, err)
	}
	if unused, err := identities.Unused(30940); !unused || err != nil {
		t.Fatalf("Unused(30940) before the account exists = %v, %v", unused, err)
	}
	if err := executor.ensureAccount(context.Background(), "app-agentcheck", 30940); err != nil {
		t.Fatal(err)
	}
	identity, ok, err := identities.App("agentcheck")
	if want := (appid.Identity{Username: "app-agentcheck", UID: 30940, GID: 30940}); !ok || err != nil || identity != want {
		t.Fatalf("App() = %+v, %v, %v; want %+v", identity, ok, err, want)
	}
	if unused, err := identities.Unused(30940); unused || err != nil {
		t.Fatalf("Unused(30940) with the account = %v, %v", unused, err)
	}

	// Accounts the Host Agent would not create.
	run(t, "groupadd", "--gid", "30941", "agentcheck-group")
	run(t, "useradd", "--uid", "30941", "--gid", "30941", "--no-create-home", "--shell", "/usr/sbin/nologin", "app-othergroup")
	run(t, "useradd", "--uid", "20941", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", "app-memberrange")
	run(t, "useradd", "--uid", "30940", "--non-unique", "--gid", "30940", "--no-create-home", "--shell", "/usr/sbin/nologin", "app-shareduid")
	for _, id := range []string{"othergroup", "memberrange", "shareduid"} {
		if identity, ok, err := identities.App(id); !ok || err == nil {
			t.Errorf("App(%s) = %+v, %v, %v; want it refused", id, identity, ok, err)
		}
	}
}

// asContainer runs command as user with hostDir bind-mounted at /mnt in a
// private mount namespace, the way Docker gives a container a bind mount:
// the host folders above hostDir are not traversed. Like Docker, it gives
// the process only the groups its service lists (group_add), never the host
// account's.
func asContainer(user, groups, hostDir, command string) outcome {
	groupFlag := "--clear-groups"
	if groups != "" {
		groupFlag = "--groups=" + groups
	}
	return asUser("root", "unshare", "--mount", "--propagation", "private", "sh", "-c",
		`mount --bind "$1" /mnt && exec setpriv --reuid="$2" --regid="$2" "$3" -- sh -c "$4"`,
		"container", hostDir, user, groupFlag, command)
}
