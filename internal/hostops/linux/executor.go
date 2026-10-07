// Package linux implements privileged A-NAS host operations with fixed commands.
package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

type BlockDevice struct {
	Disk          hoststate.Disk
	DevicePath    string
	PartitionPath string
}

type DeviceResolver interface {
	ResolveDevice(context.Context, string) (BlockDevice, error)
}

type HostStateResolver struct {
	Reader *linuxhoststate.Reader
}

func (r HostStateResolver) ResolveDevice(ctx context.Context, diskID string) (BlockDevice, error) {
	if r.Reader == nil {
		return BlockDevice{}, errors.New("Linux host-state reader is unavailable")
	}
	device, err := r.Reader.ResolveDevice(ctx, diskID)
	if err != nil {
		return BlockDevice{}, err
	}
	return BlockDevice{Disk: device.Disk, DevicePath: device.DevicePath, PartitionPath: device.PartitionPath}, nil
}

type CommandRunner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

type Options struct {
	SystemRoot    string
	MountPoint    string
	MountUnitName string
	SMBInterface  string
}

type Executor struct {
	resolver      DeviceResolver
	runner        CommandRunner
	systemRoot    string
	mountPoint    string
	mountUnitName string
	smbInterface  string
	spaceRootsMu  sync.RWMutex
	spaceRoots    map[string]string
	registryPath  string
	registryError error
}

func NewExecutor(resolver DeviceResolver, runner CommandRunner, options Options) *Executor {
	if runner == nil {
		runner = execCommandRunner{}
	}
	systemRoot := options.SystemRoot
	if systemRoot == "" {
		systemRoot = "/"
	}
	mountPoint := options.MountPoint
	if mountPoint == "" {
		mountPoint = "/srv/a-nas/data"
	}
	unitName := options.MountUnitName
	if unitName == "" {
		unitName = "srv-a\\x2dnas-data.mount"
	}
	executor := &Executor{
		resolver: resolver, runner: runner, systemRoot: filepath.Clean(systemRoot),
		mountPoint: filepath.Clean(mountPoint), mountUnitName: unitName, smbInterface: strings.TrimSpace(options.SMBInterface),
		spaceRoots: make(map[string]string),
	}
	executor.registryPath = filepath.Join(executor.systemRoot, "var", "lib", "a-nas", "space-registry.json")
	executor.registryError = executor.loadSpaceRegistry()
	return executor
}

func (e *Executor) SetCredential(ctx context.Context, request accounts.CredentialRequest) error {
	if !validUsername(request.Username) || strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.PrivateSpaceID) == "" || request.Password == "" {
		return errors.New("invalid credential request")
	}
	if e.smbInterface != "" && !regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`).MatchString(e.smbInterface) {
		return errors.New("invalid Samba interface")
	}
	if _, err := e.runner.Run(ctx, "groupadd", []string{"--force", "a-nas-members"}, ""); err != nil {
		return fmt.Errorf("ensure Samba group: %w", err)
	}
	if _, err := e.runner.Run(ctx, "id", []string{"--user", request.Username}, ""); err != nil {
		if _, err := e.runner.Run(ctx, "useradd", []string{
			"--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", request.Username,
		}, ""); err != nil {
			return fmt.Errorf("create locked Samba account: %w", err)
		}
	}
	if _, err := e.runner.Run(ctx, "usermod", []string{"--append", "--groups", "a-nas-members", request.Username}, ""); err != nil {
		return fmt.Errorf("add Samba group membership: %w", err)
	}
	privateRoot := filepath.Join(e.mountPoint, "spaces", "private", request.Username)
	sharedRoot := filepath.Join(e.mountPoint, "spaces", "shared")
	if err := e.registerSpaces(map[string]string{request.PrivateSpaceID: privateRoot, "space:shared": sharedRoot}); err != nil {
		return err
	}
	if e.dataVolumeReady() {
		if err := e.materializeRegisteredSpaces(ctx); err != nil {
			return err
		}
	}
	passwordInput := request.Password + "\n" + request.Password + "\n"
	if _, err := e.runner.Run(ctx, "smbpasswd", []string{"-s", "-a", request.Username}, passwordInput); err != nil {
		return fmt.Errorf("set Samba password: %w", err)
	}
	return e.applySambaConfiguration(ctx)
}

func (e *Executor) dataVolumeReady() bool {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(e.mountPoint, &stats); err != nil || uint64(stats.Type) != 0x9123683e {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(e.mountPoint, ".a-nas-volume.json"))
	return err == nil && len(contents) != 0
}

func (e *Executor) materializeRegisteredSpaces(ctx context.Context) error {
	e.spaceRootsMu.RLock()
	spaces := make(map[string]string, len(e.spaceRoots))
	for spaceID, root := range e.spaceRoots {
		spaces[spaceID] = root
	}
	e.spaceRootsMu.RUnlock()
	for spaceID, root := range spaces {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
				return err
			}
			if _, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", root}, ""); err != nil {
				return fmt.Errorf("create space subvolume: %w", err)
			}
			if err := os.MkdirAll(root, 0o770); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		owner := "root:a-nas-members"
		mode := "2770"
		if spaceID != "space:shared" {
			username := filepath.Base(root)
			if !validUsername(username) {
				return errors.New("registered private space has an invalid owner")
			}
			owner = username + ":a-nas"
			mode = "0770"
		}
		if _, err := e.runner.Run(ctx, "chown", []string{owner, root}, ""); err != nil {
			return fmt.Errorf("own space: %w", err)
		}
		if _, err := e.runner.Run(ctx, "chmod", []string{mode, root}, ""); err != nil {
			return fmt.Errorf("protect space: %w", err)
		}
	}
	return nil
}

func (e *Executor) DisableCredential(ctx context.Context, username string) error {
	if !validUsername(username) {
		return errors.New("invalid username")
	}
	if _, err := e.runner.Run(ctx, "smbpasswd", []string{"-d", username}, ""); err != nil {
		return fmt.Errorf("disable Samba credential: %w", err)
	}
	if _, err := e.runner.Run(ctx, "usermod", []string{"--lock", username}, ""); err != nil {
		return fmt.Errorf("lock Samba account: %w", err)
	}
	return nil
}

func (e *Executor) applySambaConfiguration(ctx context.Context) error {
	interfaces := "lo"
	if e.smbInterface != "" {
		interfaces += " " + e.smbInterface
	}
	configuration := fmt.Sprintf(`[global]
    server role = standalone server
    security = user
    map to guest = Never
    server min protocol = SMB3_00
    smb encrypt = required
    server signing = mandatory
    unix extensions = no
    interfaces = %s
    bind interfaces only = yes
    disable netbios = yes
    smb ports = 445

