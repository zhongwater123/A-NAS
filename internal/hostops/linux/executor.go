// Package linux implements privileged A-NAS host operations with fixed commands.
package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

type BlockDevice struct {
	Disk          hoststate.Disk
	DevicePath    string
	PartitionPath string
}

type DeviceResolver interface {
	ResolveDevice(context.Context, string) (BlockDevice, error)
	// MountedDataVolume is the Btrfs filesystem mounted at the data mount
	// point, with the disks the kernel lists for it, or nil.
	MountedDataVolume(context.Context) (*hoststate.DataVolume, error)
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

func (r HostStateResolver) MountedDataVolume(ctx context.Context) (*hoststate.DataVolume, error) {
	if r.Reader == nil {
		return nil, errors.New("Linux host-state reader is unavailable")
	}
	state, err := r.Reader.Read(ctx)
	if err != nil {
		return nil, err
	}
	return state.DataVolume, nil
}

// CommandRunner runs the fixed host commands with their arguments and
// standard input.
type CommandRunner interface {
	// Run returns what the command wrote to standard output and standard
	// error together, for commands whose output only explains a failure.
	Run(context.Context, string, []string, string) ([]byte, error)
	// Output returns only what the command wrote to standard output, for
	// commands whose output is parsed: Samba tools print warnings on standard
	// error even when they succeed. A failure's error carries standard error.
	Output(context.Context, string, []string, string) ([]byte, error)
}

type Options struct {
	SystemRoot    string
	MountPoint    string
	MountUnitName string
	SMBInterface  string
	Now           func() time.Time
	// Logger receives what background work, such as converting the data
	// volume, reports after the request that started it returned.
	Logger *slog.Logger
}

type Executor struct {
	resolver      DeviceResolver
	runner        CommandRunner
	systemRoot    string
	mountPoint    string
	mountUnitName string
	smbInterface  string
	// stateOwner owns the Host Agent's records: root, as which it runs.
	stateOwner int
	// stateFailures are the records that cannot be used, by path.
	stateFailures map[string]error
	spaceRootsMu  sync.RWMutex
	spaceRoots    map[string]string
	registryPath  string
	registryError error
	identitiesMu  sync.Mutex
	identities    map[string]identityRecord
	identityPath  string
	identityError error
	materializeMu sync.Mutex
	viewingMu     sync.Mutex
	viewing       map[string]viewingRecord
	viewingPath   string
	viewingError  error
	now           func() time.Time
	appsMu        sync.Mutex
	apps          map[string]int
	appsPath      string
	appsError     error
	logger        *slog.Logger
	conversionMu  sync.Mutex
	converting    bool
	// markerMu orders changes to the data-volume marker.
	markerMu sync.Mutex
	// operations is the journal of data-volume operations by plan ID.
	operationsMu    sync.Mutex
	operations      map[string]*volumeOperation
	operationsPath  string
	operationsError error
	// operating is the plan whose operation runs now; one runs at a time.
	operating string
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
		spaceRoots: make(map[string]string), identities: make(map[string]identityRecord),
		viewing: make(map[string]viewingRecord), now: options.Now, apps: make(map[string]int),
	}
	if executor.now == nil {
		executor.now = time.Now
	}
	executor.logger = options.Logger
	if executor.logger == nil {
		executor.logger = slog.Default()
	}
	stateDirectory := filepath.Join(executor.systemRoot, "var", "lib", stateDirectoryName)
	executor.registryPath = filepath.Join(stateDirectory, "space-registry.json")
	executor.identityPath = filepath.Join(stateDirectory, "identity-registry.json")
	executor.viewingPath = filepath.Join(stateDirectory, "viewing-grants.json")
	executor.appsPath = filepath.Join(stateDirectory, "app-identities.json")
	executor.operationsPath = filepath.Join(stateDirectory, "volume-operations.json")
	executor.stateOwner = os.Geteuid()
	executor.stateFailures = executor.prepareState(stateDirectory, filepath.Join(executor.systemRoot, "var", "lib", legacyStateDirectoryName), []string{
		executor.registryPath, executor.identityPath, executor.viewingPath, executor.appsPath, executor.operationsPath,
	})
	executor.registryError = executor.loadSpaceRegistry()
	// Viewing grants are checked against the identities, so those load first.
	executor.identityError = executor.loadIdentityRegistry()
	executor.viewingError = executor.loadViewingRegistry()
	executor.appsError = executor.loadAppsRegistry()
	executor.operationsError = executor.loadOperations()
	for path, err := range map[string]error{
		executor.registryPath: executor.registryError, executor.identityPath: executor.identityError,
		executor.viewingPath: executor.viewingError, executor.appsPath: executor.appsError, executor.operationsPath: executor.operationsError,
	} {
		if err != nil {
			executor.logger.Error("a Host Agent record cannot be used; the operations that need it fail", "file", path, "error", err)
		}
	}
	return executor
}

