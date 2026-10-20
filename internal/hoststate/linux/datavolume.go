package linux

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

var filesystemUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// readDataVolume describes the Btrfs filesystem mounted as the data volume:
// the disks holding it, the profiles its data and metadata are in, and the
// exclusive operation (a balance converting profiles) running on it. Profiles
// come from sysfs allocation/<type>/<profile>/used_bytes, which exists only
// for profiles that hold block groups. It is nil without a mounted volume.
func readDataVolume(root fs.FS, uuid string, disks []hoststate.Disk) *hoststate.DataVolume {
	if !filesystemUUIDPattern.MatchString(uuid) {
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
	if !filesystemUUIDPattern.MatchString(uuid) {
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
