//go:build rootintegration

package linux

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TestRootTerminalRunsAsTheAdministrator starts a Web terminal shell
// through the File Broker and checks it runs as the administrator's own
// account rather than as root or the Product Service.
func TestRootTerminalRunsAsTheAdministrator(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	identities := map[string]accounts.Identity{}
	for i, user := range []struct {
		name string
		role accounts.Role
	}{{"keeper", accounts.RoleAdmin}, {"alice", accounts.RoleMember}} {
		if err := executor.SetCredential(ctx, accounts.CredentialRequest{
			UserID: "user:" + user.name, PrivateSpaceID: "space:" + user.name, Username: user.name,
			Password: user.name + " password for tests", Role: user.role, UID: 20110 + i, Enabled: true,
		}); err != nil {
			t.Fatalf("SetCredential(%s) error = %v", user.name, err)
		}
		identities[user.name+"-token"] = accounts.Identity{Username: user.name, UID: 20110 + i, Role: user.role, Enabled: true}
	}
	alicePrivate := filepath.Join(mount, "spaces", "private", "alice")
	if result := asUser("alice", "sh", "-c", "echo secret > "+filepath.Join(alicePrivate, "secret.txt")); !result.ok {
		t.Fatalf("alice writes a file: %s", result.output)
	}
	broker := startFileBroker(t, mount, executor, identities)

	if _, err := broker.StartShell(accounts.WithSessionToken(ctx, "alice-token"), 80, 24); err == nil {
		t.Fatal("a member opened a terminal")
	}
	shell, err := broker.StartShell(accounts.WithSessionToken(ctx, "keeper-token"), 80, 24)
	if err != nil {
		t.Fatalf("StartShell() error = %v", err)
	}
	defer shell.Close()
	script := "echo uid=$(id -u) groups=$(id -Gn) home=$(pwd); cat " + filepath.Join(alicePrivate, "secret.txt") + "; ls -l $(tty); exit\n"
	if _, err := shell.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	output := make(chan string, 1)
	go func() {
		contents, _ := io.ReadAll(shell)
		output <- string(contents)
	}()
	var transcript string
	select {
	case transcript = <-output:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal did not finish")
	}
	for _, want := range []string{
		"uid=20110", "a-nas-admins", "home=" + filepath.Join(mount, "spaces", "private", "keeper"), "Permission denied", " keeper tty ",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("terminal output is missing %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "\nsecret") {
		t.Errorf("the administrator's terminal read another member's file:\n%s", transcript)
	}
}
