package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

// volumeMarker is .a-nas-volume.json at the root of the data volume: the
// evidence that the mounted filesystem is the data volume, and the layout
// the Host Agent keeps it converted to (ADR 0025).
type volumeMarker struct {
	FilesystemUUID string `json:"filesystemUuid"`
	DiskID         string `json:"diskId,omitempty"`
	FormatVersion  int    `json:"formatVersion"`
	// PlanID is the plan that created the volume; absent on volumes created
	// before issue #165.
	PlanID string `json:"planId,omitempty"`
	// Layout is absent on volumes created on one disk before ADR 0025.
	Layout datavolume.Layout `json:"layout,omitempty"`
	// Pending is the layout disks being added call for. It is recorded
	// before they are added and becomes Layout once the volume has them, so
	// that disks a killed run added still get it (issue #165).
	Pending *pendingLayout `json:"pending,omitempty"`
}

type pendingLayout struct {
	PlanID string            `json:"planId"`
	Layout datavolume.Layout `json:"layout"`
	// Devices is how many devices the volume has with the disks added.
	Devices int `json:"devices"`
}

func (e *Executor) markerPath() string { return filepath.Join(e.mountPoint, ".a-nas-volume.json") }

func (e *Executor) mountUnitPath() string {
	return filepath.Join(e.systemRoot, "etc", "systemd", "system", e.mountUnitName)
}

func (e *Executor) readMarker() (volumeMarker, error) {
	contents, err := os.ReadFile(e.markerPath())
	if err != nil {
		return volumeMarker{}, err
	}
	var marker volumeMarker
	if err := json.Unmarshal(contents, &marker); err != nil || marker.FormatVersion != 1 || !datavolume.ValidFilesystemUUID(marker.FilesystemUUID) {
		return volumeMarker{}, errors.New("data-volume identity marker is invalid")
	}
	return marker, nil
}

func (e *Executor) writeMarker(marker volumeMarker) error {
	contents, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return writeAtomic(e.markerPath(), append(contents, '\n'), 0o644)
}

