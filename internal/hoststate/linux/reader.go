// Package linux reads host state from stable Debian and Linux interfaces.
package linux

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	hostIdentityDomain = "a-nas:host:v1"
	diskIdentityDomain = "a-nas:disk:v1"
	dataMountPoint     = "/srv/a-nas/data"
)

type dependencies struct {
	root                  fs.FS
	hostname              func() (string, error)
	architecture          string
	now                   func() time.Time
	readBlockDevices      func(context.Context) ([]byte, error)
	readSMART             func(context.Context, string) ([]byte, error)
	readFilesystemUsage   func(string) (filesystemUsage, error)
	sleep                 func(context.Context, time.Duration) error
	readTaskServices      func(context.Context) ([]byte, error)
	readTaskBlockDevices  func(context.Context) ([]byte, error)
	openTaskIconRoots     func() []taskIconRoot
	readTaskExe           func(int) (string, error)
	resolveTaskExecutable func(string) string
}

// Reader observes a Debian host without changing its state.
type Reader struct {
	dependencies  dependencies
	metrics       metricsSampler
	deviceTasks   deviceTaskSampler
	workloadTasks workloadTaskSampler
	smart         smartCache
}

type ResolvedDevice struct {
	Disk          hoststate.Disk
	DevicePath    string
	PartitionPath string
}

// New returns a Reader backed by the local Linux filesystem and lsblk.
func New() *Reader {
	return newReader(dependencies{
		root:                  os.DirFS("/"),
		hostname:              os.Hostname,
		architecture:          runtime.GOARCH,
		now:                   time.Now,
		sleep:                 sleepContext,
		readTaskServices:      readRunningServiceProperties,
		readTaskBlockDevices:  readTaskBlockDevices,
		openTaskIconRoots:     openTaskIconRoots,
		readTaskExe:           readTaskExe,
		resolveTaskExecutable: resolveTaskExecutable,
		readBlockDevices: func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(
				ctx,
				"lsblk",
				"--json",
				"--bytes",
				"--tree",
				"--exclude", "7",
				"--output", "NAME,TYPE,SIZE,MODEL,TRAN,ROTA,RM,FSTYPE,MOUNTPOINTS,WWN,SERIAL",
			).Output()
		},
		readSMART: readSMARTCommand,
		readFilesystemUsage: func(path string) (filesystemUsage, error) {
			var stats syscall.Statfs_t
			if err := syscall.Statfs(path, &stats); err != nil {
				return filesystemUsage{}, err
			}
			blockSize := uint64(stats.Bsize)
			return filesystemUsage{
				total:     uint64(stats.Blocks) * blockSize,
				available: uint64(stats.Bavail) * blockSize,
				readOnly:  stats.Flags&stRDOnly != 0,
			}, nil
		},
	})
}

func newReader(dependencies dependencies) *Reader {
	return &Reader{dependencies: dependencies}
}

// Read returns one point-in-time observation of the local host.
func (r *Reader) Read(ctx context.Context) (hoststate.State, error) {
	if err := ctx.Err(); err != nil {
		return hoststate.State{}, err
	}

	osRelease, err := readOSRelease(r.dependencies.root)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read operating system: %w", err)
	}
	machineID, err := readMachineID(r.dependencies.root)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read machine identity: %w", err)
	}
	uptime, err := readUptime(r.dependencies.root)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read uptime: %w", err)
	}
	hostname, err := r.dependencies.hostname()
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read hostname: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return hoststate.State{}, errors.New("read hostname: empty hostname")
	}
	if strings.TrimSpace(r.dependencies.architecture) == "" {
		return hoststate.State{}, errors.New("read architecture: empty architecture")
	}
	if err := ctx.Err(); err != nil {
		return hoststate.State{}, err
	}

	blockDevices, err := r.dependencies.readBlockDevices(ctx)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read block devices: %w", err)
	}
	disks, smartTargets, err := parseDisks(blockDevices, r.smartFor)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read block devices: %w", err)
	}
	r.refreshSMARTInBackground(smartTargets)
	observeDataVolume(disks, r.dependencies.readFilesystemUsage, func() *uint64 {
		return readBtrfsErrors(r.dependencies.root, mountedDevices(blockDevices, dataMountPoint))
	})

	hostID, err := derivedResourceID("host", hostIdentityDomain, machineID)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("derive host identity: %w", err)
	}

	return hoststate.State{
		ObservedAt: r.dependencies.now().UTC(),
		System: hoststate.System{
			ID:              hostID,
			Hostname:        hostname,
			OperatingSystem: osRelease,
			Architecture:    r.dependencies.architecture,
			UptimeSeconds:   uptime,
			Health:          hoststate.HealthUnknown,
		},
		Disks: disks,
	}, nil
}

