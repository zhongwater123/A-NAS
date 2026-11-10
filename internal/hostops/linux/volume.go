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
	"sync"
	"sync/atomic"
	"time"

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
	// SpreadStopped names the disks (devices and sizes, devicesKey) a spread
	// of the mirror finished on with space still unusable. The check does
	// not spread again until the disks change or the administrator asks.
	SpreadStopped string `json:"spreadStopped,omitempty"`
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
	if err := writeAtomic(e.mountUnitPath(), []byte(mountUnit(uuid, e.mountPoint)), 0o644); err != nil {
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
	_, _ = e.EnsureFamilyStore(ctx)
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

// mountOptions are the data volume's mount options. A device the volume was
// mounted from may vanish while the volume keeps running, as a mirror does
// when one disk fails, so the mount is not bound to it: without
// x-systemd.device-bound=no, systemd unmounts the volume when the device it
// names in the mount table goes away (ADR 0025).
const mountOptions = "noatime,compress=zstd:3,nodev,nosuid,noexec,x-systemd.device-bound=no"

// mountUnit is the data volume's mount unit. systemd waits for the
// filesystem's UUID device, which udev marks ready once the kernel knows
// every disk of the volume, for its device timeout (90 seconds by default);
// the unit then fails, and the Host Agent decides whether to mount a mirror
// without the missing disk.
func mountUnit(uuid, mountPoint string) string {
	return fmt.Sprintf(`[Unit]
Description=A-NAS data volume
After=local-fs-pre.target
Before=local-fs.target

[Mount]
What=UUID=%s
Where=%s
Type=btrfs
Options=%s
TimeoutSec=30

[Install]
WantedBy=local-fs.target
`, uuid, mountPoint, mountOptions)
}

// mountUnitUUID is the filesystem UUID the mount unit mounts, or "".
func mountUnitUUID(contents []byte) string {
	for line := range strings.SplitSeq(string(contents), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "What=UUID="); found {
			if uuid := strings.TrimSpace(value); datavolume.ValidFilesystemUUID(uuid) {
				return uuid
			}
		}
	}
	return ""
}