// createVolume erases the planned disks, formats them as the data volume
// with the layout and mounts it by filesystem UUID. Run again for the same
// plan it continues: a volume the plan already mounted is only prepared,
// and one that never mounted is formatted again from the start, since no
// file can be on it. It refuses to touch a data volume, or a mount unit for
// one, that the plan did not make (issue #165).
func (e *Executor) createVolume(ctx context.Context, operation *volumeOperation) (datavolume.Volume, error) {
	mounted, err := e.resolver.MountedDataVolume(ctx)
	if err != nil {
		return datavolume.Volume{}, fmt.Errorf("read the mounted data volume: %w", err)
	}
	if mounted != nil {
		if !e.madeBy(operation, mounted) {
			return datavolume.Volume{}, datavolume.ErrVolumeExists
		}
		return e.prepareVolume(ctx, operation, mounted.FilesystemUUID)
	}
	if e.dataVolumeReady() {
		return datavolume.Volume{}, datavolume.ErrVolumeExists
	}
	devices, err := e.resolveTargets(ctx, operation.Disks)
	if err != nil {
		return datavolume.Volume{}, err
	}
	if err := e.checkMountUnit(ctx, devices); err != nil {
		return datavolume.Volume{}, err
	}
	e.enter(operation, datavolume.StepErase)
	for _, device := range devices {
		if err := e.prepareDisk(ctx, device); err != nil {
			return datavolume.Volume{}, err
		}
	}
	e.enter(operation, datavolume.StepFormat)
	data, metadata := operation.Layout.Profiles()
	format := []string{"--force", "--label", "ANAS_DATA", "--data", string(data), "--metadata", string(metadata)}
	for _, device := range devices {
		format = append(format, device.PartitionPath)
	}
	if output, err := e.runner.Run(ctx, "mkfs.btrfs", format, ""); err != nil {
		return datavolume.Volume{}, commandError("mkfs.btrfs failed", err, output)
	}
	uuidBytes, err := e.runner.Output(ctx, "blkid", []string{"--output", "value", "--match-tag", "UUID", devices[0].PartitionPath}, "")
	if err != nil {
		return datavolume.Volume{}, fmt.Errorf("read Btrfs UUID: %w", err)
	}
	uuid := strings.TrimSpace(string(uuidBytes))
	if !datavolume.ValidFilesystemUUID(uuid) {
		return datavolume.Volume{}, errors.New("blkid returned an invalid filesystem UUID")
	}
	if err := e.recordFormatted(operation, uuid); err != nil {
		return datavolume.Volume{}, fmt.Errorf("record the new filesystem: %w", err)
	}
	e.enter(operation, datavolume.StepMount)
	if err := os.MkdirAll(e.mountPoint, 0o750); err != nil {
		return datavolume.Volume{}, fmt.Errorf("create mount point: %w", err)
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
	if err := writeAtomic(e.mountUnitPath(), []byte(unit), 0o644); err != nil {
		return datavolume.Volume{}, fmt.Errorf("install data-volume mount unit: %w", err)
	}
	// A filesystem on several disks mounts once the kernel knows them all.
	steps := []struct {
		name string
		args []string
	}{
		{name: "btrfs", args: []string{"device", "scan"}},
		{name: "systemctl", args: []string{"daemon-reload"}},
		{name: "systemctl", args: []string{"enable", "--now", e.mountUnitName}},
	}
	for _, step := range steps {
		if output, err := e.runner.Run(ctx, step.name, step.args, ""); err != nil {
			return datavolume.Volume{}, commandError(step.name+" failed after formatting", err, output)
		}
	}
	mounted, err = e.resolver.MountedDataVolume(ctx)
	if err != nil {
		return datavolume.Volume{}, fmt.Errorf("read the mounted data volume: %w", err)
	}
	if mounted == nil || !strings.EqualFold(mounted.FilesystemUUID, uuid) {
		return datavolume.Volume{}, errors.New("the new data volume is not mounted")
	}
	return e.prepareVolume(ctx, operation, uuid)
}

// prepareVolume readies the new data volume once it is mounted: the spaces
// subvolume, the snapshot directory, the marker naming the plan, and the
// registered spaces. Each step may be repeated.
func (e *Executor) prepareVolume(ctx context.Context, operation *volumeOperation, uuid string) (datavolume.Volume, error) {
	e.enter(operation, datavolume.StepPrepare)
	spaces := filepath.Join(e.mountPoint, "spaces")
	if _, err := os.Lstat(spaces); errors.Is(err, os.ErrNotExist) {
		if output, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", spaces}, ""); err != nil {
			return datavolume.Volume{}, commandError("btrfs failed after formatting", err, output)
		}
	} else if err != nil {
		return datavolume.Volume{}, err
	}
	if err := os.MkdirAll(filepath.Join(e.mountPoint, ".a-nas-snapshots"), 0o700); err != nil {
		return datavolume.Volume{}, err
	}
	marker := volumeMarker{FilesystemUUID: uuid, DiskID: operation.Disks[0].DiskID, FormatVersion: 1, PlanID: operation.PlanID}
	if len(operation.Disks) > 1 {
		marker.Layout = operation.Layout
	}
	e.markerMu.Lock()
	err := e.writeMarker(marker)
	e.markerMu.Unlock()
	if err != nil {
		return datavolume.Volume{}, fmt.Errorf("write data-volume identity marker: %w", err)
	}
	if _, err := e.materializeRegisteredSpaces(ctx); err != nil {
		return datavolume.Volume{}, fmt.Errorf("materialize registered spaces: %w", err)
	}
	// Best effort: a failure only delays managed stores until the Host Agent's
	// periodic repair, which logs it.
	_, _ = e.EnsurePhotosStore(ctx)
	_, _ = e.EnsureSharesStore(ctx)
	volume := datavolume.Volume{ID: "volume:data", FilesystemUUID: uuid, Layout: operation.Layout, State: datavolume.VolumeStateAvailable}
	sizes := make([]uint64, 0, len(operation.Disks))
	for _, disk := range operation.Disks {
		volume.DiskIDs = append(volume.DiskIDs, disk.DiskID)
		if device, err := e.resolver.ResolveDevice(ctx, disk.DiskID); err == nil {
			sizes = append(sizes, device.Disk.CapacityBytes)
		}
	}
	if len(sizes) == len(operation.Disks) {
		volume.CapacityBytes = datavolume.UsableBytes(operation.Layout, sizes)
	}
	return volume, nil
}

// madeBy reports whether the operation made the mounted volume: it formatted
// the filesystem, or, when its journal entry is gone or an older release ran
// the plan, the marker names the plan and the volume is on its disks.
func (e *Executor) madeBy(operation *volumeOperation, mounted *hoststate.DataVolume) bool {
	if e.formattedBy(operation, mounted.FilesystemUUID) {
		return true
	}
	marker, err := e.readMarker()
	if err != nil || !strings.EqualFold(marker.FilesystemUUID, mounted.FilesystemUUID) {
		return false
	}
	named := marker.PlanID == operation.PlanID || (marker.PlanID == "" && marker.DiskID == operation.Disks[0].DiskID)
	if !named || len(mounted.DiskIDs) != len(operation.Disks) {
		return false
	}
	for _, disk := range operation.Disks {
		if !slices.ContainsFunc(mounted.DiskIDs, func(id hoststate.ResourceID) bool { return id.String() == disk.DiskID }) {
			return false
		}
	}
	return true
}

