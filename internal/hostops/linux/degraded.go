package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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

// degradedMountRefusal is why the volume found by the probe mount must not
// run without its missing disks, or "" with the survivors when it may.
func degradedMountRefusal(marker volumeMarker, uuid string, devices []linuxhoststate.BtrfsDevice, record *degradedRecord) ([]uint64, string) {
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

const (
	// probeOptions mount the volume only to look at it. A read-only Btrfs
	// mount still replays the log tree and writes the result; nologreplay
	// keeps the probe from writing to disks the volume may not run on.
	probeOptions = "degraded,ro,rescue=nologreplay," + mountOptions
	// degradedOptions run the mirror without its missing disk. A balance
	// paused at shutdown would resume on the remaining disk and rewrite the
	// mirror's block groups as single copies; it stays paused instead.
	degradedOptions = "degraded,skip_balance," + mountOptions
)

// MountDegraded mounts a mirror that systemd gave up mounting because a disk
// is missing, and reports whether it did. A probe mount, read-only, shows
// the volume's layout and which disks the kernel found; the volume is then
// mounted read-write without the missing disk, or left offline with the
// reason logged. A volume an administrator unmounted stays unmounted.
func (e *Executor) MountDegraded(ctx context.Context) (bool, error) {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	if e.degradedError != nil {
		return false, fmt.Errorf("the volume may not run without a disk: %w", e.degradedError)
	}
	if e.dataVolumeReady() {
		// A probe the last check could not unmount holds the volume
		// read-only; unmount it and start over.
		if !e.probeLeftMounted(ctx) {
			return false, nil
		}
		if err := e.onHost(ctx, "umount", e.mountPoint); err != nil {
			return false, fmt.Errorf("unmount the data volume a probe left read-only: %w", err)
		}
		e.unmountedProbe = true
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
	if gaveUp, err := e.mountGaveUp(ctx); err != nil || !gaveUp {
		return false, err
	}
	members, err := e.volumeMembers(ctx, uuid)
	if err != nil || len(members) == 0 {
		return false, err
	}
	if err := e.onHost(ctx, "mount", "-t", "btrfs", "-o", probeOptions, members[0], e.mountPoint); err != nil {
		return false, fmt.Errorf("mount the data volume read-only to look at it: %w", err)
	}
	marker, markerErr := e.readMarker()
	survivors, refusal := degradedMountRefusal(marker, uuid, linuxhoststate.ReadBtrfsDevices(os.DirFS(e.systemRoot), uuid), e.degraded)
	if markerErr != nil {
		refusal = "the volume has no valid marker"
	}
	if err := e.onHost(ctx, "umount", e.mountPoint); err != nil {
		return false, fmt.Errorf("unmount the data volume after looking at it: %w", err)
	}
	e.unmountedProbe = true
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
	if err := e.onHost(ctx, "mount", "-t", "btrfs", "-o", degradedOptions, members[0], e.mountPoint); err != nil {
		return false, fmt.Errorf("mount the data volume without its missing disk: %w", err)
	}
	e.unmountedProbe = false
	// udev held the disks back from systemd while one was missing; the kernel
	// now counts the volume complete, so udev releases them.
	if output, err := e.runner.Run(ctx, "udevadm", append([]string{"trigger", "--action=change"}, members...), ""); err != nil {
		e.logger.WarnContext(ctx, "udev did not re-probe the data volume's disks", "error", commandError("udevadm trigger", err, output))
	}
	e.logger.WarnContext(ctx, "mounted the data volume without a missing disk; new data has one copy until the disk is back or replaced", "survivors", survivors)
	return true, nil
}

// mountGaveUp reports whether systemd tried to mount the volume and gave up:
// the mount unit failed, or it has not been active since boot, as when its
// disks did not all appear in time. A volume that was mounted and then
// stopped, as an administrator does for maintenance, does not count, unless
// what systemd last saw mounted was the Host Agent's own probe.
func (e *Executor) mountGaveUp(ctx context.Context) (bool, error) {
	output, err := e.runner.Output(ctx, "systemctl", []string{"show", "--property=ActiveState", "--property=ActiveEnterTimestampMonotonic", e.mountUnitName}, "")
	if err != nil {
		return false, commandError("systemctl show failed", err, output)
	}
	properties := make(map[string]string)
	for line := range strings.SplitSeq(string(output), "\n") {
		if key, value, found := strings.Cut(strings.TrimSpace(line), "="); found {
			properties[key] = value
		}
	}
	switch properties["ActiveState"] {
	case "failed":
		return true, nil
	case "inactive":
		return properties["ActiveEnterTimestampMonotonic"] == "0" || e.unmountedProbe, nil
	default:
		return false, nil
	}
}

// probeLeftMounted reports whether the volume at the mount point is a probe
// the Host Agent mounted read-only: read-only per mount and degraded, unlike
// a volume Btrfs turned read-only after an error, which stays mounted rw.
func (e *Executor) probeLeftMounted(ctx context.Context) bool {
	output, err := e.runner.Output(ctx, "findmnt", []string{"--noheadings", "--output", "VFS-OPTIONS,FS-OPTIONS", "--mountpoint", e.mountPoint}, "")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(output))
	return len(fields) == 2 && slices.Contains(strings.Split(fields[0], ","), "ro") && slices.Contains(strings.Split(fields[1], ","), "degraded")
}

// volumeMembers are the partitions the kernel can see with the filesystem
// UUID.
func (e *Executor) volumeMembers(ctx context.Context, uuid string) ([]string, error) {
	output, err := e.runner.Output(ctx, "blkid", []string{"--cache-file", "/dev/null", "--match-token", "UUID=" + uuid, "--output", "device"}, "")
	// blkid exits 2 when no device matches.
	if exitCode(err) == 2 {
		return nil, nil
	}
	if err != nil {
		return nil, commandError("blkid failed", err, output)
	}
	var members []string
	for line := range strings.SplitSeq(string(output), "\n") {
		if device := strings.TrimSpace(line); validDevicePath(device) {
			members = append(members, device)
		}
	}
	return members, nil
}

// exitCode is the exit status of a command that ran and failed, or -1.
func exitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

// RestoringRedundancy reports whether the data volume ran without a disk and
// has not been checked since: data written meanwhile has one copy. It does
// not wait for a mount in progress: the host state asks on every read.
func (e *Executor) RestoringRedundancy() bool {
	return e.restoring.Load()
}

// RestoreRedundancy finishes a period without a disk once the volume has all
// its disks again: after the conversion check turned what was written with
// one copy back into a mirror, a scrub rewrites what the returning disk
// missed, and the record is dropped. It reports whether a scrub started. A
// scrub that stopped is tried again after scrubRetry.
func (e *Executor) RestoreRedundancy(ctx context.Context) (bool, error) {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	if e.degraded == nil || e.scrubbing || e.now().Before(e.scrubRetryAt) || !e.dataVolumeReady() {
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
		e.finishScrub(output, err)
	}()
	return true, nil
}

// scrubRetry is how long a scrub that stopped waits before the next one.
const scrubRetry = 6 * time.Hour

// finishScrub ends a period without a disk once its scrub has run. A scrub
// that found what neither copy could repair (btrfs exits 3) has still done
// all it can: the record goes, and the damage shows as Btrfs errors in the
// system health. One that stopped, at a shutdown or on a read error, runs
// again after scrubRetry, not at every check.
func (e *Executor) finishScrub(output []byte, err error) {
	e.degradedMu.Lock()
	defer e.degradedMu.Unlock()
	e.scrubbing = false
	switch {
	case exitCode(err) == 3:
		e.logger.Error("the data-volume scrub found data neither copy could repair; the kernel log names the files", "error", commandError("btrfs scrub", err, output))
	case err != nil:
		e.scrubRetryAt = e.now().Add(scrubRetry)
		e.logger.Error("data-volume scrub stopped; it runs again later", "retry_at", e.scrubRetryAt, "error", commandError("btrfs scrub", err, output))
		return
	}
	if err := e.saveDegraded(nil); err != nil {
		e.logger.Error("could not drop the record of running without a disk", "error", err)
		return
	}
	e.logger.Info("the data volume has its redundancy back")
}

// saveDegraded writes the record, or removes it for nil. Callers hold
// degradedMu.
func (e *Executor) saveDegraded(record *degradedRecord) error {
	if record == nil {
		if err := os.Remove(e.degradedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the record of running without a disk: %w", err)
		}
		e.degraded = nil
		e.restoring.Store(false)
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
	e.restoring.Store(true)
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
	e.restoring.Store(true)
	return nil
}

// replaceDisk erases the new disk and rebuilds onto it, from the mirror's
// other copy, the disk the mounted mirror runs without. Btrfs runs the
// rebuild in the kernel: it goes on when the Host Agent stops and starts
// again from the beginning after a reboot, so a retry of the plan waits for
// a rebuild that runs and finishes one that is done. A disk the size of the
// missing one or larger is required; Btrfs also needs some unallocated
// space on the remaining disk and fails the rebuild without it.
func (e *Executor) replaceDisk(ctx context.Context, operation *volumeOperation) (datavolume.Volume, error) {
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
	uuid := strings.ToLower(mounted.FilesystemUUID)
	root := os.DirFS(e.systemRoot)
	target := operation.Disks[0]
	joined := slices.ContainsFunc(mounted.DiskIDs, func(id hoststate.ResourceID) bool { return id.String() == target.DiskID })
	rebuilding := e.replacing(root, uuid)
	if !joined && !rebuilding {
		var missing []uint64
		for _, device := range linuxhoststate.ReadBtrfsDevices(root, uuid) {
			if device.Missing {
				missing = append(missing, device.ID)
			}
		}
		if len(missing) != 1 {
			return datavolume.Volume{}, fmt.Errorf("%w: %d disks are missing, want one", datavolume.ErrVolumeChanged, len(missing))
		}
		devices, err := e.resolveTargets(ctx, operation.Disks)
		if err != nil {
			return datavolume.Volume{}, err
		}
		e.enter(operation, datavolume.StepErase)
		if err := e.prepareDisk(ctx, devices[0]); err != nil {
			return datavolume.Volume{}, err
		}
		e.enter(operation, datavolume.StepReplace)
		// The degraded mount keeps a balance paused at shutdown from running
		// on one disk, but a paused balance blocks the rebuild. Cancelling it
		// loses nothing: the conversion check redoes what is left.
		if e.balancePaused(root, uuid) {
			if output, err := e.runner.Run(ctx, "btrfs", []string{"balance", "cancel", e.mountPoint}, ""); err != nil {
				return datavolume.Volume{}, commandError("btrfs balance cancel failed", err, output)
			}
		}
		start := []string{"replace", "start", strconv.FormatUint(missing[0], 10), devices[0].PartitionPath, e.mountPoint}
		if output, err := e.runner.Run(ctx, "btrfs", start, ""); err != nil {
			return datavolume.Volume{}, commandError("btrfs replace start failed", err, output)
		}
		rebuilding = true
	} else {
		e.enter(operation, datavolume.StepReplace)
	}
	// A disk that joined with no rebuild running was rebuilt before a
	// restart cut the Host Agent off; after a reboot btrfs reports such a
	// rebuild as never started, so there is nothing to wait for.
	if rebuilding {
		if err := e.awaitReplace(ctx, operation); err != nil {
			return datavolume.Volume{}, err
		}
	}
	// The new disk may be larger than the one it replaced.
	for _, device := range linuxhoststate.ReadBtrfsDevices(root, uuid) {
		resize := []string{"filesystem", "resize", strconv.FormatUint(device.ID, 10) + ":max", e.mountPoint}
		if output, err := e.runner.Run(ctx, "btrfs", resize, ""); err != nil {
			e.logger.WarnContext(ctx, "the data volume did not grow onto the whole new disk", "error", commandError("btrfs filesystem resize", err, output))
		}
	}
	// udev probed the new partition while it was wiped, as after adding a
	// disk; probe it again so lsblk shows the filesystem.
	if device, err := e.resolver.ResolveDevice(ctx, target.DiskID); err == nil && validDevicePath(device.PartitionPath) {
		if output, err := e.runner.Run(ctx, "udevadm", []string{"trigger", "--action=change", device.PartitionPath}, ""); err != nil {
			e.logger.WarnContext(ctx, "udev did not re-probe the new disk", "error", commandError("udevadm trigger", err, output))
		}
	}
	// What was written without the disk has one copy; the conversion check
	// turns it back into a mirror, and the scrub that follows checks it all.
	if _, err := e.ReconcileConversion(ctx); err != nil {
		e.logger.WarnContext(ctx, "data-volume conversion did not start; the periodic check retries it", "error", err)
	}
	volume := datavolume.Volume{ID: "volume:data", FilesystemUUID: mounted.FilesystemUUID, Layout: datavolume.LayoutMirror, State: datavolume.VolumeStateAvailable}
	for _, id := range mounted.DiskIDs {
		volume.DiskIDs = append(volume.DiskIDs, id.String())
	}
	if !joined {
		volume.DiskIDs = append(volume.DiskIDs, target.DiskID)
	}
	return volume, nil
}

// replacing reports whether Btrfs rebuilds a disk of the filesystem now.
func (e *Executor) replacing(root fs.FS, uuid string) bool {
	return exclusiveOperation(root, uuid) == "device replace"
}

// balancePaused reports whether a balance of the filesystem waits paused.
func (e *Executor) balancePaused(root fs.FS, uuid string) bool {
	return exclusiveOperation(root, uuid) == "balance paused"
}

// exclusiveOperation is the operation Btrfs runs exclusively on the
// filesystem, as sysfs names it.
func exclusiveOperation(root fs.FS, uuid string) string {
	contents, err := fs.ReadFile(root, "sys/fs/btrfs/"+uuid+"/exclusive_operation")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(contents))
}

// awaitReplace follows a rebuild until it finishes, recording its progress.
func (e *Executor) awaitReplace(ctx context.Context, operation *volumeOperation) error {
	for {
		output, err := e.runner.Output(ctx, "btrfs", []string{"replace", "status", "-1", e.mountPoint}, "")
		if err != nil {
			return commandError("btrfs replace status failed", err, output)
		}
		finished, percent, err := parseReplaceStatus(string(output))
		if err != nil {
			return err
		}
		if finished {
			return nil
		}
		e.operationsMu.Lock()
		operation.Progress = percent
		e.operationsMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// parseReplaceStatus reads one line of "btrfs replace status -1". It parses
// text because nothing better exists: btrfs-progs 6.14 has no machine-readable
// output for replace status (--format json is refused), and its ioctl would
// need a binary parser of the kernel's struct instead. While the rebuild
// runs "28.2% done, 0 write errs, 0 uncorr. read errs", then "Started on
// <time>, finished on <time>, ...", or "... canceled on ..." and "Never
// started" when there is nothing to wait for.
func parseReplaceStatus(output string) (finished bool, percent int, err error) {
	line := strings.TrimSpace(output)
	switch {
	case strings.Contains(line, "finished on"):
		return true, 100, nil
	case strings.Contains(line, "canceled on"), strings.HasPrefix(line, "Never started"):
		return false, 0, fmt.Errorf("the rebuild stopped: %s", line)
	}
	value, _, found := strings.Cut(line, "% done")
	if !found {
		return false, 0, fmt.Errorf("unexpected btrfs replace status %q", line)
	}
	done, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || done < 0 || done > 100 {
		return false, 0, fmt.Errorf("unexpected btrfs replace status %q", line)
	}
	return false, int(done), nil
}
