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
)

func TestMain(m *testing.M) {
	if os.Geteuid() != 0 || os.Getenv("ANAS_ROOT_INTEGRATION") != "1" {
		// Refuse to touch a developer machine by accident.
		os.Stderr.WriteString("root integration tests require root and ANAS_ROOT_INTEGRATION=1 in a disposable container\n")
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

// resetAgentState gives each test fresh Host Agent registries; the container
// is disposable, so nothing else depends on them.
func resetAgentState(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll("/var/lib/a-nas"); err != nil {
		t.Fatal(err)
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
