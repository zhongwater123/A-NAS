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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	hostIdentityDomain = "a-nas:host:v1"
	diskIdentityDomain = "a-nas:disk:v1"
)

type dependencies struct {
	root             fs.FS
	hostname         func() (string, error)
	architecture     string
	now              func() time.Time
	readBlockDevices func(context.Context) ([]byte, error)
	readSMART        func(context.Context, string) ([]byte, error)
	sleep            func(context.Context, time.Duration) error
}

// Reader observes a Debian host without changing its state.
type Reader struct {
	dependencies dependencies
	metrics      metricsSampler
}

type ResolvedDevice struct {
	Disk          hoststate.Disk
	DevicePath    string
	PartitionPath string
}

// New returns a Reader backed by the local Linux filesystem and lsblk.
func New() *Reader {
	return newReader(dependencies{
		root:         os.DirFS("/"),
		hostname:     os.Hostname,
		architecture: runtime.GOARCH,
		now:          time.Now,
		sleep:        sleepContext,
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
		readSMART: func(ctx context.Context, deviceName string) ([]byte, error) {
			if deviceName == "" || strings.ContainsAny(deviceName, `/\\`) {
				return nil, errors.New("invalid block device name")
			}
			output, err := exec.CommandContext(ctx, "smartctl", "--json", "--all", "/dev/"+deviceName).Output()
			if err != nil && len(output) == 0 {
				return nil, err
			}
			return output, nil
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
	disks, err := parseDisks(ctx, blockDevices, r.dependencies.readSMART)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("read block devices: %w", err)
	}

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
	disks, err := parseDisks(ctx, contents, r.dependencies.readSMART)
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

func parseDisks(ctx context.Context, contents []byte, readSMART func(context.Context, string) ([]byte, error)) ([]hoststate.Disk, error) {
	var document blockDeviceDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, err
	}

	disks := make([]hoststate.Disk, 0, len(document.BlockDevices))
	seen := make(map[string]struct{})
	systemDisks := 0
	for _, device := range document.BlockDevices {
		if device.Type != "disk" {
			continue
		}
		if device.Size == 0 || strings.TrimSpace(device.Model) == "" {
			return nil, fmt.Errorf("disk %q is missing model or capacity", device.Name)
		}
		identity, err := stableDiskIdentity(device)
		if err != nil {
			return nil, fmt.Errorf("disk %q: %w", device.Name, err)
		}
		id, err := derivedResourceID("disk", diskIdentityDomain, identity)
		if err != nil {
			return nil, fmt.Errorf("disk %q: %w", device.Name, err)
		}
		if _, exists := seen[id.String()]; exists {
			return nil, errors.New("duplicate stable disk identity")
		}
		seen[id.String()] = struct{}{}

		role := hoststate.DiskRoleUnassigned
		if containsMountpoint(device, "/") {
			role = hoststate.DiskRoleSystem
			systemDisks++
		} else if containsMountpoint(device, "/srv/a-nas/data") {
			role = hoststate.DiskRoleData
		}
		smartStatus := hoststate.HealthUnknown
		var temperature *int
		if readSMART != nil {
			if smartJSON, smartErr := readSMART(ctx, device.Name); smartErr == nil {
				smartStatus, temperature = parseSMART(smartJSON)
			}
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
		return nil, fmt.Errorf("found %d system disks, want exactly one", systemDisks)
	}

	sort.Slice(disks, func(i, j int) bool {
		return disks[i].ID.String() < disks[j].ID.String()
	})
	return disks, nil
}

type smartDocument struct {
	SMARTStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	NVMeHealth *struct {
		Temperature int `json:"temperature"`
	} `json:"nvme_smart_health_information_log"`
}

func parseSMART(contents []byte) (hoststate.Health, *int) {
	var document smartDocument
	if len(contents) == 0 || json.Unmarshal(contents, &document) != nil {
		return hoststate.HealthUnknown, nil
	}
	health := hoststate.HealthUnknown
	if document.SMARTStatus != nil {
		if document.SMARTStatus.Passed {
			health = hoststate.HealthHealthy
		} else {
			health = hoststate.HealthCritical
		}
	}
	var temperature *int
	if document.Temperature != nil {
		value := document.Temperature.Current
		temperature = &value
	} else if document.NVMeHealth != nil {
		value := document.NVMeHealth.Temperature
		temperature = &value
	}
	return health, temperature
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
