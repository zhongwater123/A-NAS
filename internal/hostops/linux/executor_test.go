package linux_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestCreateVolumeResolvesStableIDAndWritesUUIDMountUnit(t *testing.T) {
	host := newDiskHost(t, "disk:test-data")
	executor := host.executor()

	operation := host.finish(t, executor, startCreate(t, executor, "plan:test", storage.LayoutSingle, host.targets("disk:test-data")...))
	if operation.State != storage.OperationSucceeded || operation.Volume == nil {
		t.Fatalf("operation = %+v, want succeeded", operation)
	}
	if got, want := operation.Volume.FilesystemUUID, host.uuid(1); got != want {
		t.Fatalf("filesystem UUID = %q, want %q", got, want)
	}
	unit, err := os.ReadFile(filepath.Join(host.systemRoot, "etc", "systemd", "system", "a-nas-data.mount"))
	if err != nil {
		t.Fatalf("read mount unit: %v", err)
	}
	if !strings.Contains(string(unit), "What=UUID="+host.uuid(1)) || !strings.Contains(string(unit), "Options=noatime,compress=zstd:3,nodev,nosuid,noexec") {
		t.Fatalf("mount unit does not bind the verified UUID and safe options:\n%s", unit)
	}
	if len(host.lines()) < 7 {
		t.Fatalf("commands = %#v, want destructive steps plus mount setup", host.lines())
	}
	for _, line := range host.lines() {
		if strings.Contains(line, "disk:test-data") {
			t.Fatalf("stable ID leaked into a host command: %q", line)
		}
	}
}

func TestSetCredentialKeepsPasswordOutOfArgumentsAndAppliesHardenedSambaConfig(t *testing.T) {
	runner := &recordingRunner{}
	systemRoot := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "data")
	executor := linux.NewExecutor(nil, runner, linux.Options{
		SystemRoot: systemRoot, MountPoint: mountPoint, SMBInterface: "enp3s0",
	})
	request := accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "alice password for testing", Role: accounts.RoleMember, UID: 20100, Enabled: true,
	}

	if err := executor.SetCredential(context.Background(), request); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	var passwordWasPiped bool
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command.args, " "), request.Password) {
			t.Fatalf("password appeared in command arguments: %#v", command)
		}
		if command.name == "smbpasswd" && command.stdin == request.Password+"\n"+request.Password+"\n" {
			passwordWasPiped = true
		}
		if command.name == "btrfs" {
			t.Fatalf("credential setup attempted to materialize a space before the data volume was mounted: %#v", command)
		}
	}
	if !passwordWasPiped {
		t.Fatalf("smbpasswd did not receive the password only over stdin: %#v", runner.commands)
	}
	configuration, err := os.ReadFile(filepath.Join(systemRoot, "etc", "samba", "smb.conf"))
	if err != nil {
		t.Fatalf("read Samba configuration: %v", err)
	}
	for _, required := range []string{
		"server min protocol = SMB3_00", "map to guest = Never", "smb encrypt = required",
		"interfaces = lo enp3s0", "[homes]", "[Shared]", "vfs objects = recycle",
		"inherit acls = yes", "recycle:directory_mode = 0770", "recycle:subdir_mode = 0770",
	} {
		if !strings.Contains(string(configuration), required) {
			t.Fatalf("Samba configuration is missing %q:\n%s", required, configuration)
		}
	}
}

func TestSetCredentialRefusesPasswordsSmbpasswdWouldCut(t *testing.T) {
	for name, password := range map[string]string{
		"newline":  "short\nshort\nthen any text",
		"NUL":      "short\x00then any text",
		"too long": strings.Repeat("x", 255),
		"empty":    "",
	} {
		runner := &recordingRunner{}
		executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})
		err := executor.SetCredential(context.Background(), accounts.CredentialRequest{
			UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
			Password: password, Role: accounts.RoleMember, UID: 20100, Enabled: true,
		})
		if err == nil || len(runner.commands) != 0 {
			t.Errorf("%s password: error = %v, commands = %#v; want refusal before any command", name, err, runner.commands)
		}
	}
}

func TestSetCredentialReportsUseraddFailureWithoutIncludingThePassword(t *testing.T) {
	executor := linux.NewExecutor(nil, accountFailureRunner{}, linux.Options{
		SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data"), SMBInterface: "enp3s0",
	})
	request := accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "alice password for testing", Role: accounts.RoleAdmin, UID: 20100, Enabled: true,
	}

	err := executor.SetCredential(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "cannot lock /etc/passwd") {
		t.Fatalf("SetCredential() error = %v, want useradd diagnostic", err)
	}
	if strings.Contains(err.Error(), request.Password) {
		t.Fatalf("SetCredential() error exposed the password: %v", err)
	}
}

type recordedCommand struct {
	name  string
	args  []string
	stdin string
}

type recordingRunner struct{ commands []recordedCommand }

// lines are the recorded commands as command lines.
func (r *recordingRunner) lines() []string {
	lines := make([]string, len(r.commands))
	for i, command := range r.commands {
		lines[i] = strings.Join(append([]string{command.name}, command.args...), " ")
	}
	return lines
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	r.commands = append(r.commands, recordedCommand{name: name, args: append([]string(nil), args...), stdin: stdin})
	if name == "blkid" {
		return []byte("11111111-2222-3333-4444-555555555555\n"), nil
	}
	return nil, nil
}

// Output is Run: the recorded commands print nothing on standard error.
func (r *recordingRunner) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return r.Run(ctx, name, args, stdin)
}

type accountFailureRunner struct{}

func (accountFailureRunner) Run(_ context.Context, name string, _ []string, _ string) ([]byte, error) {
	switch name {
	case "getent":
		return nil, exitStatus(2)
	case "useradd":
		return []byte("useradd: cannot lock /etc/passwd; try again later\n"), exitStatus(1)
	default:
		return nil, nil
	}
}

func (r accountFailureRunner) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return r.Run(ctx, name, args, stdin)
}
