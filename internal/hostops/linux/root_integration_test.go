//go:build rootintegration

// Root integration tests run the Host Agent executor against real account,
// ACL, Btrfs, and Samba tools. They modify the host and must only run in a
// disposable privileged container; see docs/development/LOCAL_ENVIRONMENT.md.
package linux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
)

func TestMain(m *testing.M) {
	// The File Broker re-executes this binary as a per-user worker; it runs
	// as that user with an empty environment, so check for it first.
	if len(os.Args) == 3 && os.Args[1] == filebroker.WorkerArgument {
		if err := filebroker.RunWorker(os.Args[2]); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == filebroker.ProbeArgument {
		if err := filebroker.RunProbe(); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Geteuid() != 0 || os.Getenv("ANAS_ROOT_INTEGRATION") != "1" {
		// Refuse to touch a developer machine by accident.
		_, _ = os.Stderr.WriteString("root integration tests require root and ANAS_ROOT_INTEGRATION=1 in a disposable container\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestRootIdentityLifecycle(t *testing.T) {
	ctx := context.Background()
	resetAgentState(t)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: filepath.Join(t.TempDir(), "offline")})
	password := "owner password for testing"
	if err := executor.SetCredential(ctx, accounts.CredentialRequest{
		UserID: "user:owner", PrivateSpaceID: "space:owner", Username: "owner",
		Password: password, Role: accounts.RoleAdmin, UID: 20100, Enabled: true,
	}); err != nil {
		t.Fatalf("SetCredential(owner) error = %v", err)
	}
	if got := run(t, "getent", "passwd", "owner"); !strings.HasPrefix(got, "owner:x:20100:20100:") {
		t.Fatalf("owner passwd entry = %q", got)
	}
	groups := " " + run(t, "id", "--name", "--groups", "owner") + " "
	for _, group := range []string{"a-nas-users", "a-nas-admins"} {
		if !strings.Contains(groups, " "+group+" ") {
			t.Fatalf("owner groups = %q, missing %s", groups, group)
		}
	}
	if got := run(t, "getent", "group", "a-nas-users"); !strings.HasPrefix(got, "a-nas-users:x:20000:") {
		t.Fatalf("a-nas-users group = %q", got)
	}

	before := run(t, "getent", "passwd", "daemon")
	err := executor.SetCredential(ctx, accounts.CredentialRequest{
		UserID: "user:daemon", PrivateSpaceID: "space:daemon", Username: "daemon",
		Password: "daemon password for testing", Role: accounts.RoleMember, UID: 20101, Enabled: true,
	})
	if !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("SetCredential(daemon) error = %v, want ErrIdentityConflict", err)
	}
	if after := run(t, "getent", "passwd", "daemon"); after != before {
		t.Fatalf("system account changed: %q -> %q", before, after)
	}

	client := exec.Command("smbclient", "//127.0.0.1/IPC$", "--user=owner%"+password, "--client-protection=encrypt")
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = client.Process.Kill(); _ = client.Wait() })
	waitFor(t, "owner SMB session", func() bool {
		return strings.Contains(run(t, "smbstatus", "--processes", "--user=owner", "--json"), `"username": "owner"`)
	})
	if err := executor.DisableCredential(ctx, "owner"); err != nil {
		t.Fatalf("DisableCredential(owner) error = %v", err)
	}
	waitFor(t, "owner SMB session to end", func() bool {
		return !strings.Contains(run(t, "smbstatus", "--processes", "--user=owner", "--json"), `"username": "owner"`)
	})
	if output, err := exec.Command("smbclient", "//127.0.0.1/IPC$", "--user=owner%"+password,
		"--client-protection=encrypt", "--command=exit").CombinedOutput(); err == nil {
		t.Fatalf("disabled account can still open an SMB session: %s", output)
	}
}

