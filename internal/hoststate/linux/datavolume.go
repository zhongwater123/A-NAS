package linux

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// btrfsDevices maps each block device of a mounted Btrfs filesystem to that
// filesystem's UUID, from sysfs (fs/btrfs/<uuid>/devices). lsblk takes
// FSTYPE and UUID from the udev database, which btrfs device add leaves
// stale until the next boot: udev probes the new partition when the tool
// closes it, still wiped, and the kernel then writes the superblock without
// another event. The kernel's own list is right at once.
func btrfsDevices(root fs.FS) map[string]string {
	devices := make(map[string]string)
	if root == nil {
		return devices
	}
	members, err := fs.Glob(root, "sys/fs/btrfs/*/devices/*")
	if err != nil {
		return devices
	}
	for _, member := range members {
		if uuid := path.Base(path.Dir(path.Dir(member))); datavolume.ValidFilesystemUUID(uuid) {
			devices[path.Base(member)] = uuid
		}
	}
	return devices
}

// dataVolumeUUID is the filesystem UUID of the Btrfs mounted at the data
// mount point: the kernel's for the device the mount table names, or the
// udev database's when sysfs does not list that device.
func dataVolumeUUID(devices []blockDevice, btrfs map[string]string) string {
	for _, name := range namesMountedAt(devices, dataMountPoint) {
		if uuid := btrfs[name]; uuid != "" {
			return uuid
		}
	}
	return mountedBtrfsUUID(devices, dataMountPoint)
}

// holdsBtrfs reports whether the kernel lists the device, or a partition on
// it, as a device of the mounted Btrfs filesystem with the UUID, or of any
// mounted Btrfs when uuid is "".
func holdsBtrfs(device blockDevice, btrfs map[string]string, uuid string) bool {
	if member, ok := btrfs[device.Name]; ok && device.Name != "" && (uuid == "" || member == uuid) {
		return true
	}
	for _, child := range device.Children {
		if holdsBtrfs(child, btrfs, uuid) {
			return true
		}
	}
	return false
}

// readDataVolume describes the Btrfs filesystem mounted as the data volume:
// the disks holding it, the profiles its data and metadata are in, and the
// exclusive operation (a balance converting profiles) running on it. Profiles
// come from sysfs allocation/<type>/<profile>/used_bytes, which exists only
// for profiles that hold block groups. It is nil without a mounted volume.
func readDataVolume(root fs.FS, uuid string, disks []hoststate.Disk) *hoststate.DataVolume {
	if !datavolume.ValidFilesystemUUID(uuid) {
		return nil
	}
	volume := &hoststate.DataVolume{FilesystemUUID: uuid}
	for _, disk := range disks {
		if disk.Role == hoststate.DiskRoleData {
			volume.DiskIDs = append(volume.DiskIDs, disk.ID)
		}
	}
	if len(volume.DiskIDs) == 0 {
		return nil
	}
	if root == nil {
		return volume
	}
	volume.Data, volume.Metadata, volume.Operation = ReadBtrfsAllocation(root, uuid)
	volume.MissingDevices = ReadMissingDevices(root, uuid)
	return volume
}

// BtrfsDevice is one device of a mounted Btrfs filesystem, from sysfs
// devinfo/<devid>.
type BtrfsDevice struct {
	ID uint64
	// Missing says the kernel cannot write to it: it was absent when the
	// volume was mounted degraded, or it came back while the volume was
	// mounted and stays unwritable until the next mount.
	Missing bool
}

// ReadBtrfsDevices lists the devices of the mounted Btrfs filesystem with
// the UUID (root is the filesystem root), leaving out a replacement being
// written. A disk that vanishes while the volume is mounted is listed as
// present: only its entry under devices/ stops leading to a block device,
// which ReadMissingDevices also checks.
func ReadBtrfsDevices(root fs.FS, uuid string) []BtrfsDevice {
	if root == nil || !datavolume.ValidFilesystemUUID(uuid) {
		return nil
	}
	entries, err := fs.Glob(root, "sys/fs/btrfs/"+uuid+"/devinfo/*")
	if err != nil {
		return nil
	}
	var devices []BtrfsDevice
	for _, entry := range entries {
		id, err := strconv.ParseUint(path.Base(entry), 10, 64)
		if err != nil || readFlag(root, entry+"/replace_target") {
			continue
		}
		devices = append(devices, BtrfsDevice{ID: id, Missing: readFlag(root, entry+"/missing") || !readFlag(root, entry+"/writeable")})
	}
	return devices
}

// ReadMissingDevices counts the devices of the mounted Btrfs filesystem with
// the UUID that the kernel cannot write to (root is the filesystem root),
// including a disk that vanished while the volume was mounted.
func ReadMissingDevices(root fs.FS, uuid string) int {
	devices := ReadBtrfsDevices(root, uuid)
	if len(devices) == 0 {
		return 0
	}
	unwritable := 0
	for _, device := range devices {
		if device.Missing {
			unwritable++
		}
	}
	links, err := fs.Glob(root, "sys/fs/btrfs/"+uuid+"/devices/*")
	if err != nil {
		return unwritable
	}
	present := 0
	for _, link := range links {
		if _, err := fs.Stat(root, link); err == nil {
			present++
		}
	}
	return max(unwritable, len(devices)-present)
}

// readFlag reads a sysfs file holding 0 or 1; anything else reads as 0.
func readFlag(root fs.FS, file string) bool {
	contents, err := fs.ReadFile(root, file)
	return err == nil && strings.TrimSpace(string(contents)) == "1"
}