// checkMountUnit refuses to replace a mount unit for a filesystem the plan
// did not make: a data volume that is offline now would otherwise never
// mount again, and its disks would look unused (issue #165). The unit may
// be replaced when a volume creation that never succeeded formatted the
// filesystem, or when the filesystem is on a disk the plan erases anyway.
func (e *Executor) checkMountUnit(ctx context.Context, devices []BlockDevice) error {
	contents, err := os.ReadFile(e.mountUnitPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the data-volume mount unit: %w", err)
	}
	uuid := ""
	for line := range strings.SplitSeq(string(contents), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "What=UUID="); found {
			uuid = strings.TrimSpace(value)
		}
	}
	if !datavolume.ValidFilesystemUUID(uuid) {
		return datavolume.ErrVolumeExists
	}
	if e.formattedByUnfinishedCreation(uuid) {
		return nil
	}
	for _, device := range devices {
		output, err := e.runner.Output(ctx, "blkid", []string{"--probe", "--output", "value", "--match-tag", "UUID", device.PartitionPath}, "")
		if err == nil && strings.EqualFold(strings.TrimSpace(string(output)), uuid) {
			return nil
		}
	}
	return datavolume.ErrVolumeExists
}

// addDisks erases the planned disks, adds them to the mounted data volume
// and starts converting it to the layout in the background. The layout is
// recorded as pending in the marker first, so the Host Agent's check
// converts to it even when the run is killed right after Btrfs added the
// disks (issue #165). Run again for the same plan, it skips the disks that
// are members already. Files stay where they are and the volume stays in
// use throughout.
func (e *Executor) addDisks(ctx context.Context, operation *volumeOperation) (datavolume.Volume, error) {
	mounted, err := e.resolver.MountedDataVolume(ctx)
	if err != nil {
		return datavolume.Volume{}, fmt.Errorf("read the mounted data volume: %w", err)
	}
	if mounted == nil {
		return datavolume.Volume{}, datavolume.ErrVolumeUnavailable
	}
	if !strings.EqualFold(mounted.FilesystemUUID, operation.FilesystemUUID) {
		return datavolume.Volume{}, datavolume.ErrVolumeChanged
	}
	var adding []datavolume.DiskTarget
	for _, disk := range operation.Disks {
		if !slices.ContainsFunc(mounted.DiskIDs, func(id hoststate.ResourceID) bool { return id.String() == disk.DiskID }) {
			adding = append(adding, disk)
		}
	}
	if err := e.recordPendingLayout(operation, len(mounted.DiskIDs)+len(adding)); err != nil {
		return datavolume.Volume{}, err
	}
	if len(adding) > 0 {
		devices, err := e.resolveTargets(ctx, adding)
		if err != nil {
			return datavolume.Volume{}, err
		}
		e.enter(operation, datavolume.StepErase)
		for _, device := range devices {
			if err := e.prepareDisk(ctx, device); err != nil {
				return datavolume.Volume{}, err
			}
		}
		e.enter(operation, datavolume.StepAddDevices)
		add := []string{"device", "add", "-f"}
		for _, device := range devices {
			add = append(add, device.PartitionPath)
		}
		if output, err := e.runner.Run(ctx, "btrfs", append(add, e.mountPoint), ""); err != nil {
			return datavolume.Volume{}, commandError("btrfs device add failed", err, output)
		}
		// udev probed the new partitions while they were still wiped and hears
		// nothing of the superblock the kernel wrote; probe them again so lsblk
		// and blkid agree before the next boot. Host state does not depend on it
		// (it reads the members from sysfs), so a failure is only logged.
		trigger := []string{"trigger", "--action=change"}
		for _, device := range devices {
			trigger = append(trigger, device.PartitionPath)
		}
		if output, err := e.runner.Run(ctx, "udevadm", trigger, ""); err != nil {
			e.logger.WarnContext(ctx, "udev did not re-probe the added disks", "error", commandError("udevadm trigger", err, output))
		} else if output, err := e.runner.Run(ctx, "udevadm", []string{"settle", "--timeout=10"}, ""); err != nil {
			e.logger.WarnContext(ctx, "udev did not settle after adding disks", "error", commandError("udevadm settle", err, output))
		}
	}
	// The disks belong to the volume now; the layout becomes the volume's
	// before anything else can fail, so the Host Agent's check converts to it.
	if err := e.applyLayout(operation); err != nil {
		return datavolume.Volume{}, err
	}
	if _, err := e.ReconcileConversion(ctx); err != nil {
		e.logger.WarnContext(ctx, "data-volume conversion did not start; the periodic check retries it", "error", err)
	}
	volume := datavolume.Volume{FilesystemUUID: mounted.FilesystemUUID, Layout: operation.Layout, State: datavolume.VolumeStateAvailable}
	for _, disk := range operation.Disks {
		volume.DiskIDs = append(volume.DiskIDs, disk.DiskID)
	}
	return volume, nil
}