[homes]
    comment = A-NAS private space
    path = %s/spaces/private/%%S
    valid users = %%S
    read only = no
    browseable = no
    create mask = 0660
    directory mask = 0770
    vfs objects = recycle
    recycle:repository = .a-nas-trash/%%U
    recycle:keeptree = yes
    recycle:versions = yes

[Shared]
    comment = A-NAS shared space
    path = %s/spaces/shared
    valid users = @a-nas-members
    force group = a-nas-members
    read only = no
    browseable = yes
    create mask = 0660
    force create mode = 0660
    directory mask = 2770
    force directory mode = 2000
    vfs objects = recycle
    recycle:repository = .a-nas-trash/%%U
    recycle:keeptree = yes
    recycle:versions = yes
`, interfaces, e.mountPoint, e.mountPoint)
	configurationPath := filepath.Join(e.systemRoot, "etc", "samba", "smb.conf")
	candidatePath := configurationPath + ".candidate"
	if err := writeAtomic(candidatePath, []byte(configuration), 0o644); err != nil {
		return err
	}
	defer os.Remove(candidatePath)
	if output, err := e.runner.Run(ctx, "testparm", []string{"--suppress-prompt", candidatePath}, ""); err != nil {
		return fmt.Errorf("validate Samba configuration: %w: %s", err, strings.TrimSpace(string(output)))
	}
	previous, previousErr := os.ReadFile(configurationPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	if err := os.Rename(candidatePath, configurationPath); err != nil {
		return err
	}
	if output, err := e.runner.Run(ctx, "smbcontrol", []string{"all", "reload-config"}, ""); err != nil {
		if previousErr == nil {
			_ = writeAtomic(configurationPath, previous, 0o644)
		} else {
			_ = os.Remove(configurationPath)
		}
		return fmt.Errorf("reload Samba configuration: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validUsername(username string) bool {
	return regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`).MatchString(username)
}