// stRDOnly is ST_RDONLY in statfs flags: the filesystem is mounted read-only.
const stRDOnly = 0x1

type filesystemUsage struct {
	total, available uint64
	readOnly         bool
}

// observeDataVolume adds the mounted data volume's size, free space,
// read-only state and Btrfs error count to the data disk.
func observeDataVolume(disks []hoststate.Disk, readUsage func(string) (filesystemUsage, error), readErrors func() *uint64) {
	for index := range disks {
		if disks[index].Role != hoststate.DiskRoleData {
			continue
		}
		if readErrors != nil {
			disks[index].FilesystemErrors = readErrors()
		}
		if readUsage == nil {
			return
		}
		usage, err := readUsage(dataMountPoint)
		if err != nil || usage.total == 0 || usage.available > usage.total {
			return
		}
		disks[index].FilesystemCapacityBytes = &usage.total
		disks[index].FilesystemAvailableBytes = &usage.available
		disks[index].FilesystemReadOnly = usage.readOnly
		return
	}
}

// readBtrfsErrors sums the write, read, flush, checksum and generation
// errors Linux has counted for every device of the Btrfs filesystem on one
// of devices (sysfs devinfo/*/error_stats, Linux 5.14 and later). The counts
// live in the filesystem and grow until reset with "btrfs device stats -z".
// It is nil when the filesystem is not found there.
func readBtrfsErrors(root fs.FS, devices []string) *uint64 {
	if root == nil || len(devices) == 0 {
		return nil
	}
	members, err := fs.Glob(root, "sys/fs/btrfs/*/devices/*")
	if err != nil {
		return nil
	}
	for _, member := range members {
		if !slices.Contains(devices, path.Base(member)) {
			continue
		}
		stats, err := fs.Glob(root, path.Dir(path.Dir(member))+"/devinfo/*/error_stats")
		if err != nil || len(stats) == 0 {
			return nil
		}
		var total uint64
		for _, file := range stats {
			contents, err := fs.ReadFile(root, file)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
				fields := strings.Fields(line)
				if len(fields) != 2 {
					return nil
				}
				count, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return nil
				}
				total += count
			}
		}
		return &total
	}
	return nil
}

// mountedDevices names the block devices lsblk shows mounted at mountpoint.
func mountedDevices(contents []byte, mountpoint string) []string {
	var document blockDeviceDocument
	if json.Unmarshal(contents, &document) != nil {
		return nil
	}
	var names []string
	var visit func(blockDevice)
	visit = func(device blockDevice) {
		for _, candidate := range device.Mountpoints {
			if candidate != nil && *candidate == mountpoint && device.Name != "" {
				names = append(names, device.Name)
				break
			}
		}
		for _, child := range device.Children {
			visit(child)
		}
	}
	for _, device := range document.BlockDevices {
		visit(device)
	}
	return names
}

// ResolveDevice maps an opaque disk ID to a current kernel device path for the
// privileged Linux Adapter. The path never crosses the product API boundary.
func (r *Reader) ResolveDevice(ctx context.Context, diskID string) (ResolvedDevice, error) {
	contents, err := r.dependencies.readBlockDevices(ctx)
	if err != nil {
		return ResolvedDevice{}, err
	}
	var document blockDeviceDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return ResolvedDevice{}, err
	}
	disks, _, err := parseDisks(contents, r.smartFor)
	if err != nil {
		return ResolvedDevice{}, err
	}
	byID := make(map[string]hoststate.Disk, len(disks))
	for _, disk := range disks {
		byID[disk.ID.String()] = disk
	}
	for _, device := range document.BlockDevices {
		if device.Type != "disk" {
			continue
		}
		identity, err := stableDiskIdentity(device)
		if err != nil {
			continue
		}
		id, err := derivedResourceID("disk", diskIdentityDomain, identity)
		if err != nil || id.String() != diskID {
			continue
		}
		if device.Name == "" || strings.ContainsAny(device.Name, `/\\`) {
			return ResolvedDevice{}, errors.New("invalid kernel block-device name")
		}
		partitionName := device.Name + "1"
		if last := device.Name[len(device.Name)-1]; last >= '0' && last <= '9' {
			partitionName = device.Name + "p1"
		}
		return ResolvedDevice{
			Disk: byID[diskID], DevicePath: "/dev/" + device.Name, PartitionPath: "/dev/" + partitionName,
		}, nil
	}
	return ResolvedDevice{}, errors.New("stable disk ID was not found")
}

