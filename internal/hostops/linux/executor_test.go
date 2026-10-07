package linux_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

func TestCreateVolumeResolvesStableIDAndWritesUUIDMountUnit(t *testing.T) {
	diskID, _ := hoststate.NewResourceID("disk:test-data")
	disk := hoststate.Disk{
		ID: diskID, Model: "Test Data Disk", Transport: hoststate.TransportSATA,
		CapacityBytes: 512_000_000_000, Role: hoststate.DiskRoleUnassigned,
	}
	resolver := fixedResolver{device: linux.BlockDevice{Disk: disk, DevicePath: "/dev/sdb", PartitionPath: "/dev/sdb1"}}
	runner := &recordingRunner{}
	systemRoot := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "data")
	executor := linux.NewExecutor(resolver, runner, linux.Options{
		SystemRoot: systemRoot, MountPoint: mountPoint, MountUnitName: "a-nas-data.mount",
	})

	volume, err := executor.CreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:test", DiskID: disk.ID.String(), Fingerprint: storage.FingerprintDisk(disk),
	})
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if got, want := volume.FilesystemUUID, "11111111-2222-3333-4444-555555555555"; got != want {
		t.Fatalf("filesystem UUID = %q, want %q", got, want)
	}
	unitPath := filepath.Join(systemRoot, "etc", "systemd", "system", "a-nas-data.mount")
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read mount unit: %v", err)
	}
	if !strings.Contains(string(unit), "What=UUID=11111111-2222-3333-4444-555555555555") || !strings.Contains(string(unit), "Options=noatime,compress=zstd:3,nodev,nosuid,noexec") {
		t.Fatalf("mount unit does not bind the verified UUID and safe options:\n%s", unit)
	}
	if len(runner.commands) < 7 {
		t.Fatalf("commands = %#v, want destructive steps plus mount setup", runner.commands)
	}
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command.args, " "), "disk:test-data") {
			t.Fatalf("stable ID leaked into a host command: %#v", command)
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

type fixedResolver struct{ device linux.BlockDevice }

func (r fixedResolver) ResolveDevice(context.Context, string) (linux.BlockDevice, error) {
	return r.device, nil
}

type recordedCommand struct {
	name  string
	args  []string
	stdin string
}

type recordingRunner struct{ commands []recordedCommand }

func (r *recordingRunner) Run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	r.commands = append(r.commands, recordedCommand{name: name, args: append([]string(nil), args...), stdin: stdin})
	if name == "blkid" {
		return []byte("11111111-2222-3333-4444-555555555555\n"), nil
	}
	return nil, nil
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