// recordPendingLayout records in the marker, before any disk is added, the
// layout the plan calls for and the devices the volume will then have. A
// retry keeps what its first run recorded.
func (e *Executor) recordPendingLayout(operation *volumeOperation, devices int) error {
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	marker, err := e.readMarker()
	if err != nil {
		return datavolume.ErrVolumeUnavailable
	}
	if !strings.EqualFold(marker.FilesystemUUID, operation.FilesystemUUID) {
		return datavolume.ErrVolumeChanged
	}
	if marker.Pending != nil && marker.Pending.PlanID == operation.PlanID {
		return nil
	}
	marker.Pending = &pendingLayout{PlanID: operation.PlanID, Layout: operation.Layout, Devices: devices}
	if err := e.writeMarker(marker); err != nil {
		return fmt.Errorf("record the pending data-volume layout: %w", err)
	}
	return nil
}

// applyLayout makes the layout the plan recorded as pending the volume's.
// The Host Agent's check may have done so already.
func (e *Executor) applyLayout(operation *volumeOperation) error {
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	marker, err := e.readMarker()
	if err != nil {
		return fmt.Errorf("record the data-volume layout: %w", err)
	}
	if marker.Pending != nil && marker.Pending.PlanID != operation.PlanID {
		return datavolume.ErrVolumeChanged
	}
	marker.Layout, marker.Pending = operation.Layout, nil
	if err := e.writeMarker(marker); err != nil {
		return fmt.Errorf("record the data-volume layout: %w", err)
	}
	return nil
}

// promotePendingLayout makes a pending layout the volume's once the kernel
// lists the devices it was recorded for: the run that added them was cut
// short before it could.
func (e *Executor) promotePendingLayout() (volumeMarker, error) {
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	marker, err := e.readMarker()
	if err != nil || marker.Pending == nil {
		return marker, err
	}
	devices, err := fs.Glob(os.DirFS(e.systemRoot), "sys/fs/btrfs/"+strings.ToLower(marker.FilesystemUUID)+"/devices/*")
	if err != nil || len(devices) < marker.Pending.Devices {
		return marker, nil
	}
	e.logger.Info("the data volume has the disks of a cut-short addition; taking their layout", "plan", marker.Pending.PlanID, "layout", marker.Pending.Layout)
	marker.Layout, marker.Pending = marker.Pending.Layout, nil
	return marker, e.writeMarker(marker)
}

// ContinueConversion starts converting the data volume to its layout now.
func (e *Executor) ContinueConversion(ctx context.Context) error {
	started, err := e.ReconcileConversion(ctx)
	if err == nil && !started {
		return datavolume.ErrNoConversion
	}
	return err
}

// ReconcileConversion converts the data volume to the layout its marker
// names when block groups are outside the layout's profiles and Btrfs runs
// nothing else: a balance with the soft filter rewrites only those, so it
// picks up where a stopped one left off. Stopping the Host Agent or the
// machine cancels it, which is why the Host Agent calls this at startup and
// then periodically. It reports whether a balance started.
func (e *Executor) ReconcileConversion(ctx context.Context) (bool, error) {
	if !e.dataVolumeReady() {
		return false, nil
	}
	marker, err := e.promotePendingLayout()
	if err != nil {
		return false, err
	}
	// A single disk, or a volume from before ADR 0025, has nothing to convert to.
	if !marker.Layout.Fits(2) {
		return false, nil
	}
	data, metadata, operation := linuxhoststate.ReadBtrfsAllocation(os.DirFS(e.systemRoot), strings.ToLower(marker.FilesystemUUID))
	args := conversionCommand(marker.Layout, data, metadata, operation)
	if args == nil {
		return false, nil
	}
	e.conversionMu.Lock()
	defer e.conversionMu.Unlock()
	if e.converting {
		return false, nil
	}
	e.converting = true
	args = append(args, e.mountPoint)
	e.logger.InfoContext(ctx, "converting the data volume", "layout", marker.Layout, "command", "btrfs "+strings.Join(args, " "))
	go func() {
		output, err := e.runner.Run(context.Background(), "btrfs", args, "")
		e.conversionMu.Lock()
		e.converting = false
		e.conversionMu.Unlock()
		if err != nil {
			e.logger.Error("data-volume conversion stopped", "layout", marker.Layout, "error", commandError("btrfs balance", err, output))
			return
		}
		e.logger.Info("data-volume conversion finished", "layout", marker.Layout)
	}()
	return true, nil
}