func readOSRelease(root fs.FS) (hoststate.OperatingSystem, error) {
	contents, err := fs.ReadFile(root, "etc/os-release")
	if err != nil {
		return hoststate.OperatingSystem{}, err
	}

	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			unquoted, unquoteErr := strconv.Unquote(value)
			if unquoteErr != nil {
				return hoststate.OperatingSystem{}, unquoteErr
			}
			value = unquoted
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return hoststate.OperatingSystem{}, err
	}

	name := strings.TrimSpace(values["NAME"])
	if strings.EqualFold(strings.TrimSpace(values["ID"]), "debian") {
		name = "Debian"
	}
	version := strings.TrimSpace(values["VERSION_ID"])
	if name == "" || version == "" {
		return hoststate.OperatingSystem{}, errors.New("os-release is missing name or version")
	}
	return hoststate.OperatingSystem{Name: name, Version: version}, nil
}

func readMachineID(root fs.FS) (string, error) {
	contents, err := fs.ReadFile(root, "etc/machine-id")
	if err != nil {
		return "", err
	}
	machineID := strings.ToLower(strings.TrimSpace(string(contents)))
	decoded, err := hex.DecodeString(machineID)
	if err != nil || len(decoded) != 16 {
		return "", errors.New("machine-id is not a 128-bit hexadecimal value")
	}
	return machineID, nil
}

func readUptime(root fs.FS) (uint64, error) {
	contents, err := fs.ReadFile(root, "proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(contents))
	if len(fields) == 0 {
		return 0, errors.New("uptime is empty")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
		return 0, errors.New("uptime is invalid")
	}
	return uint64(seconds), nil
}

type blockDeviceDocument struct {
	BlockDevices []blockDevice `json:"blockdevices"`
}

type blockDevice struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Size        uint64        `json:"size"`
	Model       string        `json:"model"`
	Transport   string        `json:"tran"`
	Rotational  bool          `json:"rota"`
	Removable   bool          `json:"rm"`
	Filesystem  string        `json:"fstype"`
	Mountpoints []*string     `json:"mountpoints"`
	WWN         string        `json:"wwn"`
	Serial      string        `json:"serial"`
	Children    []blockDevice `json:"children"`
}

