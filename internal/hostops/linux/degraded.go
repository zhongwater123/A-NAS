package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

// A mirror whose disk is missing at boot keeps serving (ADR 0025, decision
// 6). systemd waits for every disk of the volume and gives up; the Host Agent
// then mounts the mirror without the missing disk, but only a mirror, only
// without one disk, and only on the disks it ran on last. What Linux and
// systemd do at each step is recorded in the investigation
// 2026-11-06-mirror-missing-a-disk.

// degradedRecord says the data volume ran without some of its disks: the
// survivors hold data the others lack. A mount without a survivor would run
// on stale disks and split the volume in two, so it is refused. The record
// is kept until redundancy is restored.
type degradedRecord struct {
	FilesystemUUID string    `json:"filesystemUuid"`
	Since          time.Time `json:"since"`
	// Survivors are the Btrfs device IDs the volume ran on.
	Survivors []uint64 `json:"survivors"`
}

func (r degradedRecord) valid() bool {
	return datavolume.ValidFilesystemUUID(r.FilesystemUUID) && !r.Since.IsZero() && len(r.Survivors) > 0
}

// btrfsDevice is one device of a mounted Btrfs filesystem, from sysfs
// devinfo/<devid>.
type btrfsDevice struct {
	ID      uint64
	Missing bool
}

// readBtrfsDevices lists the devices of the mounted filesystem, leaving out
// a replacement being written. A device absent when the volume was mounted
// is marked missing.
func readBtrfsDevices(root fs.FS, uuid string) []btrfsDevice {
	entries, err := fs.Glob(root, "sys/fs/btrfs/"+uuid+"/devinfo/*")
	if err != nil {
		return nil
	}
	flag := func(file string) bool {
		contents, err := fs.ReadFile(root, file)
		return err == nil && strings.TrimSpace(string(contents)) == "1"
	}
	var devices []btrfsDevice
	for _, entry := range entries {
		id, err := strconv.ParseUint(path.Base(entry), 10, 64)
		if err != nil || flag(entry+"/replace_target") {
			continue
		}
		devices = append(devices, btrfsDevice{ID: id, Missing: flag(entry+"/missing") || !flag(entry+"/writeable")})
	}
	return devices
}

// degradedMountRefusal is why the volume found by the probe mount must not
// run without its missing disks, or "" with the survivors when it may.
func degradedMountRefusal(marker volumeMarker, uuid string, devices []btrfsDevice, record *degradedRecord) ([]uint64, string) {
	if !strings.EqualFold(marker.FilesystemUUID, uuid) {
		return nil, "the volume's marker names another filesystem"
	}
	if marker.Layout != datavolume.LayoutMirror {
		return nil, "only a mirror runs without a disk; this volume's layout is " + string(marker.Layout)
	}
	var survivors []uint64
	for _, device := range devices {
		if !device.Missing {
			survivors = append(survivors, device.ID)
		}
	}
	missing := len(devices) - len(survivors)
	switch {
	case missing == 0:
		return nil, "no disk is missing, yet the volume did not mount"
	case missing > 1 || len(survivors) == 0:
		return nil, fmt.Sprintf("%d disks are missing; a mirror keeps working without one", missing)
	}
	if record != nil && strings.EqualFold(record.FilesystemUUID, uuid) {
		for _, id := range record.Survivors {
			if !slices.Contains(survivors, id) {
				return nil, fmt.Sprintf("Btrfs device %d, which the volume last ran on without the others, is missing; the disks here are older", id)
			}
		}
	}
	return survivors, ""
}

// onHost runs mount or umount as a transient systemd service: the Host
// Agent's own mount namespace would keep a mount it made itself. A mount
// made in the host's namespace reaches the Host Agent's as it was made; a
// later remount does not, so the volume is mounted read-write in one step.
func (e *Executor) onHost(ctx context.Context, command ...string) error {
	args := append([]string{"--wait", "--collect", "--quiet", "--unit=anas-data-volume-mount", "--"}, command...)
	if output, err := e.runner.Run(ctx, "systemd-run", args, ""); err != nil {
		return commandError(command[0]+" failed", err, output)
	}
	return nil
}

