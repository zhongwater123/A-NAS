package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func TestConversionCommandConvertsOnlyWhatIsOutsideTheLayout(t *testing.T) {
	type usage = []hoststate.ProfileUsage
	single := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileSingle, UsedBytes: used}
	}
	dup := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileDup, UsedBytes: used}
	}
	raid1 := func(used uint64) hoststate.ProfileUsage {
		return hoststate.ProfileUsage{Profile: hoststate.ProfileRAID1, UsedBytes: used}
	}
	for _, test := range []struct {
		name      string
		layout    datavolume.Layout
		data      usage
		metadata  usage
		operation hoststate.VolumeOperation
		want      []string
	}{
		{"single disk joined by a mirror", datavolume.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft", "-mconvert=raid1,soft"}},
		{"cancelled halfway", datavolume.LayoutMirror, usage{single(4), raid1(5)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid1,soft"}},
		{"span keeps data single", datavolume.LayoutSpan, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-mconvert=raid1,soft"}},
		{"stripe", datavolume.LayoutStripe, usage{single(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone,
			[]string{"balance", "start", "-dconvert=raid0,soft"}},
		{"empty leftovers", datavolume.LayoutMirror, usage{single(0), raid1(9)}, usage{raid1(1)}, hoststate.VolumeOperationNone, nil},
		{"already converting", datavolume.LayoutMirror, usage{single(9)}, usage{dup(1)}, hoststate.VolumeOperationBalance, nil},
		{"another operation", datavolume.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationOther, nil},
		{"paused at unmount", datavolume.LayoutMirror, usage{single(9)}, nil, hoststate.VolumeOperationBalancePaused, []string{"balance", "resume"}},
	} {
		if got := conversionCommand(test.layout, test.data, test.metadata, test.operation); !slices.Equal(got, test.want) {
			t.Errorf("%s: command = %v, want %v", test.name, got, test.want)
		}
	}
}

// Disks whose addition was cut short after Btrfs added them still get the
// layout their plan asked for: it takes effect once the kernel lists them as
// devices of the volume (issue #165).
func TestAPendingLayoutTakesEffectOnceTheVolumeHasTheDisks(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	systemRoot := t.TempDir()
	executor := NewExecutor(nil, nil, Options{SystemRoot: systemRoot, MountPoint: t.TempDir()})
	if err := executor.writeMarker(volumeMarker{
		FilesystemUUID: uuid, FormatVersion: 1, Pending: &pendingLayout{PlanID: "plan:add", Layout: datavolume.LayoutMirror, Devices: 2},
	}); err != nil {
		t.Fatal(err)
	}
	devices := filepath.Join(systemRoot, "sys", "fs", "btrfs", uuid, "devices")
	if err := os.MkdirAll(devices, 0o755); err != nil {
		t.Fatal(err)
	}
	join := func(name string) {
		if err := os.WriteFile(filepath.Join(devices, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	join("sda1")
	if marker, err := executor.promotePendingLayout(); err != nil || marker.Layout != "" || marker.Pending == nil {
		t.Fatalf("marker with one of two devices = %+v, %v; want the layout still pending", marker, err)
	}
	join("sdb1")
	if marker, err := executor.promotePendingLayout(); err != nil || marker.Layout != datavolume.LayoutMirror || marker.Pending != nil {
		t.Fatalf("marker with both devices = %+v, %v; want the mirror", marker, err)
	}
	if marker, err := executor.readMarker(); err != nil || marker.Layout != datavolume.LayoutMirror || marker.Pending != nil {
		t.Fatalf("stored marker = %+v, %v; want the mirror recorded", marker, err)
	}
}

// scriptedBtrfs answers `btrfs filesystem show` with one allocation after
// another, the last one for good, and records the other commands run. A
// balance that fails changes nothing; the full balance, when hold is set,
// moves to the next allocation and runs until a `balance cancel`.
type scriptedBtrfs struct {
	mu       sync.Mutex
	shows    []string
	commands []string
	// fail makes balances whose arguments contain it fail.
	fail string
	// hold keeps the full balance running until it is cancelled.
	hold      bool
	cancelled chan struct{}
}

func (r *scriptedBtrfs) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	line := strings.Join(append([]string{name}, args...), " ")
	r.commands = append(r.commands, line)
	if r.fail != "" && strings.Contains(line, r.fail) {
		r.mu.Unlock()
		return []byte("ERROR: error during balancing '/data': No space left on device"), errors.New("exit status 1")
	}
	if len(r.shows) > 1 {
		r.shows = r.shows[1:]
	}
	switch {
	case strings.Contains(line, "balance cancel"):
		close(r.cancelled)
	case r.hold && strings.Contains(line, "--full-balance"):
		r.mu.Unlock()
		select {
		case <-r.cancelled:
			return []byte("ERROR: error during balancing '/data': Operation canceled"), errors.New("exit status 1")
		case <-time.After(5 * time.Second):
			return nil, errors.New("the full balance was never cancelled")
		}
	}
	r.mu.Unlock()
	return nil, nil
}

func (r *scriptedBtrfs) Output(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != "btrfs" || !strings.HasPrefix(strings.Join(args, " "), "filesystem show --raw ") {
		return nil, errors.New("unexpected command")
	}
	return []byte(r.shows[0]), nil
}

func (r *scriptedBtrfs) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commands)
}

// threeDisks is `btrfs filesystem show --raw` for a mirror of three 500 GB
// disks allocated as given, in GB.
func threeDisks(uuid string, first, second, third uint64) string {
	const gb = 1_000_000_000
	return fmt.Sprintf("Label: 'ANAS_DATA'  uuid: %s\n\tTotal devices 3 FS bytes used 1\n"+
		"\tdevid    1 size %d used %d path /dev/sdb1\n\tdevid    2 size %d used %d path /dev/sda1\n\tdevid    3 size %d used %d path /dev/sdc1\n\n",
		uuid, 500*gb, first*gb, 500*gb, second*gb, 500*gb, third*gb)
}

func waitIdle(t *testing.T, executor *Executor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		executor.conversionMu.Lock()
		busy := executor.converting
		executor.conversionMu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("still spreading after 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// spreadExecutor is an executor of a mirror whose marker is in a temporary
// mount point, measuring every millisecond while it spreads.
func spreadExecutor(t *testing.T, runner CommandRunner) (*Executor, volumeMarker) {
	t.Helper()
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: t.TempDir()})
	executor.spreadPoll = time.Millisecond
	marker := volumeMarker{FilesystemUUID: uuid, FormatVersion: 1, Layout: datavolume.LayoutMirror}
	if err := executor.writeMarker(marker); err != nil {
		t.Fatal(err)
	}
	return executor, marker
}

const spreadUUID = "09e275fe-794a-458d-8200-b6e67c55cc22"

// A disk that joined two full ones of a mirror stays mostly unused: every
// block group needs room on two disks. The check packs half-empty block
// groups and then writes data block groups anew, which places them on the
// disks with the most space left.
func TestTheMirrorSpreadsUntilItCanUseEveryDisk(t *testing.T) {
	runner := &scriptedBtrfs{shows: []string{
		threeDisks(spreadUUID, 480, 490, 0),
		threeDisks(spreadUUID, 470, 480, 0),
		threeDisks(spreadUUID, 330, 330, 320),
	}}
	executor, mirror := spreadExecutor(t, runner)

	started, err := executor.reconcileSpread(context.Background(), mirror)
	if err != nil || !started {
		t.Fatalf("reconcileSpread() = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	mount := executor.mountPoint
	want := []string{"btrfs balance start -dusage=50 " + mount, "btrfs balance start --full-balance -d " + mount}
	if got := runner.ran(); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if started, err := executor.reconcileSpread(context.Background(), mirror); started || err != nil {
		t.Fatalf("reconcileSpread() on a spread mirror = %v, %v; want nothing to do", started, err)
	}
	if executor.SpreadStopped() {
		t.Fatal("SpreadStopped() = true for a mirror that uses all of its disks")
	}
}

// The full balance would rewrite every data block group; it is cancelled as
// soon as the mirror can use all of its disks.
func TestSpreadingStopsTheBalanceOnceNoSpaceIsUnusable(t *testing.T) {
	runner := &scriptedBtrfs{hold: true, cancelled: make(chan struct{}), shows: []string{
		threeDisks(spreadUUID, 480, 480, 0),
		threeDisks(spreadUUID, 470, 470, 0),
		threeDisks(spreadUUID, 330, 330, 300),
	}}
	executor, mirror := spreadExecutor(t, runner)

	if started, err := executor.reconcileSpread(context.Background(), mirror); !started || err != nil {
		t.Fatalf("reconcileSpread() = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	mount := executor.mountPoint
	want := []string{"btrfs balance start -dusage=50 " + mount, "btrfs balance start --full-balance -d " + mount, "btrfs balance cancel " + mount}
	if got := runner.ran(); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if recorded, err := executor.readMarker(); err != nil || recorded.SpreadStopped != "" || executor.SpreadStopped() {
		t.Fatalf("marker = %+v, %v; want no stop recorded for a spread that reached its goal", recorded, err)
	}
}

// Only a mirror strands space; a span or a stripe uses a disk on its own.
func TestOnlyAMirrorSpreads(t *testing.T) {
	runner := &scriptedBtrfs{shows: []string{threeDisks(spreadUUID, 480, 480, 0)}}
	executor, _ := spreadExecutor(t, runner)
	for _, layout := range []datavolume.Layout{datavolume.LayoutSpan, datavolume.LayoutStripe} {
		if started, err := executor.reconcileSpread(context.Background(), volumeMarker{FilesystemUUID: spreadUUID, FormatVersion: 1, Layout: layout}); started || err != nil {
			t.Fatalf("reconcileSpread(%s) = %v, %v; want nothing to do", layout, started, err)
		}
	}
	if got := runner.ran(); len(got) != 0 {
		t.Fatalf("commands = %q, want none", got)
	}
}

// A spread that leaves space unusable, for lack of room on the old disks,
// is recorded with the disks it ran on, also across a restart: the check
// does not rewrite the volume again for them. It spreads again once the
// disks change, or when the administrator asks.
func TestASpreadThatLeavesSpaceUnusableWaitsForOtherDisks(t *testing.T) {
	runner := &scriptedBtrfs{fail: "balance", shows: []string{threeDisks(spreadUUID, 480, 480, 0)}}
	executor, mirror := spreadExecutor(t, runner)

	if started, err := executor.reconcileSpread(context.Background(), mirror); !started || err != nil {
		t.Fatalf("reconcileSpread() = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	recorded, err := executor.readMarker()
	if err != nil || recorded.SpreadStopped == "" || !executor.SpreadStopped() {
		t.Fatalf("marker = %+v, %v; want the disks recorded and the stop reported", recorded, err)
	}
	if started, err := executor.reconcileSpread(context.Background(), recorded); started || err != nil {
		t.Fatalf("reconcileSpread() on the same disks = %v, %v; want it to wait", started, err)
	}
	restarted := NewExecutor(nil, runner, Options{SystemRoot: executor.systemRoot, MountPoint: executor.mountPoint})
	if started, err := restarted.reconcileSpread(context.Background(), recorded); started || err != nil || !restarted.SpreadStopped() {
		t.Fatalf("reconcileSpread() after a restart = %v, %v (stopped %v); want it to wait", started, err, restarted.SpreadStopped())
	}

	// A disk grew: the recorded stop no longer applies.
	runner.mu.Lock()
	runner.shows = []string{strings.Replace(threeDisks(spreadUUID, 480, 480, 0), "devid    3 size 500000000000", "devid    3 size 900000000000", 1)}
	runner.mu.Unlock()
	if started, err := executor.reconcileSpread(context.Background(), recorded); !started || err != nil {
		t.Fatalf("reconcileSpread() after a disk changed = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)

	// Asking to continue tries again on the same disks; the volume is no
	// Btrfs here, so there is no conversion for it to start itself.
	if err := executor.ContinueConversion(context.Background()); !errors.Is(err, datavolume.ErrNoConversion) {
		t.Fatalf("ContinueConversion() error = %v", err)
	}
	if recorded, err := executor.readMarker(); err != nil || recorded.SpreadStopped != "" || executor.SpreadStopped() {
		t.Fatalf("marker after asking to continue = %+v, %v; want the stop cleared", recorded, err)
	}
}

// Disks are not added while Btrfs runs a balance, or while the Host Agent
// converts or spreads the volume between two balances: btrfs device add
// would fail after the disks were erased. While disks are added, the checks
// start no balance.
func TestDisksAreNotAddedWhileTheVolumeIsBusy(t *testing.T) {
	for _, busy := range []string{"converting", "balance"} {
		t.Run(busy, func(t *testing.T) {
			runner := &commandLog{}
			executor := NewExecutor(mountedVolume{uuid: spreadUUID, disks: []string{"disk:old"}}, runner, Options{SystemRoot: t.TempDir(), MountPoint: t.TempDir()})
			if busy == "converting" {
				executor.converting = true
			} else {
				sysfs := filepath.Join(executor.systemRoot, "sys", "fs", "btrfs", spreadUUID)
				if err := os.MkdirAll(sysfs, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sysfs, "exclusive_operation"), []byte("balance\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := executor.addDisks(context.Background(), &volumeOperation{
				PlanID: "plan:add", Kind: datavolume.PlanKindAddDisks, Layout: datavolume.LayoutMirror, FilesystemUUID: spreadUUID,
				Disks: []datavolume.DiskTarget{{DiskID: "disk:old", Fingerprint: "old"}, {DiskID: "disk:new", Fingerprint: "new"}},
			})
			if !errors.Is(err, datavolume.ErrVolumeBusy) || len(runner.ran()) != 0 {
				t.Fatalf("addDisks() error = %v after %q; want ErrVolumeBusy before any command", err, runner.ran())
			}
		})
	}

	runner := &scriptedBtrfs{shows: []string{threeDisks(spreadUUID, 480, 480, 0)}}
	executor, mirror := spreadExecutor(t, runner)
	if err := executor.beginAdding(spreadUUID); err != nil {
		t.Fatal(err)
	}
	if started, err := executor.reconcileSpread(context.Background(), mirror); started || err != nil {
		t.Fatalf("reconcileSpread() while disks are added = %v, %v; want nothing started", started, err)
	}
	executor.endAdding()
	if started, err := executor.reconcileSpread(context.Background(), mirror); !started || err != nil {
		t.Fatalf("reconcileSpread() after adding = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
}

// commandLog records the commands run and lets each succeed.
type commandLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *commandLog) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

func (c *commandLog) Output(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return c.Run(ctx, name, args, stdin)
}

func (c *commandLog) ran() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines)
}

// mountedVolume resolves the data volume as mounted on the disks.
type mountedVolume struct {
	uuid  string
	disks []string
}

func (m mountedVolume) ResolveDevice(context.Context, string) (BlockDevice, error) {
	return BlockDevice{}, errors.New("no device is resolved here")
}

func (m mountedVolume) MountedDataVolume(context.Context) (*hoststate.DataVolume, error) {
	volume := &hoststate.DataVolume{FilesystemUUID: m.uuid}
	for _, id := range m.disks {
		resource, err := hoststate.NewResourceID(id)
		if err != nil {
			return nil, err
		}
		volume.DiskIDs = append(volume.DiskIDs, resource)
	}
	return volume, nil
}