func (e *Executor) SetCredential(ctx context.Context, request accountid.CredentialRequest) error {
	if !accountid.ValidUsername(request.Username) || strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.PrivateSpaceID) == "" || !validSambaPassword(request.Password) {
		return errors.New("invalid credential request")
	}
	identity := accountid.Identity{Username: request.Username, UID: request.UID, Role: request.Role, Enabled: request.Enabled}
	if err := validIdentity(identity); err != nil {
		return err
	}
	if e.smbInterface != "" && !smbInterfacePattern.MatchString(e.smbInterface) {
		return errors.New("invalid Samba interface")
	}
	if e.identityError != nil {
		return e.identityError
	}
	if err := e.ensureFixedGroups(ctx); err != nil {
		return err
	}
	if err := e.ensureIdentity(ctx, identity); err != nil {
		return err
	}
	if err := e.recordIdentity(identity); err != nil {
		return err
	}
	if err := e.mirrorIdentityManifest(); err != nil {
		return err
	}
	privateRoot := filepath.Join(e.mountPoint, "spaces", "private", request.Username)
	sharedRoot := filepath.Join(e.mountPoint, "spaces", "shared")
	if err := e.registerSpaces(map[string]string{request.PrivateSpaceID: privateRoot, "space:shared": sharedRoot}); err != nil {
		return err
	}
	if e.dataVolumeReady() {
		if _, err := e.materializeRegisteredSpaces(ctx); err != nil {
			return err
		}
	}
	passwordInput := request.Password + "\n" + request.Password + "\n"
	if _, err := e.runner.Run(ctx, "smbpasswd", []string{"-s", "-a", request.Username}, passwordInput); err != nil {
		return fmt.Errorf("set Samba password: %w", err)
	}
	if !request.Enabled {
		if output, err := e.runner.Run(ctx, "smbpasswd", []string{"-d", request.Username}, ""); err != nil {
			return commandError("keep Samba credential disabled", err, output)
		}
	}
	return e.applySambaConfiguration(ctx)
}

// DataVolumeReady reports whether the intended Btrfs data volume, with its
// identity marker, is mounted at the mount point.
func (e *Executor) DataVolumeReady() bool { return e.dataVolumeReady() }

func (e *Executor) dataVolumeReady() bool {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(e.mountPoint, &stats); err != nil || uint64(stats.Type) != 0x9123683e {
		return false
	}
	contents, err := os.ReadFile(filepath.Join(e.mountPoint, ".a-nas-volume.json"))
	return err == nil && len(contents) != 0
}

// ReconcileDataVolume repairs and materializes registered spaces when the
// intended Btrfs data volume is already mounted, then refreshes the Samba
// configuration. It is safe to call during Host Agent startup: an absent or
// offline data volume is left untouched. It returns the pre-existing
// directories whose ownership or ACL had drifted and were repaired.
func (e *Executor) ReconcileDataVolume(ctx context.Context) ([]string, error) {
	repaired, err := e.RepairDataVolumePermissions(ctx)
	if err != nil {
		return repaired, err
	}
	if err := e.mirrorIdentityManifest(); err != nil {
		return repaired, err
	}
	return repaired, e.applySambaConfiguration(ctx)
}

// RepairDataVolumePermissions reapplies the ACL layout of ADR 0008 to space
// roots, trash directories, and their containers. Only those directories
// carry policy; contents inherit default ACLs and are never walked.
func (e *Executor) RepairDataVolumePermissions(ctx context.Context) ([]string, error) {
	if e.registryError != nil {
		return nil, e.registryError
	}
	if !e.dataVolumeReady() {
		return nil, nil
	}
	return e.materializeRegisteredSpaces(ctx)
}

func (e *Executor) DisableCredential(ctx context.Context, username string) error {
	if !accountid.ValidUsername(username) {
		return errors.New("invalid username")
	}
	// Both boundaries must be closed, even if one subsystem is unavailable.
	return errors.Join(e.disableHostCredential(ctx, username), e.disableAssistant(ctx, username))
}