// parseDisks maps lsblk's disks to public disks with the SMART readings smart
// has for them, and lists the disks SMART can be read from. smart may be nil.
func parseDisks(contents []byte, smart func(identity string) (hoststate.Health, *int)) ([]hoststate.Disk, []smartTarget, error) {
	var document blockDeviceDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, nil, err
	}

	disks := make([]hoststate.Disk, 0, len(document.BlockDevices))
	targets := make([]smartTarget, 0, len(document.BlockDevices))
	seen := make(map[string]struct{})
	systemDisks := 0
	for _, device := range document.BlockDevices {
		if device.Type != "disk" {
			continue
		}
		if device.Size == 0 || strings.TrimSpace(device.Model) == "" {
			return nil, nil, fmt.Errorf("disk %q is missing model or capacity", device.Name)
		}
		identity, err := stableDiskIdentity(device)
		if err != nil {
			return nil, nil, fmt.Errorf("disk %q: %w", device.Name, err)
		}
		id, err := derivedResourceID("disk", diskIdentityDomain, identity)
		if err != nil {
			return nil, nil, fmt.Errorf("disk %q: %w", device.Name, err)
		}
		if _, exists := seen[id.String()]; exists {
			return nil, nil, errors.New("duplicate stable disk identity")
		}
		seen[id.String()] = struct{}{}

		role := hoststate.DiskRoleUnassigned
		if containsMountpoint(device, "/") {
			role = hoststate.DiskRoleSystem
			systemDisks++
		} else if containsMountpoint(device, dataMountPoint) {
			role = hoststate.DiskRoleData
		}
		smartStatus := hoststate.HealthUnknown
		var temperature *int
		if smart != nil {
			smartStatus, temperature = smart(identity)
		}
		if device.Name != "" && !strings.ContainsAny(device.Name, `/\\`) {
			targets = append(targets, smartTarget{identity: identity, name: device.Name})
		}
		health := smartStatus
		if health == "" {
			health = hoststate.HealthUnknown
		}
		disks = append(disks, hoststate.Disk{
			ID:                 id,
			Model:              strings.TrimSpace(device.Model),
			Transport:          mapTransport(device.Transport),
			CapacityBytes:      device.Size,
			Rotational:         device.Rotational,
			Removable:          device.Removable,
			InUse:              deviceInUse(device),
			Filesystems:        collectFilesystems(device),
			Role:               role,
			Health:             health,
			SMARTStatus:        smartStatus,
			TemperatureCelsius: temperature,
		})
	}
	if systemDisks != 1 {
		return nil, nil, fmt.Errorf("found %d system disks, want exactly one", systemDisks)
	}

	sort.Slice(disks, func(i, j int) bool {
		return disks[i].ID.String() < disks[j].ID.String()
	})
	return disks, targets, nil
}

func deviceInUse(device blockDevice) bool {
	for _, mountpoint := range device.Mountpoints {
		if mountpoint != nil && strings.TrimSpace(*mountpoint) != "" {
			return true
		}
	}
	filesystem := strings.ToLower(strings.TrimSpace(device.Filesystem))
	if filesystem == "swap" || filesystem == "linux_raid_member" || filesystem == "lvm2_member" || filesystem == "crypto_luks" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(device.Type)) {
	case "crypt", "lvm", "raid", "md", "mpath":
		return true
	}
	for _, child := range device.Children {
		if deviceInUse(child) {
			return true
		}
	}
	return false
}

func collectFilesystems(device blockDevice) []string {
	seen := make(map[string]struct{})
	var visit func(blockDevice)
	visit = func(candidate blockDevice) {
		if filesystem := strings.ToLower(strings.TrimSpace(candidate.Filesystem)); filesystem != "" {
			seen[filesystem] = struct{}{}
		}
		for _, child := range candidate.Children {
			visit(child)
		}
	}
	visit(device)
	if len(seen) == 0 {
		return nil
	}
	filesystems := make([]string, 0, len(seen))
	for filesystem := range seen {
		filesystems = append(filesystems, filesystem)
	}
	sort.Strings(filesystems)
	return filesystems
}

func stableDiskIdentity(device blockDevice) (string, error) {
	if wwn := strings.ToLower(strings.TrimSpace(device.WWN)); wwn != "" {
		return "wwn:" + wwn, nil
	}
	if serial := strings.TrimSpace(device.Serial); serial != "" {
		model := strings.ToLower(strings.Join(strings.Fields(device.Model), " "))
		return "serial:" + model + "\x00" + serial, nil
	}
	return "", errors.New("no stable hardware identity")
}

func derivedResourceID(kind, domain, source string) (hoststate.ResourceID, error) {
	digest := sha256.Sum256([]byte(domain + "\x00" + source))
	return hoststate.NewResourceID(kind + ":" + hex.EncodeToString(digest[:16]))
}

func containsMountpoint(device blockDevice, mountpoint string) bool {
	for _, candidate := range device.Mountpoints {
		if candidate != nil && *candidate == mountpoint {
			return true
		}
	}
	for _, child := range device.Children {
		if containsMountpoint(child, mountpoint) {
			return true
		}
	}
	return false
}

func mapTransport(value string) hoststate.Transport {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "nvme":
		return hoststate.TransportNVMe
	case "sata", "ata":
		return hoststate.TransportSATA
	case "usb":
		return hoststate.TransportUSB
	default:
		return hoststate.TransportUnknown
	}
}

var _ hoststate.Observer = (*Reader)(nil)