// EnsureMountUnit brings a mount unit written by an earlier release up to
// date and reports whether it changed it. It changes nothing the next mount
// would not: systemd keeps the dependencies of a volume mounted now until
// it is mounted again.
func (e *Executor) EnsureMountUnit(ctx context.Context) (bool, error) {
	contents, err := os.ReadFile(e.mountUnitPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the data-volume mount unit: %w", err)
	}
	uuid := mountUnitUUID(contents)
	if uuid == "" {
		return false, errors.New("the data-volume mount unit names no filesystem UUID")
	}
	want := mountUnit(uuid, e.mountPoint)
	if string(contents) == want {
		return false, nil
	}
	if err := writeAtomic(e.mountUnitPath(), []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("update the data-volume mount unit: %w", err)
	}
	if output, err := e.runner.Run(ctx, "systemctl", []string{"daemon-reload"}, ""); err != nil {
		return true, commandError("systemctl daemon-reload failed", err, output)
	}
	return true, nil
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
	uuid := mountUnitUUID(contents)
	if uuid == "" {
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
	if len(adding) > 0 {
		if err := e.beginAdding(operation.FilesystemUUID); err != nil {
			return datavolume.Volume{}, err
		}
		defer e.endAdding()
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
		e.endAdding()
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

// beginAdding refuses to add disks while Btrfs runs a balance or the Host
// Agent converts or spreads the volume, which may be between two balances
// the kernel does not show: btrfs device add would fail after the disks
// were erased. Until endAdding, the Host Agent's checks start no balance.
func (e *Executor) beginAdding(uuid string) error {
	e.conversionMu.Lock()
	defer e.conversionMu.Unlock()
	_, _, operation := linuxhoststate.ReadBtrfsAllocation(os.DirFS(e.systemRoot), strings.ToLower(uuid))
	if e.converting || e.adding || operation != hoststate.VolumeOperationNone {
		return datavolume.ErrVolumeBusy
	}
	e.adding = true
	return nil
}

func (e *Executor) endAdding() {
	e.conversionMu.Lock()
	e.adding = false
	e.conversionMu.Unlock()
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

// ContinueConversion starts converting the data volume to its layout, or
// spreading a mirror over all of its disks, now; spreading is tried again
// even where it last left space unusable.
func (e *Executor) ContinueConversion(ctx context.Context) error {
	if err := e.recordSpreadStopped(""); err != nil {
		return err
	}
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
// then periodically. Once nothing is left to convert, it spreads a mirror
// that cannot use all of its disks over them (reconcileSpread). It reports
// whether a balance started.
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
	uuid := strings.ToLower(marker.FilesystemUUID)
	// Without one of its disks a mirror cannot hold two copies: converting
	// would fail every time. It converts once the disk is back or replaced.
	if linuxhoststate.ReadMissingDevices(os.DirFS(e.systemRoot), uuid) > 0 {
		return false, nil
	}
	data, metadata, operation := linuxhoststate.ReadBtrfsAllocation(os.DirFS(e.systemRoot), uuid)
	args := conversionCommand(marker.Layout, data, metadata, operation)
	if args == nil {
		convertData, convertMetadata := datavolume.OutsideLayout(data, metadata, marker.Layout)
		if operation != hoststate.VolumeOperationNone || convertData || convertMetadata {
			return false, nil
		}
		return e.reconcileSpread(ctx, marker)
	}
	e.conversionMu.Lock()
	defer e.conversionMu.Unlock()
	if e.converting || e.adding {
		return false, nil
	}
	e.converting = true
	args = append(args, e.mountPoint)
	e.logger.InfoContext(ctx, "converting the data volume", "layout", marker.Layout, "command", "btrfs "+strings.Join(args, " "))
	//nolint:gosec // G118: a conversion runs for hours and outlives the request that starts it; converting tracks it.
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

// Spreading a mirror packs the data block groups at most half full, which
// frees whole ones on every disk without needing a new one, and then writes
// data block groups anew: Btrfs places each on the disks with the most space
// left, as the btrfs documentation advises after adding disks. The full
// balance starts with the newest block groups and does not come back to the
// ones it wrote, so it is cancelled as soon as no space is unusable: it
// rewrites only as much as the disks need, not the whole volume.
var (
	spreadPack = []string{"balance", "start", "-dusage=50"}
	spreadAll  = []string{"balance", "start", "--full-balance", "-d"}
)

// reconcileSpread spreads a mirror that cannot use space its disks have
// (datavolume.Unusable) over all of them: a disk that joined two full ones
// stays mostly unused until then, since each block group needs room on two
// disks. A spread cut short by a shutdown starts again at the next check. A
// spread that finished with space still unusable, for lack of room on the
// old disks, is recorded in the marker with the disks it ran on, and the
// check leaves them alone until they change or the administrator asks. It
// reports whether spreading started.
func (e *Executor) reconcileSpread(ctx context.Context, marker volumeMarker) (bool, error) {
	if marker.Layout != datavolume.LayoutMirror {
		return false, nil
	}
	unusable, devices, err := e.measureSpread(ctx, marker.FilesystemUUID)
	if err != nil {
		return false, err
	}
	if unusable == 0 {
		e.conversionMu.Lock()
		e.spreadStopped = false
		e.conversionMu.Unlock()
		if marker.SpreadStopped != "" {
			return false, e.recordSpreadStopped("")
		}
		return false, nil
	}
	e.conversionMu.Lock()
	defer e.conversionMu.Unlock()
	e.spreadStopped = marker.SpreadStopped == devicesKey(devices)
	if e.spreadStopped || e.converting || e.adding {
		return false, nil
	}
	e.converting = true
	e.logger.InfoContext(ctx, "spreading the data volume over all of its disks", "unusableBytes", unusable)
	//nolint:gosec // G118: like a conversion, spreading outlives the request that starts it; converting tracks it.
	go e.spread(marker.FilesystemUUID)
	return true, nil
}

func (e *Executor) spread(uuid string) {
	ctx := context.Background()
	defer func() {
		e.conversionMu.Lock()
		e.converting = false
		e.conversionMu.Unlock()
	}()
	e.balance(ctx, spreadPack)
	if unusable, _, err := e.measureSpread(ctx, uuid); err == nil && unusable > 0 {
		e.balanceUntilSpread(ctx, uuid)
	}
	unusable, devices, err := e.measureSpread(ctx, uuid)
	switch {
	case err != nil:
		e.logger.Error("the data volume could not be measured after spreading it", "error", err)
	case unusable == 0:
		e.logger.Info("the data volume can use all of its disks")
	default:
		e.logger.Warn("the data volume still cannot use all of its disks; it is spread again once its disks change or on request", "unusableBytes", unusable)
		if err := e.recordSpreadStopped(devicesKey(devices)); err != nil {
			e.logger.Error("record that spreading the data volume stopped", "error", err)
		}
	}
}

// balanceUntilSpread runs the full data balance and cancels it once no
// space is unusable, measuring every spreadPoll. It waits for its watch to
// end, so that no cancel reaches a later balance.
func (e *Executor) balanceUntilSpread(ctx context.Context, uuid string) {
	done := make(chan struct{})
	var watch sync.WaitGroup
	var cancelled atomic.Bool
	watch.Go(func() {
		ticker := time.NewTicker(e.spreadPoll)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			if unusable, _, err := e.measureSpread(ctx, uuid); err != nil || unusable > 0 {
				continue
			}
			cancelled.Store(true)
			if output, err := e.runner.Run(ctx, "btrfs", []string{"balance", "cancel", e.mountPoint}, ""); err != nil {
				e.logger.Warn("the spread balance could not be cancelled", "error", commandError("btrfs balance cancel", err, output))
			}
			return
		}
	})
	if output, err := e.runner.Run(ctx, "btrfs", append(slices.Clone(spreadAll), e.mountPoint), ""); err != nil && !cancelled.Load() {
		e.logger.Warn("a balance of the data volume stopped", "command", "btrfs "+strings.Join(spreadAll, " "), "error", commandError("btrfs balance", err, output))
	}
	close(done)
	watch.Wait()
}

func (e *Executor) balance(ctx context.Context, command []string) {
	if output, err := e.runner.Run(ctx, "btrfs", append(slices.Clone(command), e.mountPoint), ""); err != nil {
		e.logger.Warn("a balance of the data volume stopped", "command", "btrfs "+strings.Join(command, " "), "error", commandError("btrfs balance", err, output))
	}
}

// measureSpread reads how Btrfs allocates the mirror's devices, checked
// against sysfs as the host state reads them, and how much space the mirror
// cannot use yet. Unknown allocations are errors, not evidence that the
// spread finished. Bound even background reads so a stalled command cannot
// keep the spread watcher, and therefore the volume's operation lock, alive.
func (e *Executor) measureSpread(ctx context.Context, uuid string) (uint64, []hoststate.VolumeDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := e.runner.Output(ctx, "btrfs", []string{"filesystem", "show", "--raw", e.mountPoint}, "")
	if err != nil {
		return 0, nil, commandError("btrfs filesystem show", err, output)
	}
	devices := linuxhoststate.VolumeDevices(os.DirFS(e.systemRoot), strings.ToLower(uuid), output)
	if len(devices) < 2 {
		return 0, nil, errors.New("the data volume device allocation is unknown")
	}
	return datavolume.Unusable(datavolume.LayoutMirror, devices), devices, nil
}

// devicesKey names the disks of the volume by device and size, so that a
// recorded stop no longer applies once a disk is added, replaced or grown.
func devicesKey(devices []hoststate.VolumeDevice) string {
	keys := make([]string, 0, len(devices))
	for _, device := range devices {
		keys = append(keys, fmt.Sprintf("%d:%d", device.DevID, device.SizeBytes))
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

// recordSpreadStopped records in the marker the disks a spread left space
// unusable on, or clears the record ("").
func (e *Executor) recordSpreadStopped(devices string) error {
	e.conversionMu.Lock()
	e.spreadStopped = devices != ""
	e.conversionMu.Unlock()
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	marker, err := e.readMarker()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if marker.SpreadStopped == devices {
		return nil
	}
	marker.SpreadStopped = devices
	return e.writeMarker(marker)
}

// SpreadStopped reports whether spreading the mirror over the disks it has
// now left space unusable, as the last check found.
func (e *Executor) SpreadStopped() bool {
	e.conversionMu.Lock()
	defer e.conversionMu.Unlock()
	return e.spreadStopped
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