func (e *Executor) disableHostCredential(ctx context.Context, username string) error {
	if _, err := e.runner.Run(ctx, "smbpasswd", []string{"-d", username}, ""); err != nil {
		return fmt.Errorf("disable Samba credential: %w", err)
	}
	if _, err := e.runner.Run(ctx, "usermod", []string{"--lock", username}, ""); err != nil {
		return fmt.Errorf("lock Samba account: %w", err)
	}
	if err := e.disconnectSMBSessions(ctx, username); err != nil {
		return err
	}
	if err := e.setIdentityEnabled(username, false); err != nil {
		return err
	}
	return e.mirrorIdentityManifest()
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
%s
[Shared]
    comment = A-NAS shared space
    path = %s/spaces/shared
    valid users = @%s
    read only = no
    browseable = yes
    access based share enum = yes
%s`, interfaces, e.mountPoint, sambaShareOptions, e.mountPoint, accountid.UsersGroup, sambaShareOptions)
	configurationPath := filepath.Join(e.systemRoot, "etc", "samba", "smb.conf")
	candidatePath := configurationPath + ".candidate"
	if err := writeAtomic(candidatePath, []byte(configuration), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(candidatePath) }()
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

// sambaShareOptions keep the kernel's POSIX ACLs authoritative: Samba runs as
// the signed-in user, new files inherit default ACLs, clients cannot edit
// ACLs, and recycle moves deletions into the per-user trash directory that
// the Host Agent prepares (ADR 0008). The create and directory masks become
// the ACL mask of new entries; Samba's default 0744 would make every
// inherited write grant read-only. Clients cannot reach the trash directories
// by name, so a member cannot rename the Shared trash root out of the way;
// recycle itself is not subject to veto files.
const sambaShareOptions = `    inherit acls = yes
    create mask = 0660
    directory mask = 0770
    nt acl support = no
    hide unreadable = yes
    veto files = /.a-nas-trash/
    vfs objects = recycle
    recycle:repository = .a-nas-trash/%U
    recycle:directory_mode = 0770
    recycle:subdir_mode = 0770
    recycle:keeptree = yes
    recycle:versions = yes
`

var (
	smbInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	deviceNamePattern   = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
)

// validSambaPassword reports whether smbpasswd -s receives password whole: it
// reads each copy as one line of C text into a 256-byte buffer, so a newline,
// NUL or longer password would set a different password than the Web one.
func validSambaPassword(password string) bool {
	return password != "" && len(password) <= 254 && utf8.ValidString(password) &&
		!strings.ContainsFunc(password, unicode.IsControl)
}

func commandError(action string, err error, output []byte) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}

func (e *Executor) loadSpaceRegistry() error {
	contents, err := e.readState(e.registryPath)
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
	// A registry that fails a check is not used at all.
	for spaceID, root := range roots {
		if !e.validSpace(spaceID, root) {
			return fmt.Errorf("space registry: %s is not a space root", root)
		}
	}
	if roots != nil {
		e.spaceRoots = roots
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
		if !e.validSpace(spaceID, root) {
			return errors.New("cannot register invalid space path")
		}
		e.spaceRoots[spaceID] = root
	}
	contents, err := json.MarshalIndent(e.spaceRoots, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return writeAtomic(e.registryPath, contents, 0o600)
}

// validSpace reports whether root is where a space with the ID lives: the
// Shared folder, or a private space named after its owner (ADR 0008).
// Registered roots are kept as written, so they must be clean.
func (e *Executor) validSpace(spaceID, root string) bool {
	if strings.TrimSpace(spaceID) == "" || root != filepath.Clean(root) {
		return false
	}
	if spaceID == sharedSpaceID {
		return root == filepath.Join(e.mountPoint, "spaces", "shared")
	}
	return filepath.Dir(root) == filepath.Join(e.mountPoint, "spaces", "private") && accountid.ValidUsername(filepath.Base(root))
}

func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validDevicePath(path string) bool {
	if filepath.Dir(path) != "/dev" {
		return false
	}
	return deviceNamePattern.MatchString(filepath.Base(path))
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	//nolint:gosec // G301: parents are system directories such as /etc/samba and /etc/systemd/system; mode protects the file.
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
	// The rename is on disk only once the directory is: records such as the
	// operation journal must survive a power cut right after they are written.
	return syncDirectory(filepath.Dir(path))
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return hostCommand(ctx, name, args, stdin).CombinedOutput()
}

func (execCommandRunner) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	output, err := hostCommand(ctx, name, args, stdin).Output()
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		if detail := bytes.TrimSpace(exitError.Stderr); len(detail) != 0 {
			err = fmt.Errorf("%w: %s", err, detail)
		}
	}
	return output, err
}

func hostCommand(ctx context.Context, name string, args []string, stdin string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	return command
}

var _ datavolume.VolumeExecutor = (*Executor)(nil)
var _ accountid.CredentialProvisioner = (*Executor)(nil)
var _ accountid.ViewingProvisioner = (*Executor)(nil)
var _ appid.Host = (*Executor)(nil)