var (
	btrfsShowFilesystem = regexp.MustCompile(`^Label: .*\suuid: ([0-9A-Fa-f-]{36})$`)
	btrfsShowTotal      = regexp.MustCompile(`^\s+Total devices (\d+) `)
	btrfsShowDevice     = regexp.MustCompile(`^\s+devid\s+(\d+)\s+size\s+(\d+)\s+used\s+(\d+)\s+path\s+\S`)
)

// parseBtrfsDevices reads from `btrfs filesystem show --raw` the devices of
// the filesystem with the UUID: each device's size and the bytes its block
// groups take. Sysfs lists neither, and btrfs-progs prints this command, and
// `btrfs device usage`, only as text (`--format json` is refused for both
// in 6.14), so the lines are parsed here. It is nil unless the output lists
// every device the filesystem has, none of them missing, since the
// allocation of a filesystem missing a device cannot be planned.
func parseBtrfsDevices(output []byte, uuid string) []hoststate.VolumeDevice {
	var devices []hoststate.VolumeDevice
	inside, total := false, 0
	for line := range strings.Lines(string(output)) {
		line = strings.TrimRight(line, "\r\n")
		if match := btrfsShowFilesystem.FindStringSubmatch(line); match != nil {
			if inside {
				break
			}
			inside = strings.EqualFold(match[1], uuid)
			continue
		}
		if !inside {
			continue
		}
		if strings.Contains(strings.ToLower(line), "missing") {
			return nil
		}
		if match := btrfsShowTotal.FindStringSubmatch(line); match != nil {
			value, err := strconv.Atoi(match[1])
			if err != nil || total != 0 {
				return nil
			}
			total = value
			continue
		}
		match := btrfsShowDevice.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		var numbers [3]uint64
		for i := range numbers {
			value, err := strconv.ParseUint(match[i+1], 10, 64)
			if err != nil {
				return nil
			}
			numbers[i] = value
		}
		device := hoststate.VolumeDevice{DevID: numbers[0], SizeBytes: numbers[1], AllocatedBytes: numbers[2]}
		if device.DevID == 0 || device.SizeBytes == 0 || device.AllocatedBytes > device.SizeBytes ||
			slices.ContainsFunc(devices, func(other hoststate.VolumeDevice) bool { return other.DevID == device.DevID }) {
			return nil
		}
		devices = append(devices, device)
	}
	if total == 0 || len(devices) != total {
		return nil
	}
	return devices
}

// VolumeDevices reads the devices of the mounted Btrfs filesystem with the
// UUID from the output of `btrfs filesystem show --raw`, checked against
// sysfs (root is the filesystem root): the kernel's devinfo/<devid> lists
// every device, a missing one included, so a device the tool does not list
// leaves the allocation unknown (nil). The host state and the Host Agent's
// spreading both read the devices through it.
func VolumeDevices(root fs.FS, uuid string, output []byte) []hoststate.VolumeDevice {
	devices := parseBtrfsDevices(output, uuid)
	if devices == nil || root == nil {
		return devices
	}
	known, err := fs.Glob(root, "sys/fs/btrfs/"+uuid+"/devinfo/*")
	if err != nil || len(known) == 0 {
		return devices
	}
	if len(known) != len(devices) {
		return nil
	}
	for _, directory := range known {
		if missing, err := fs.ReadFile(root, directory+"/missing"); err == nil && strings.TrimSpace(string(missing)) != "0" {
			return nil
		}
	}
	return devices
}

// ReadBtrfsAllocation reads from sysfs (root is the filesystem root) the
// profiles that hold data and metadata of the mounted Btrfs filesystem with
// the UUID, and the exclusive operation running on it.
func ReadBtrfsAllocation(root fs.FS, uuid string) (data, metadata []hoststate.ProfileUsage, operation hoststate.VolumeOperation) {
	if !datavolume.ValidFilesystemUUID(uuid) {
		return nil, nil, hoststate.VolumeOperationNone
	}
	base := "sys/fs/btrfs/" + uuid
	return readProfileUsage(root, base+"/allocation/data"), readProfileUsage(root, base+"/allocation/metadata"),
		readExclusiveOperation(root, base+"/exclusive_operation")
}

func readProfileUsage(root fs.FS, directory string) []hoststate.ProfileUsage {
	files, err := fs.Glob(root, directory+"/*/used_bytes")
	if err != nil {
		return nil
	}
	var usage []hoststate.ProfileUsage
	for _, file := range files {
		profile := hoststate.Profile(path.Base(path.Dir(file)))
		if !profile.Valid() {
			continue
		}
		contents, err := fs.ReadFile(root, file)
		if err != nil {
			return nil
		}
		used, err := strconv.ParseUint(strings.TrimSpace(string(contents)), 10, 64)
		if err != nil {
			return nil
		}
		usage = append(usage, hoststate.ProfileUsage{Profile: profile, UsedBytes: used})
	}
	sort.Slice(usage, func(i, j int) bool { return usage[i].Profile < usage[j].Profile })
	return usage
}

// readExclusiveOperation reads the operation Btrfs runs exclusively on the
// filesystem (sysfs exclusive_operation, Linux 5.10 and later).
func readExclusiveOperation(root fs.FS, file string) hoststate.VolumeOperation {
	contents, err := fs.ReadFile(root, file)
	if err != nil {
		return hoststate.VolumeOperationNone
	}
	switch strings.TrimSpace(string(contents)) {
	case "", "none":
		return hoststate.VolumeOperationNone
	case "balance":
		return hoststate.VolumeOperationBalance
	case "balance paused":
		return hoststate.VolumeOperationBalancePaused
	default:
		return hoststate.VolumeOperationOther
	}
}