// The Host Agent's records leave the Product Service's StateDirectory for
// one only root can open; a record the service account wrote is not used
// (issue #176).
func TestRootStateLeavesTheProductServiceDirectory(t *testing.T) {
	resetAgentState(t)
	if _, err := exec.Command("id", "a-nas").CombinedOutput(); err != nil {
		run(t, "useradd", "--system", "--no-create-home", "--home-dir", "/var/lib/a-nas", "--shell", "/usr/sbin/nologin", "a-nas")
	}
	run(t, "install", "-d", "-o", "a-nas", "-g", "a-nas", "-m", "0700", "/var/lib/a-nas")
	identities := `{"owner":{"uid":20100,"role":"admin","enabled":true}}` + "\n"
	writeFile(t, "/var/lib/a-nas/identity-registry.json", identities)
	if got := asUser("a-nas", "sh", "-c", `printf '{"jellyfin":30000}' > /var/lib/a-nas/app-identities.json`); !got.ok {
		t.Fatalf("plant a record as a-nas: %s", got.output)
	}

	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: filepath.Join(t.TempDir(), "offline")})
	if executor.identityError != nil || !executor.identityKnown("owner") {
		t.Fatalf("moved identity registry = %v, %v", executor.identities, executor.identityError)
	}
	if executor.appsError == nil || !strings.Contains(executor.appsError.Error(), "owned by") || len(executor.apps) != 0 {
		t.Fatalf("a-nas's app registry = %v, %v; want it refused", executor.apps, executor.appsError)
	}
	if got := run(t, "stat", "-c", "%U:%G %a", "/var/lib/a-nas-host"); got != "root:root 700" {
		t.Errorf("state directory = %s, want root:root 700", got)
	}
	if got := run(t, "stat", "-c", "%U:%G %a", "/var/lib/a-nas-host/identity-registry.json"); got != "root:root 600" {
		t.Errorf("moved registry = %s, want root:root 600", got)
	}
	if got := readFile(t, "/var/lib/a-nas-host/identity-registry.json"); got != identities {
		t.Errorf("moved registry = %q, want %q", got, identities)
	}
	if _, err := os.Lstat("/var/lib/a-nas/identity-registry.json"); !os.IsNotExist(err) {
		t.Errorf("the original stayed in the Product Service's directory: %v", err)
	}
	if _, err := os.Lstat("/var/lib/a-nas-host/app-identities.json"); !os.IsNotExist(err) {
		t.Errorf("a-nas's record was moved: %v", err)
	}
	for _, command := range [][]string{
		{"ls", "/var/lib/a-nas-host"},
		{"sh", "-c", "printf '{}' > /var/lib/a-nas-host/space-registry.json"},
	} {
		if got := asUser("a-nas", command...); got.ok {
			t.Errorf("a-nas ran %v in the Host Agent's directory: %s", command, got.output)
		}
	}
}

// A refused identity registry leaves the spaces' ACLs and the registry's copy
// on the data volume as they were: without it, every private space would get
// the fail-closed ACL and the copy would be emptied.
func TestRootRefusedIdentityRegistryLeavesSpacesAlone(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	if err := executor.SetCredential(ctx, accounts.CredentialRequest{
		UserID: "user:regcheck", PrivateSpaceID: "space:regcheck", Username: "regcheck",
		Password: "regcheck password for tests", Role: accounts.RoleMember, UID: 20190, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(mount, "spaces", "private", "regcheck")
	manifest := filepath.Join(mount, identityManifestName)
	aclBefore := run(t, "getfacl", "--omit-header", "--absolute-names", private)
	manifestBefore := readFile(t, manifest)
	writeFile(t, "/var/lib/a-nas-host/identity-registry.json",
		`{"regcheck":{"uid":20190,"role":"member","enabled":true},"regcheck2":{"uid":20190,"role":"member","enabled":true}}`)

	restarted := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	if restarted.identityError == nil {
		t.Fatal("an identity registry with one UID twice loaded")
	}
	if _, err := restarted.ReconcileDataVolume(ctx); err == nil {
		t.Error("ReconcileDataVolume() with a refused identity registry succeeded")
	}
	if err := restarted.DisableCredential(ctx, "regcheck"); err != nil {
		t.Errorf("DisableCredential() error = %v", err)
	}
	if got := run(t, "getfacl", "--omit-header", "--absolute-names", private); got != aclBefore {
		t.Errorf("private space ACL changed:\n%s\nwant:\n%s", got, aclBefore)
	}
	if got := readFile(t, manifest); got != manifestBefore {
		t.Errorf("identity manifest = %q, want it unchanged (%q)", got, manifestBefore)
	}
}

// resetAgentState gives each test fresh Host Agent registries; the container
// is disposable, so nothing else depends on them.
func resetAgentState(t *testing.T) {
	t.Helper()
	for _, directory := range []string{"/var/lib/a-nas", "/var/lib/a-nas-host"} {
		if err := os.RemoveAll(directory); err != nil {
			t.Fatal(err)
		}
	}
}

func startSamba(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll("/etc/samba", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/etc/samba/smb.conf", []byte("[global]\n    server role = standalone server\n    smb ports = 445\n    disable netbios = yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("pgrep", "-x", "smbd").CombinedOutput(); err == nil && len(output) != 0 {
		return
	}
	run(t, "smbd", "--daemon")
	waitFor(t, "smbd", func() bool { return exec.Command("smbcontrol", "smbd", "ping").Run() == nil })
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
