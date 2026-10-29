package linux

import (
	"io/fs"
	"path"
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
	return volume
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
