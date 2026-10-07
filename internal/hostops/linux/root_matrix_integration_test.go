//go:build rootintegration

package linux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TestRootPermissionMatrix checks the ADR 0008 layout on a real Btrfs volume
// with real Samba: SMB and local processes (the terminal entry point) run as
// the user, the transitional Product Service account stands in for Web until
// the File Broker exists, and the kernel decides every access.
func TestRootPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	// A volume from an earlier release is reconciled at Host Agent startup,
	// before any identity, and therefore any fixed group, exists.
	for _, group := range []string{"a-nas-admins", "a-nas-users"} {
		_ = exec.Command("groupdel", group).Run()
	}
	if err := executor.registerSpaces(map[string]string{sharedSpaceID: filepath.Join(mount, "spaces", "shared")}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ReconcileDataVolume(ctx); err != nil {
		t.Fatalf("ReconcileDataVolume() before any identity error = %v", err)
	}
	run(t, "getent", "group", "a-nas-users")
	passwords := map[string]string{"keeper": "keeper password for tests", "alice": "alice password for tests", "bob": "bob password for tests"}
	for i, user := range []struct {
		name string
		role accounts.Role
	}{{"keeper", accounts.RoleAdmin}, {"alice", accounts.RoleMember}, {"bob", accounts.RoleMember}} {
		if err := executor.SetCredential(ctx, accounts.CredentialRequest{
			UserID: "user:" + user.name, PrivateSpaceID: "space:" + user.name, Username: user.name,
			Password: passwords[user.name], Role: user.role, UID: 20110 + i, Enabled: true,
		}); err != nil {
			t.Fatalf("SetCredential(%s) error = %v", user.name, err)
		}
	}
	private := filepath.Join(mount, "spaces", "private", "alice")
	shared := filepath.Join(mount, "spaces", "shared")
	// t.TempDir is private to root; every test identity must read the payload.
	payloadDirectory, err := os.MkdirTemp("", "a-nas-matrix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(payloadDirectory) })
	payload := filepath.Join(payloadDirectory, "payload.txt")
	if err := os.WriteFile(payload, []byte("matrix payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payloadDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	smb := func(user, share, commands string) outcome {
		output, err := exec.Command("smbclient", "//127.0.0.1/"+share, "--user="+user+"%"+passwords[user],
			"--client-protection=encrypt", "--command="+commands).CombinedOutput()
		return outcome{string(output), err == nil && !strings.Contains(string(output), "NT_STATUS_")}
	}
	expect := func(what string, allowed bool, result outcome) {
		t.Helper()
		if result.ok != allowed {
			t.Errorf("%s: allowed=%v, want %v\n%s", what, result.ok, allowed, result.output)
		}
	}

	// Personal space: owner via SMB and terminal; Web (transitional a-nas).
	expect("alice SMB writes her space", true, smb("alice", "alice", "put "+payload+" a.txt; mkdir docs; put "+payload+` docs\c.txt; put `+payload+" d.txt"))
	expect("alice terminal reads her file", true, asUser("alice", "cat", filepath.Join(private, "a.txt")))
	expect("alice terminal appends", true, asUser("alice", "sh", "-c", "echo more >> "+filepath.Join(private, "a.txt")))
	expect("Web (a-nas) reads alice's SMB file", true, asUser("a-nas", "cat", filepath.Join(private, "a.txt")))
	expect("Web (a-nas) creates a file", true, asUser("a-nas", "cp", payload, filepath.Join(private, "web.txt")))
	expect("alice SMB reads the Web file", true, smb("alice", "alice", "get web.txt /dev/null"))
	expect("alice SMB renames", true, smb("alice", "alice", "rename a.txt b.txt"))
	expect("bob terminal lists alice's space", false, asUser("bob", "ls", private))
	expect("bob SMB opens alice's space", false, smb("bob", "alice", "ls"))
	expect("admin terminal reads alice's file", false, asUser("keeper", "cat", filepath.Join(private, "b.txt")))
	expect("admin SMB opens alice's space", false, smb("keeper", "alice", "ls"))
	expect("alice lists the private-space container", false, asUser("alice", "ls", filepath.Join(mount, "spaces", "private")))

	// Deletion: Web first, then SMB, both into .a-nas-trash/alice.
	webTrash := filepath.Join(private, ".a-nas-trash", "alice", "trash0123", "content")
	expect("Web (a-nas) deletes into the per-user trash", true, asUser("a-nas", "sh", "-c",
		"mkdir "+filepath.Dir(webTrash)+" && mv "+filepath.Join(private, "web.txt")+" "+webTrash))
	expect("alice SMB deletes after Web", true, smb("alice", "alice", `del docs\c.txt; del d.txt`))
	for _, path := range []string{webTrash, filepath.Join(private, ".a-nas-trash", "alice", "docs", "c.txt"), filepath.Join(private, ".a-nas-trash", "alice", "d.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("deleted file is not in the per-user trash: %v", err)
		}
		expect("Web (a-nas) reads trash item "+filepath.Base(path), true, asUser("a-nas", "cat", path))
	}
	expect("Web (a-nas) restores", true, asUser("a-nas", "mv", webTrash, filepath.Join(private, "restored.txt")))
	expect("alice SMB reads the restored file", true, smb("alice", "alice", "get restored.txt /dev/null"))

	// Shared: everyone reads and writes; deleted files stay private to the deleter.
	expect("alice SMB writes Shared", true, smb("alice", "Shared", "put "+payload+" s.txt"))
	expect("bob terminal appends to alice's Shared file", true, asUser("bob", "sh", "-c", "echo bob >> "+filepath.Join(shared, "s.txt")))
	expect("admin SMB reads Shared", true, smb("keeper", "Shared", "get s.txt /dev/null"))
	if acl := asUser("root", "getfacl", "--omit-header", filepath.Join(shared, "s.txt")).output; !strings.Contains(acl, "group:a-nas-users:rw") {
		t.Errorf("SMB-created Shared file did not inherit the folder ACL:\n%s", acl)
	}
	expect("bob SMB deletes in Shared", true, smb("bob", "Shared", "del s.txt"))
	if _, err := os.Stat(filepath.Join(shared, ".a-nas-trash", "bob", "s.txt")); err != nil {
		t.Errorf("Shared deletion is not in bob's trash: %v", err)
	}
	expect("alice lists bob's Shared trash", false, asUser("alice", "ls", filepath.Join(shared, ".a-nas-trash", "bob")))
	if listing := smb("alice", "Shared", "ls").output; strings.Contains(listing, ".a-nas-trash") {
		t.Errorf("SMB shows the trash container:\n%s", listing)
	}
	expect("alice SMB renames the Shared trash root", false, smb("alice", "Shared", "rename .a-nas-trash moved"))

	// An identity created by synchronization gets its Shared trash at once.
	if err := executor.SyncIdentities(ctx, []accounts.Identity{
		{Username: "keeper", UID: 20110, Role: accounts.RoleAdmin, Enabled: true},
		{Username: "alice", UID: 20111, Role: accounts.RoleMember, Enabled: true},
		{Username: "bob", UID: 20112, Role: accounts.RoleMember, Enabled: true},
		{Username: "carol", UID: 20113, Role: accounts.RoleMember, Enabled: true},
	}); err != nil {
		t.Fatalf("SyncIdentities() error = %v", err)
	}
	if info, err := os.Stat(filepath.Join(shared, ".a-nas-trash", "carol")); err != nil || !info.IsDir() {
		t.Errorf("synchronized identity has no Shared trash: %v", err)
	}

	// Whoever can write Shared (an app, or a member outside SMB) replaces the
	// trash root with a link; repair must not follow it.
	victim := filepath.Join(payloadDirectory, "victim")
	run(t, "mkdir", victim)
	expect("alice renames the Shared trash root locally", true, asUser("alice", "mv", filepath.Join(shared, ".a-nas-trash"), filepath.Join(shared, "moved")))
	run(t, "ln", "-s", victim, filepath.Join(shared, ".a-nas-trash"))
	if _, err := executor.RepairDataVolumePermissions(ctx); err != nil {
		t.Fatalf("RepairDataVolumePermissions() with a planted link error = %v", err)
	}
	if acl := run(t, "getfacl", "--omit-header", victim); strings.Contains(acl, "a-nas") {
		t.Errorf("repair followed the planted link:\n%s", acl)
	}
	if info, err := os.Lstat(filepath.Join(shared, ".a-nas-trash", "bob")); err != nil || !info.IsDir() {
		t.Errorf("repair did not recreate the Shared trash: %v", err)
	}
	run(t, "rm", "-rf", filepath.Join(shared, "moved"))
	if displaced, _ := filepath.Glob(filepath.Join(shared, ".a-nas-trash.displaced-*")); len(displaced) != 1 {
		t.Errorf("displaced entries = %v", displaced)
	} else {
		run(t, "rm", displaced[0])
	}

	// Read-only shared folder: readable by all, writable only by grantees.
	media := filepath.Join(mount, "spaces", "media")
	if _, _, err := executor.ensureDirectoryACL(ctx, media, sharedFolderACL("user:alice"), false); err != nil {
		t.Fatal(err)
	}
	expect("alice writes Media", true, asUser("alice", "cp", payload, filepath.Join(media, "m.txt")))
	expect("bob reads Media", true, asUser("bob", "cat", filepath.Join(media, "m.txt")))
	expect("bob writes Media", false, asUser("bob", "sh", "-c", "echo bob >> "+filepath.Join(media, "m.txt")))
	expect("bob creates in Media", false, asUser("bob", "cp", payload, filepath.Join(media, "bob.txt")))

	// Drift on a space root is detected and repaired.
	run(t, "setfacl", "--modify", "user:bob:rwx", private)
	run(t, "chmod", "0777", private)
	expect("bob reads alice's space after drift", true, asUser("bob", "ls", private))
	repaired, err := executor.RepairDataVolumePermissions(ctx)
	if err != nil {
		t.Fatalf("RepairDataVolumePermissions() error = %v", err)
	}
	if !slices.Contains(repaired, private) {
		t.Errorf("repaired = %v, want %s", repaired, private)
	}
	expect("bob lists alice's space after repair", false, asUser("bob", "ls", private))
	if again, _ := executor.RepairDataVolumePermissions(ctx); len(again) != 0 {
		t.Errorf("repair is not idempotent; second run repaired %v", again)
	}

	if output, _ := exec.Command("grep", "-ri", "purging", "/var/log/samba").CombinedOutput(); len(output) != 0 {
		t.Errorf("Samba recycle purged files instead of moving them:\n%s", output)
	}
}

type outcome struct {
	output string
	ok     bool
}

func asUser(user string, command ...string) outcome {
	if user != "root" {
		command = append([]string{"setpriv", "--reuid=" + user, "--regid=" + user, "--init-groups", "--"}, command...)
	}
	output, err := exec.Command(command[0], command[1:]...).CombinedOutput()
	return outcome{string(output), err == nil}
}

// setUpDataVolume mounts a loop-backed Btrfs volume the way the product lays
// out /srv/a-nas, including the Product Service account the transitional ACL
// entries name.
func setUpDataVolume(t *testing.T, mount string) {
	t.Helper()
	if _, err := exec.Command("id", "a-nas").CombinedOutput(); err != nil {
		run(t, "useradd", "--system", "--no-create-home", "--home-dir", "/var/lib/a-nas", "--shell", "/usr/sbin/nologin", "a-nas")
	}
	run(t, "install", "-d", "-o", "root", "-g", "a-nas", "-m", "0750", "/srv/a-nas", mount)
	image := filepath.Join(t.TempDir(), "data.img")
	run(t, "truncate", "--size=512M", image)
	run(t, "mkfs.btrfs", "--quiet", image)
	run(t, "mount", "-o", "loop", image, mount)
	t.Cleanup(func() {
		if os.Getenv("ANAS_ROOT_KEEP_VOLUME") != "1" {
			_ = exec.Command("umount", mount).Run()
		}
	})
	if err := os.WriteFile(filepath.Join(mount, ".a-nas-volume.json"), []byte(`{"filesystemUuid":"test","formatVersion":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