func (e *Executor) loadSpaceRegistry() error {
	contents, err := os.ReadFile(e.registryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var roots map[string]string
	if err := json.Unmarshal(contents, &roots); err != nil {
		return fmt.Errorf("decode space registry: %w", err)
	}
	for spaceID, root := range roots {
		clean := filepath.Clean(root)
		if strings.TrimSpace(spaceID) == "" || !withinDirectory(e.mountPoint, clean) {
			return errors.New("space registry contains an invalid path")
		}
		e.spaceRoots[spaceID] = clean
	}
	return nil
}

func (e *Executor) registerSpaces(spaces map[string]string) error {
	if e.registryError != nil {
		return e.registryError
	}
	e.spaceRootsMu.Lock()
	defer e.spaceRootsMu.Unlock()
	for spaceID, root := range spaces {
		if strings.TrimSpace(spaceID) == "" || !withinDirectory(e.mountPoint, root) {
			return errors.New("cannot register invalid space path")
		}
		e.spaceRoots[spaceID] = filepath.Clean(root)
	}
	contents, err := json.MarshalIndent(e.spaceRoots, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return writeAtomic(e.registryPath, contents, 0o600)
}

func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (e *Executor) CreateVolume(ctx context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	if e.resolver == nil || strings.TrimSpace(request.PlanID) == "" || strings.TrimSpace(request.DiskID) == "" || strings.TrimSpace(request.Fingerprint) == "" {
		return storage.Volume{}, errors.New("invalid create-volume request")
	}
	device, err := e.resolver.ResolveDevice(ctx, request.DiskID)
	if err != nil {
		return storage.Volume{}, fmt.Errorf("resolve stable disk ID: %w", err)
	}
	if device.Disk.ID.String() != request.DiskID || storage.FingerprintDisk(device.Disk) != request.Fingerprint {
		return storage.Volume{}, storage.ErrDiskChanged
	}
	if device.Disk.Role != hoststate.DiskRoleUnassigned || device.Disk.InUse || device.Disk.Removable || (device.Disk.Transport != hoststate.TransportSATA && device.Disk.Transport != hoststate.TransportNVMe) {
		return storage.Volume{}, storage.ErrDiskNotEligible
	}
	if !validDevicePath(device.DevicePath) || !validDevicePath(device.PartitionPath) || device.DevicePath == device.PartitionPath {
		return storage.Volume{}, errors.New("resolved block-device path is invalid")
	}
	commands := []struct {
		name string
		args []string
	}{
		{name: "wipefs", args: []string{"--all", device.DevicePath}},
		{name: "parted", args: []string{"--script", device.DevicePath, "mklabel", "gpt", "mkpart", "primary", "btrfs", "1MiB", "100%"}},
		{name: "partprobe", args: []string{device.DevicePath}},
		{name: "udevadm", args: []string{"settle", "--timeout=10"}},
		{name: "mkfs.btrfs", args: []string{"--force", "--label", "ANAS_DATA", device.PartitionPath}},
	}
	for _, command := range commands {
		if _, err := e.runner.Run(ctx, command.name, command.args, ""); err != nil {
			return storage.Volume{}, fmt.Errorf("%s failed: %w", command.name, err)
		}
	}
	uuidBytes, err := e.runner.Run(ctx, "blkid", []string{"--output", "value", "--match-tag", "UUID", device.PartitionPath}, "")
	if err != nil {
		return storage.Volume{}, fmt.Errorf("read Btrfs UUID: %w", err)
	}
	uuid := strings.TrimSpace(string(uuidBytes))
	if !regexp.MustCompile(`^[0-9A-Fa-f-]{16,64}$`).MatchString(uuid) {
		return storage.Volume{}, errors.New("blkid returned an invalid filesystem UUID")
	}
	if err := os.MkdirAll(e.mountPoint, 0o750); err != nil {
		return storage.Volume{}, fmt.Errorf("create mount point: %w", err)
	}
	unit := fmt.Sprintf(`[Unit]
Description=A-NAS data volume
After=local-fs-pre.target
Before=local-fs.target

[Mount]
What=UUID=%s
Where=%s
Type=btrfs
Options=noatime,compress=zstd:3,nodev,nosuid,noexec
TimeoutSec=30

[Install]
WantedBy=local-fs.target
`, uuid, e.mountPoint)
	unitPath := filepath.Join(e.systemRoot, "etc", "systemd", "system", e.mountUnitName)
	if err := writeAtomic(unitPath, []byte(unit), 0o644); err != nil {
		return storage.Volume{}, fmt.Errorf("install data-volume mount unit: %w", err)
	}
	for _, command := range []struct {
		name string
		args []string
	}{
		{name: "systemctl", args: []string{"daemon-reload"}},
		{name: "systemctl", args: []string{"enable", "--now", e.mountUnitName}},
		{name: "btrfs", args: []string{"subvolume", "create", filepath.Join(e.mountPoint, "spaces")}},
	} {
		if _, err := e.runner.Run(ctx, command.name, command.args, ""); err != nil {
			return storage.Volume{}, fmt.Errorf("%s failed after formatting: %w", command.name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(e.mountPoint, ".a-nas-snapshots"), 0o700); err != nil {
		return storage.Volume{}, err
	}
	marker, err := json.Marshal(struct {
		FilesystemUUID string `json:"filesystemUuid"`
		DiskID         string `json:"diskId"`
		FormatVersion  int    `json:"formatVersion"`
	}{FilesystemUUID: uuid, DiskID: request.DiskID, FormatVersion: 1})
	if err != nil {
		return storage.Volume{}, err
	}
	if err := writeAtomic(filepath.Join(e.mountPoint, ".a-nas-volume.json"), append(marker, '\n'), 0o644); err != nil {
		return storage.Volume{}, fmt.Errorf("write data-volume identity marker: %w", err)
	}
	if err := e.materializeRegisteredSpaces(ctx); err != nil {
		return storage.Volume{}, fmt.Errorf("materialize registered spaces: %w", err)
	}
	return storage.Volume{
		ID: "volume:data", DiskID: request.DiskID, FilesystemUUID: uuid,
		CapacityBytes: device.Disk.CapacityBytes, State: storage.VolumeStateAvailable,
	}, nil
}

func validDevicePath(path string) bool {
	if filepath.Dir(path) != "/dev" {
		return false
	}
	return regexp.MustCompile(`^[A-Za-z0-9._+-]+$`).MatchString(filepath.Base(path))
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".a-nas-unit-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	return command.CombinedOutput()
}

var _ storage.VolumeExecutor = (*Executor)(nil)
var _ accounts.CredentialProvisioner = (*Executor)(nil)
