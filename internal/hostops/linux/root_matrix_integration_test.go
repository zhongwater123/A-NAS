//go:build rootintegration

package linux

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// TestRootPermissionMatrix checks the ADR 0008 layout on a real Btrfs volume
// with real Samba: SMB, local processes (the terminal entry point), and Web
// through the File Broker's per-user workers all run as the user, the Product
// Service account has no access, and the kernel decides every access.
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
	identities := map[string]accounts.Identity{}
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
		identities[user.name+"-token"] = accounts.Identity{Username: user.name, UID: 20110 + i, Role: user.role, Enabled: true}
	}
	web := startFileBroker(t, mount, executor, identities)
	as := func(user string) context.Context { return accounts.WithSessionToken(ctx, user+"-token") }
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
	webRead := func(user, path string) outcome {
		file, err := web.Open(as(user), path)
		if err != nil {
			return outcome{err.Error(), false}
		}
		defer file.Close()
		contents, err := io.ReadAll(file)
		return outcome{string(contents), err == nil}
	}
	webWrite := func(user, path string) outcome {
		file, temporary, err := web.CreateTemp(as(user), filepath.Dir(path), ".a-nas-upload-")
		if err != nil {
			return outcome{err.Error(), false}
		}
		_, writeErr := file.WriteString("written from Web\n")
		_ = file.Close()
		if writeErr == nil {
			writeErr = web.Rename(as(user), temporary, path)
		}
		if writeErr != nil {
			return outcome{writeErr.Error(), false}
		}
		return outcome{"", true}
	}
	ownerOf := func(path string) uint32 {
		var stat syscall.Stat_t
		if err := syscall.Lstat(path, &stat); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		return stat.Uid
	}

	// Personal space: owner via SMB, terminal, and Web; nobody else.
	expect("alice SMB writes her space", true, smb("alice", "alice", "put "+payload+" a.txt; mkdir docs; put "+payload+` docs\c.txt; put `+payload+" d.txt"))
	expect("alice terminal reads her file", true, asUser("alice", "cat", filepath.Join(private, "a.txt")))
	expect("alice terminal appends", true, asUser("alice", "sh", "-c", "echo more >> "+filepath.Join(private, "a.txt")))
	expect("alice Web reads her SMB file", true, webRead("alice", filepath.Join(private, "a.txt")))
	expect("alice Web uploads", true, webWrite("alice", filepath.Join(private, "web.txt")))
	if uid := ownerOf(filepath.Join(private, "web.txt")); uid != 20111 {
		t.Errorf("Web upload is owned by UID %d, want alice (20111)", uid)
	}
	expect("alice SMB reads the Web file", true, smb("alice", "alice", "get web.txt /dev/null"))
	expect("alice SMB renames", true, smb("alice", "alice", "rename a.txt b.txt"))
	expect("bob terminal lists alice's space", false, asUser("bob", "ls", private))
	expect("bob SMB opens alice's space", false, smb("bob", "alice", "ls"))
	expect("bob Web reads alice's file", false, webRead("bob", filepath.Join(private, "b.txt")))
	expect("admin terminal reads alice's file", false, asUser("keeper", "cat", filepath.Join(private, "b.txt")))
	expect("admin SMB opens alice's space", false, smb("keeper", "alice", "ls"))
	expect("admin Web reads alice's file", false, webRead("keeper", filepath.Join(private, "b.txt")))
	expect("Product Service reads alice's file", false, asUser("a-nas", "cat", filepath.Join(private, "b.txt")))
	expect("alice lists the private-space container", false, asUser("alice", "ls", filepath.Join(mount, "spaces", "private")))
	// The space root itself can be stat'ed through the traverse-only container,
	// as from a terminal; its contents cannot be listed or examined.
	if _, err := web.Scan(as("bob"), private, files.ScanSkip{}); !errors.Is(err, files.ErrForbidden) {
		t.Errorf("bob Web listing of alice's space error = %v, want ErrForbidden", err)
	}
	if _, err := web.Lstat(as("bob"), filepath.Join(private, "b.txt")); !errors.Is(err, files.ErrForbidden) {
		t.Errorf("bob Web stat inside alice's space error = %v, want ErrForbidden", err)
	}

	// Deletion: Web first, then SMB, both into .a-nas-trash/alice as alice.
	webTrash := filepath.Join(private, ".a-nas-trash", "alice", "trash0123", "content")
	if err := web.Mkdir(as("alice"), filepath.Dir(webTrash)); err != nil {
		t.Fatalf("alice Web trash container: %v", err)
	}
	if err := web.Rename(as("alice"), filepath.Join(private, "web.txt"), webTrash); err != nil {
		t.Fatalf("alice Web delete: %v", err)
	}
	expect("alice SMB deletes after Web", true, smb("alice", "alice", `del docs\c.txt; del d.txt`))
	for _, path := range []string{webTrash, filepath.Join(private, ".a-nas-trash", "alice", "docs", "c.txt"), filepath.Join(private, ".a-nas-trash", "alice", "d.txt")} {
		expect("alice Web reads trash item "+filepath.Base(path), true, webRead("alice", path))
	}
	if entries, err := web.Scan(as("alice"), filepath.Join(private, ".a-nas-trash", "alice"), files.ScanSkip{}); err != nil || len(entries) < 4 {
		t.Errorf("alice Web scan of her trash = %d entries, %v", len(entries), err)
	}
	if err := web.Rename(as("alice"), webTrash, filepath.Join(private, "restored.txt")); err != nil {
		t.Errorf("alice Web restore: %v", err)
	}
	expect("alice SMB reads the restored file", true, smb("alice", "alice", "get restored.txt /dev/null"))

	// Shared: everyone reads and writes; deleted files stay private to the deleter.
	expect("alice SMB writes Shared", true, smb("alice", "Shared", "put "+payload+" s.txt"))
	expect("bob terminal appends to alice's Shared file", true, asUser("bob", "sh", "-c", "echo bob >> "+filepath.Join(shared, "s.txt")))
	expect("bob Web reads alice's Shared file", true, webRead("bob", filepath.Join(shared, "s.txt")))
	expect("admin SMB reads Shared", true, smb("keeper", "Shared", "get s.txt /dev/null"))
	expect("bob Web uploads to Shared", true, webWrite("bob", filepath.Join(shared, "from-bob.txt")))
	expect("alice terminal appends to bob's Web file", true, asUser("alice", "sh", "-c", "echo alice >> "+filepath.Join(shared, "from-bob.txt")))
	expect("Product Service reads Shared", false, asUser("a-nas", "cat", filepath.Join(shared, "s.txt")))
	if acl := asUser("root", "getfacl", "--omit-header", filepath.Join(shared, "s.txt")).output; !strings.Contains(acl, "group:a-nas-users:rw") {
		t.Errorf("SMB-created Shared file did not inherit the folder ACL:\n%s", acl)
	}
	expect("bob SMB deletes in Shared", true, smb("bob", "Shared", "del s.txt"))
	if _, err := os.Stat(filepath.Join(shared, ".a-nas-trash", "bob", "s.txt")); err != nil {
		t.Errorf("Shared deletion is not in bob's trash: %v", err)
	}
	expect("alice lists bob's Shared trash", false, asUser("alice", "ls", filepath.Join(shared, ".a-nas-trash", "bob")))
	if _, err := web.Scan(as("alice"), filepath.Join(shared, ".a-nas-trash", "bob"), files.ScanSkip{}); !errors.Is(err, files.ErrForbidden) {
		t.Errorf("alice Web scan of bob's trash error = %v, want ErrForbidden", err)
	}
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
	expect("alice Web uploads to Media", true, webWrite("alice", filepath.Join(media, "web.txt")))
	expect("bob reads Media", true, asUser("bob", "cat", filepath.Join(media, "m.txt")))
	expect("bob Web reads Media", true, webRead("bob", filepath.Join(media, "web.txt")))
	expect("bob writes Media", false, asUser("bob", "sh", "-c", "echo bob >> "+filepath.Join(media, "m.txt")))
	expect("bob creates in Media", false, asUser("bob", "cp", payload, filepath.Join(media, "bob.txt")))
	expect("bob Web uploads to Media", false, webWrite("bob", filepath.Join(media, "bob-web.txt")))

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