// conversionCommand is the btrfs command that converts data and metadata
// outside the layout's profiles, or nil when there is nothing to convert or
// Btrfs runs another exclusive operation. A paused balance is resumed.
func conversionCommand(layout datavolume.Layout, data, metadata []hoststate.ProfileUsage, operation hoststate.VolumeOperation) []string {
	switch operation {
	case hoststate.VolumeOperationBalancePaused:
		return []string{"balance", "resume"}
	case hoststate.VolumeOperationNone:
	default:
		return nil
	}
	convertData, convertMetadata := datavolume.OutsideLayout(data, metadata, layout)
	if !convertData && !convertMetadata {
		return nil
	}
	dataProfile, metadataProfile := layout.Profiles()
	args := []string{"balance", "start"}
	if convertData {
		args = append(args, "-dconvert="+string(dataProfile)+",soft")
	}
	// Metadata filters apply to system chunks as well.
	if convertMetadata {
		args = append(args, "-mconvert="+string(metadataProfile)+",soft")
	}
	return args
}

// resolveTargets finds the planned disks again and checks that each is
// still the disk that was planned and may be erased.
func (e *Executor) resolveTargets(ctx context.Context, disks []datavolume.DiskTarget) ([]BlockDevice, error) {
	if len(disks) == 0 {
		return nil, errors.New("no disks to erase")
	}
	devices := make([]BlockDevice, 0, len(disks))
	paths := make(map[string]struct{}, len(disks))
	for _, target := range disks {
		if strings.TrimSpace(target.DiskID) == "" || strings.TrimSpace(target.Fingerprint) == "" {
			return nil, errors.New("invalid disk target")
		}
		device, err := e.resolver.ResolveDevice(ctx, target.DiskID)
		if err != nil {
			return nil, fmt.Errorf("resolve stable disk ID: %w", err)
		}
		if device.Disk.ID.String() != target.DiskID || datavolume.FingerprintDisk(device.Disk) != target.Fingerprint {
			return nil, datavolume.ErrDiskChanged
		}
		if device.Disk.Role != hoststate.DiskRoleUnassigned || device.Disk.InUse || device.Disk.Removable || (device.Disk.Transport != hoststate.TransportSATA && device.Disk.Transport != hoststate.TransportNVMe) {
			return nil, datavolume.ErrDiskNotEligible
		}
		if !validDevicePath(device.DevicePath) || !validDevicePath(device.PartitionPath) || device.DevicePath == device.PartitionPath {
			return nil, errors.New("resolved block-device path is invalid")
		}
		if _, seen := paths[device.DevicePath]; seen {
			return nil, errors.New("the same disk was planned twice")
		}
		paths[device.DevicePath] = struct{}{}
		devices = append(devices, device)
	}
	return devices, nil
}

// prepareDisk erases the disk and gives it one GPT partition for Btrfs. The
// new partition is wiped as well: when it starts where an old one did, a
// signature inside it, such as an md superblock, would otherwise reappear.
func (e *Executor) prepareDisk(ctx context.Context, device BlockDevice) error {
	steps := []struct {
		name string
		args []string
	}{
		{name: "wipefs", args: []string{"--all", device.DevicePath}},
		{name: "parted", args: []string{"--script", device.DevicePath, "mklabel", "gpt", "mkpart", "primary", "btrfs", "1MiB", "100%"}},
		{name: "partprobe", args: []string{device.DevicePath}},
		{name: "udevadm", args: []string{"settle", "--timeout=10"}},
		{name: "wipefs", args: []string{"--all", device.PartitionPath}},
	}
	for _, step := range steps {
		if output, err := e.runner.Run(ctx, step.name, step.args, ""); err != nil {
			return commandError(step.name+" failed", err, output)
		}
	}
	return nil
}
