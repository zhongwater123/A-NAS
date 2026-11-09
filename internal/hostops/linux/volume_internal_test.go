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
// another, the last one for good, and records the balances run. A balance
// that fails changes nothing.
type scriptedBtrfs struct {
	mu       sync.Mutex
	shows    []string
	balances []string
	// fail makes balances whose arguments contain it fail.
	fail string
}

func (r *scriptedBtrfs) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	r.balances = append(r.balances, line)
	if r.fail != "" && strings.Contains(line, r.fail) {
		return []byte("ERROR: error during balancing '/data': No space left on device"), errors.New("exit status 1")
	}
	if len(r.shows) > 1 {
		r.shows = r.shows[1:]
	}
	return nil, nil
}

func (r *scriptedBtrfs) Output(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != "btrfs" || strings.Join(args, " ") != "filesystem show --raw /data" {
		return nil, errors.New("unexpected command")
	}
	return []byte(r.shows[0]), nil
}

func (r *scriptedBtrfs) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.balances)
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
			t.Fatal("still moving block groups after 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// spread is the balances that spread a mirror over all of its disks.
var spread = []string{"btrfs balance start -dusage=50 /data", "btrfs balance start --full-balance -d /data"}

// A disk that joined two full ones of a mirror stays mostly unused: every
// block group needs room on two disks. The check packs half-empty block
// groups and then writes every data block group anew, which places them on
// the disks with the most space left.
func TestTheMirrorSpreadsUntilItCanUseEveryDisk(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	runner := &scriptedBtrfs{shows: []string{
		threeDisks(uuid, 480, 490, 0),
		threeDisks(uuid, 470, 480, 0),
		threeDisks(uuid, 330, 330, 320),
	}}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: "/data"})
	mirror := volumeMarker{FilesystemUUID: uuid, FormatVersion: 1, Layout: datavolume.LayoutMirror}

	started, err := executor.reconcileRebalance(context.Background(), mirror)
	if err != nil || !started {
		t.Fatalf("reconcileRebalance() = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	if got := runner.ran(); !slices.Equal(got, spread) {
		t.Fatalf("balances = %q, want %q", got, spread)
	}
	if started, err := executor.reconcileRebalance(context.Background(), mirror); started || err != nil {
		t.Fatalf("reconcileRebalance() on a spread mirror = %v, %v; want nothing to do", started, err)
	}
}

// Only a mirror strands space; a span or a stripe uses a disk on its own.
func TestOnlyAMirrorSpreads(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	runner := &scriptedBtrfs{shows: []string{threeDisks(uuid, 480, 480, 0)}}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: "/data"})
	for _, layout := range []datavolume.Layout{datavolume.LayoutSpan, datavolume.LayoutStripe} {
		if started, err := executor.reconcileRebalance(context.Background(), volumeMarker{FilesystemUUID: uuid, FormatVersion: 1, Layout: layout}); started || err != nil {
			t.Fatalf("reconcileRebalance(%s) = %v, %v; want nothing to do", layout, started, err)
		}
	}
	if got := runner.ran(); len(got) != 0 {
		t.Fatalf("balances = %q, want none", got)
	}
}

// Where the balances leave space unusable, for lack of room or because they
// failed, the check does not rewrite the volume every 15 minutes: it waits
// until the allocation changes, or until the administrator asks to continue.
func TestSpreadingThatLeavesSpaceUnusableWaitsForTheAllocationToChange(t *testing.T) {
	const uuid = "09e275fe-794a-458d-8200-b6e67c55cc22"
	runner := &scriptedBtrfs{fail: "balance", shows: []string{threeDisks(uuid, 480, 480, 0)}}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: "/data"})
	mirror := volumeMarker{FilesystemUUID: uuid, FormatVersion: 1, Layout: datavolume.LayoutMirror}

	if started, err := executor.reconcileRebalance(context.Background(), mirror); !started || err != nil {
		t.Fatalf("reconcileRebalance() = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	if got := runner.ran(); !slices.Equal(got, spread) {
		t.Fatalf("balances = %q, want %q", got, spread)
	}
	if started, err := executor.reconcileRebalance(context.Background(), mirror); started || err != nil {
		t.Fatalf("reconcileRebalance() after the balances left space unusable = %v, %v; want it to wait", started, err)
	}
	// Asking to continue tries again; the volume is no Btrfs here, so there is
	// no conversion for it to start itself.
	if err := executor.ContinueConversion(context.Background()); !errors.Is(err, datavolume.ErrNoConversion) {
		t.Fatalf("ContinueConversion() error = %v", err)
	}
	if started, err := executor.reconcileRebalance(context.Background(), mirror); !started || err != nil {
		t.Fatalf("reconcileRebalance() after asking to continue = %v, %v; want spreading started", started, err)
	}
	waitIdle(t, executor)
	if got := runner.ran(); len(got) != 4 {
		t.Fatalf("balances = %q, want the two balances twice", got)
	}
}