// MountDegraded mounts a mirror that systemd gave up mounting because a disk
// is missing, and reports whether it did. A probe mount, read-only, shows
// the volume's layout and which disks the kernel found; the volume is then
// mounted read-write without the missing disk, or left offline with the
// reason logged.
func (e *Executor) MountDegraded(ctx context.Context) (bool, error) {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	if e.dataVolumeReady() {
		return false, nil
	}
	if e.degradedError != nil {
		return false, fmt.Errorf("the volume may not run without a disk: %w", e.degradedError)
	}
	contents, err := os.ReadFile(e.mountUnitPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the data-volume mount unit: %w", err)
	}
	uuid := strings.ToLower(mountUnitUUID(contents))
	if uuid == "" {
		return false, nil
	}
	// While systemd still waits for the disks, a slow one may yet come.
	state, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--value", e.mountUnitName}, "")
	if err != nil {
		return false, commandError("systemctl show failed", err, state)
	}
	if active := strings.TrimSpace(string(state)); active != "failed" && active != "inactive" {
		return false, nil
	}
	members, err := e.volumeMembers(ctx, uuid)
	if err != nil || len(members) == 0 {
		return false, err
	}
	if err := e.onHost(ctx, "mount", "-t", "btrfs", "-o", "degraded,ro,"+mountOptions, members[0], e.mountPoint); err != nil {
		return false, fmt.Errorf("mount the data volume read-only to look at it: %w", err)
	}
	marker, markerErr := e.readMarker()
	survivors, refusal := degradedMountRefusal(marker, uuid, readBtrfsDevices(os.DirFS(e.systemRoot), uuid), e.degraded)
	if markerErr != nil {
		refusal = "the volume has no valid marker"
	}
	if err := e.onHost(ctx, "umount", e.mountPoint); err != nil {
		return false, fmt.Errorf("unmount the data volume after looking at it: %w", err)
	}
	if refusal != "" {
		e.logger.WarnContext(ctx, "the data volume stays offline", "reason", refusal)
		return false, nil
	}
	record := degradedRecord{FilesystemUUID: uuid, Since: e.now().UTC(), Survivors: survivors}
	if e.degraded != nil && strings.EqualFold(e.degraded.FilesystemUUID, uuid) {
		record.Since = e.degraded.Since
	}
	if err := e.saveDegraded(&record); err != nil {
		return false, err
	}
	if err := e.onHost(ctx, "mount", "-t", "btrfs", "-o", "degraded,"+mountOptions, members[0], e.mountPoint); err != nil {
		return false, fmt.Errorf("mount the data volume without its missing disk: %w", err)
	}
	// udev held the disks back from systemd while one was missing; the kernel
	// now counts the volume complete, so udev releases them.
	if output, err := e.runner.Run(ctx, "udevadm", append([]string{"trigger", "--action=change"}, members...), ""); err != nil {
		e.logger.WarnContext(ctx, "udev did not re-probe the data volume's disks", "error", commandError("udevadm trigger", err, output))
	}
	e.logger.WarnContext(ctx, "mounted the data volume without a missing disk; new data has one copy until the disk is back or replaced", "survivors", survivors)
	return true, nil
}

// volumeMembers are the partitions the kernel can see with the filesystem
// UUID.
func (e *Executor) volumeMembers(ctx context.Context, uuid string) ([]string, error) {
	output, err := e.runner.Output(ctx, "blkid", []string{"--cache-file", "/dev/null", "--match-token", "UUID=" + uuid, "--output", "device"}, "")
	if err != nil {
		// blkid exits 2 when nothing matches.
		return nil, nil
	}
	var members []string
	for line := range strings.SplitSeq(string(output), "\n") {
		if device := strings.TrimSpace(line); validDevicePath(device) {
			members = append(members, device)
		}
	}
	return members, nil
}

// RestoringRedundancy reports whether the data volume ran without a disk and
// has not been checked since: data written meanwhile has one copy.
func (e *Executor) RestoringRedundancy() bool {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	return e.degraded != nil
}

// RestoreRedundancy finishes a period without a disk once the volume has all
// its disks again: after the conversion check turned what was written with
// one copy back into a mirror, a scrub rewrites what the returning disk
// missed, and the record is dropped. It reports whether a scrub started.
func (e *Executor) RestoreRedundancy(ctx context.Context) (bool, error) {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	if e.degraded == nil || e.scrubbing || !e.dataVolumeReady() {
		return false, nil
	}
	marker, err := e.readMarker()
	if err != nil {
		return false, err
	}
	uuid := strings.ToLower(marker.FilesystemUUID)
	if !strings.EqualFold(uuid, e.degraded.FilesystemUUID) {
		e.logger.WarnContext(ctx, "dropping the record of another volume that ran without a disk", "filesystem", e.degraded.FilesystemUUID)
		return false, e.saveDegraded(nil)
	}
	root := os.DirFS(e.systemRoot)
	if linuxhoststate.ReadMissingDevices(root, uuid) > 0 {
		return false, nil
	}
	data, metadata, operation := linuxhoststate.ReadBtrfsAllocation(root, uuid)
	if convertData, convertMetadata := datavolume.OutsideLayout(data, metadata, marker.Layout); convertData || convertMetadata || operation != hoststate.VolumeOperationNone {
		return false, nil
	}
	e.scrubbing = true
	e.logger.InfoContext(ctx, "the data volume has all its disks again; scrubbing it to repair what the returned disk missed")
	//nolint:gosec // G118: a scrub runs for hours and outlives the check that starts it; scrubbing tracks it.
	go func() {
		output, err := e.runner.Run(context.Background(), "btrfs", []string{"scrub", "start", "-B", e.mountPoint}, "")
		e.degradedMu.Lock()
		defer e.degradedMu.Unlock()
		e.scrubbing = false
		if err != nil {
			e.logger.Error("data-volume scrub stopped; the next check starts it again", "error", commandError("btrfs scrub", err, output))
			return
		}
		if err := e.saveDegraded(nil); err != nil {
			e.logger.Error("could not drop the record of running without a disk", "error", err)
			return
		}
		e.logger.Info("the data volume has its redundancy back")
	}()
	return true, nil
}

// saveDegraded writes the record, or removes it for nil. Callers hold
// degradedMu.
func (e *Executor) saveDegraded(record *degradedRecord) error {
	if record == nil {
		if err := os.Remove(e.degradedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the record of running without a disk: %w", err)
		}
		e.degraded = nil
		return nil
	}
	contents, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeAtomic(e.degradedPath, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("record running without a disk: %w", err)
	}
	e.degraded = record
	return nil
}

// loadDegraded reads the record at startup. One that cannot be read keeps
// the volume from running without a disk until it is fixed: it may name the
// only disks safe to run on.
func (e *Executor) loadDegraded() error {
	contents, err := e.readState(e.degradedPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var record degradedRecord
	if err := json.Unmarshal(contents, &record); err != nil || !record.valid() {
		return errors.New("the record of running without a disk is invalid")
	}
	e.degraded = &record
	return nil
}
