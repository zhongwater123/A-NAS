package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

var filesystemUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{16,64}$`)

// volumeMarker is .a-nas-volume.json at the root of the data volume: the
// evidence that the mounted filesystem is the data volume, and the layout
// the Host Agent keeps it converted to (ADR 0025).
type volumeMarker struct {
	FilesystemUUID string `json:"filesystemUuid"`
	DiskID         string `json:"diskId,omitempty"`
	FormatVersion  int    `json:"formatVersion"`
	// Layout is absent on volumes created on one disk before ADR 0025.
	Layout storage.Layout `json:"layout,omitempty"`
}

func (e *Executor) markerPath() string { return filepath.Join(e.mountPoint, ".a-nas-volume.json") }

func (e *Executor) readMarker() (volumeMarker, error) {
	contents, err := os.ReadFile(e.markerPath())
	if err != nil {
		return volumeMarker{}, err
	}
	var marker volumeMarker
	if err := json.Unmarshal(contents, &marker); err != nil || marker.FormatVersion != 1 || !filesystemUUIDPattern.MatchString(marker.FilesystemUUID) {
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

// CreateVolume erases the planned disks and formats them as the data volume
// with the layout, then mounts it by filesystem UUID.
func (e *Executor) CreateVolume(ctx context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	if e.resolver == nil || strings.TrimSpace(request.PlanID) == "" || !request.Layout.Fits(len(request.Disks)) {
		return storage.Volume{}, errors.New("invalid create-volume request")
	}
	devices, err := e.resolveTargets(ctx, request.Disks)
	if err != nil {
		return storage.Volume{}, err
	}
	for _, device := range devices {
		if err := e.prepareDisk(ctx, device); err != nil {
			return storage.Volume{}, err
		}
	}
	data, metadata := request.Layout.Profiles()
	format := []string{"--force", "--label", "ANAS_DATA", "--data", string(data), "--metadata", string(metadata)}
	for _, device := range devices {
		format = append(format, device.PartitionPath)
	}
	if output, err := e.runner.Run(ctx, "mkfs.btrfs", format, ""); err != nil {
		return storage.Volume{}, commandError("mkfs.btrfs failed", err, output)
	}
	uuidBytes, err := e.runner.Run(ctx, "blkid", []string{"--output", "value", "--match-tag", "UUID", devices[0].PartitionPath}, "")
	if err != nil {
		return storage.Volume{}, fmt.Errorf("read Btrfs UUID: %w", err)
	}
	uuid := strings.TrimSpace(string(uuidBytes))
	if !filesystemUUIDPattern.MatchString(uuid) {
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
	// A filesystem on several disks mounts once the kernel knows them all.
	steps := []struct {
		name string
		args []string
	}{
		{name: "btrfs", args: []string{"device", "scan"}},
		{name: "systemctl", args: []string{"daemon-reload"}},
		{name: "systemctl", args: []string{"enable", "--now", e.mountUnitName}},
		{name: "btrfs", args: []string{"subvolume", "create", filepath.Join(e.mountPoint, "spaces")}},
	}
	for _, step := range steps {
		if output, err := e.runner.Run(ctx, step.name, step.args, ""); err != nil {
			return storage.Volume{}, commandError(step.name+" failed after formatting", err, output)
		}
	}
	if err := os.MkdirAll(filepath.Join(e.mountPoint, ".a-nas-snapshots"), 0o700); err != nil {
		return storage.Volume{}, err
	}
	marker := volumeMarker{FilesystemUUID: uuid, DiskID: request.Disks[0].DiskID, FormatVersion: 1}
	if len(devices) > 1 {
		marker.Layout = request.Layout
	}
	if err := e.writeMarker(marker); err != nil {
		return storage.Volume{}, fmt.Errorf("write data-volume identity marker: %w", err)
	}
	if _, err := e.materializeRegisteredSpaces(ctx); err != nil {
		return storage.Volume{}, fmt.Errorf("materialize registered spaces: %w", err)
	}
	// Best effort: a failure only delays managed stores until the Host Agent's
	// periodic repair, which logs it.
	_, _ = e.EnsurePhotosStore(ctx)
	_, _ = e.EnsureSharesStore(ctx)
	volume := storage.Volume{ID: "volume:data", FilesystemUUID: uuid, Layout: request.Layout, State: storage.VolumeStateAvailable}
	sizes := make([]uint64, len(devices))
	for i, device := range devices {
		volume.DiskIDs = append(volume.DiskIDs, device.Disk.ID.String())
		sizes[i] = device.Disk.CapacityBytes
	}
	volume.CapacityBytes = storage.UsableBytes(request.Layout, sizes)
	return volume, nil
}

// AddDisks erases the planned disks, adds them to the mounted data volume
// and starts converting it to the layout in the background. Files stay
// where they are and the volume stays in use throughout.
func (e *Executor) AddDisks(ctx context.Context, request storage.AddDisksRequest) (storage.Volume, error) {
	if e.resolver == nil || strings.TrimSpace(request.PlanID) == "" || len(request.Disks) == 0 || request.Layout == storage.LayoutSingle || !request.Layout.Fits(len(request.Disks)+1) {
		return storage.Volume{}, errors.New("invalid add-disks request")
	}
	if !e.dataVolumeReady() {
		return storage.Volume{}, storage.ErrVolumeUnavailable
	}
	marker, err := e.readMarker()
	if err != nil {
		return storage.Volume{}, err
	}
	if !strings.EqualFold(marker.FilesystemUUID, request.FilesystemUUID) {
		return storage.Volume{}, storage.ErrVolumeChanged
	}
	devices, err := e.resolveTargets(ctx, request.Disks)
	if err != nil {
		return storage.Volume{}, err
	}
	for _, device := range devices {
		if err := e.prepareDisk(ctx, device); err != nil {
			return storage.Volume{}, err
		}
	}
	add := []string{"device", "add", "-f"}
	for _, device := range devices {
		add = append(add, device.PartitionPath)
	}
	if output, err := e.runner.Run(ctx, "btrfs", append(add, e.mountPoint), ""); err != nil {
		return storage.Volume{}, commandError("btrfs device add failed", err, output)
	}
	// The disks belong to the volume now; record the layout before anything
	// else can fail, so the Host Agent's check converts to it regardless.
	marker.Layout = request.Layout
	if err := e.writeMarker(marker); err != nil {
		return storage.Volume{}, fmt.Errorf("record the data-volume layout: %w", err)
	}
	if _, err := e.ReconcileConversion(ctx); err != nil {
		e.logger.WarnContext(ctx, "data-volume conversion did not start; the periodic check retries it", "error", err)
	}
	volume := storage.Volume{FilesystemUUID: marker.FilesystemUUID, Layout: request.Layout, State: storage.VolumeStateAvailable}
	for _, device := range devices {
		volume.DiskIDs = append(volume.DiskIDs, device.Disk.ID.String())
	}
	return volume, nil
}

// ContinueConversion starts converting the data volume to its layout now.
func (e *Executor) ContinueConversion(ctx context.Context) error {
	started, err := e.ReconcileConversion(ctx)
	if err == nil && !started {
		return storage.ErrNoConversion
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
	marker, err := e.readMarker()
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
func conversionCommand(layout storage.Layout, data, metadata []hoststate.ProfileUsage, operation hoststate.VolumeOperation) []string {
	switch operation {
	case hoststate.VolumeOperationBalancePaused:
		return []string{"balance", "resume"}
	case hoststate.VolumeOperationNone:
	default:
		return nil
	}
	convertData, convertMetadata := storage.OutsideLayout(data, metadata, layout)
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
func (e *Executor) resolveTargets(ctx context.Context, disks []storage.DiskTarget) ([]BlockDevice, error) {
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
		if device.Disk.ID.String() != target.DiskID || storage.FingerprintDisk(device.Disk) != target.Fingerprint {
			return nil, storage.ErrDiskChanged
		}
		if device.Disk.Role != hoststate.DiskRoleUnassigned || device.Disk.InUse || device.Disk.Removable || (device.Disk.Transport != hoststate.TransportSATA && device.Disk.Transport != hoststate.TransportNVMe) {
			return nil, storage.ErrDiskNotEligible
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