// startFileBroker serves the File Broker with real per-user workers. The test
// binary is copied somewhere every user can execute, because the worker is
// this binary re-executed under the user's credentials.
func startFileBroker(t *testing.T, mount string, executor *Executor, identities map[string]accounts.Identity) *filebroker.Client {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	worker := "/usr/local/bin/anas-matrix-file-worker"
	run(t, "install", "-m", "0755", self, worker)
	server, err := filebroker.NewServer(filebroker.Config{
		Sessions: sessionTable(identities), VolumeRoot: mount, VolumeReady: executor.DataVolumeReady,
		Command: func(volumeRoot string) *exec.Cmd {
			return exec.Command(worker, filebroker.WorkerArgument, volumeRoot)
		},
		ProbeCommand: func() *exec.Cmd { return exec.Command(worker, filebroker.ProbeArgument) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.VerifyIdentitySwitch(); err != nil {
		t.Fatalf("VerifyIdentitySwitch() as root error = %v", err)
	}
	socket := filepath.Join(t.TempDir(), "file-broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
	})
	return filebroker.NewClient(socket)
}

type sessionTable map[string]accounts.Identity

func (s sessionTable) ResolveSessionIdentity(_ context.Context, token string) (accounts.Identity, error) {
	identity, ok := s[token]
	if !ok {
		return accounts.Identity{}, accounts.ErrSessionNotFound
	}
	return identity, nil
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
// out /srv/a-nas, including the Product Service account that must not reach
// any space.
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
