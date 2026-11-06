package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// The simulated Host Agent answers a plan that asks again as the real one
// does: the same operation for the same request, ErrPlanState for another
// request or while another plan's operation runs (#194).
func TestSimulatedStorageChecksAPlanThatAsksAgain(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 27, 9, 0, 0, 0, time.UTC)
	simulated := newSimulatedStorage(fake.NewWithSpareDisk())
	simulated.now = func() time.Time { return now }
	disks := []storage.DiskTarget{{DiskID: "disk:a", Fingerprint: "a"}}
	create := storage.CreateVolumeRequest{PlanID: "plan:1", Disks: disks, Layout: storage.LayoutSingle}
	first, err := simulated.StartCreateVolume(ctx, create)
	if err != nil || first.State != storage.OperationRunning {
		t.Fatalf("start = %+v, %v", first, err)
	}
	if again, err := simulated.StartCreateVolume(ctx, create); err != nil || again.PlanID != "plan:1" || again.State != storage.OperationRunning {
		t.Fatalf("the same request again = %+v, %v", again, err)
	}
	other := create
	other.Disks = []storage.DiskTarget{{DiskID: "disk:b", Fingerprint: "b"}}
	if _, err := simulated.StartCreateVolume(ctx, other); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("another request under the same plan = %v, want ErrPlanState", err)
	}
	if _, err := simulated.StartAddDisks(ctx, storage.AddDisksRequest{PlanID: "plan:1", Disks: disks, Layout: storage.LayoutMirror, FilesystemUUID: simulatedUUID}); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("another kind under the same plan = %v, want ErrPlanState", err)
	}
	if _, err := simulated.StartCreateVolume(ctx, storage.CreateVolumeRequest{PlanID: "plan:2", Disks: disks, Layout: storage.LayoutSingle}); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("another plan while the first runs = %v, want ErrPlanState", err)
	}
	now = now.Add(time.Minute)
	if done, err := simulated.VolumeOperation(ctx, "plan:1"); err != nil || done.State != storage.OperationSucceeded {
		t.Fatalf("after its steps = %+v, %v", done, err)
	}
	if again, err := simulated.StartCreateVolume(ctx, create); err != nil || again.State != storage.OperationSucceeded {
		t.Fatalf("the same request after it succeeded = %+v, %v", again, err)
	}
	if _, err := simulated.StartCreateVolume(ctx, storage.CreateVolumeRequest{PlanID: "plan:2", Disks: disks, Layout: storage.LayoutSingle}); !errors.Is(err, storage.ErrVolumeExists) {
		t.Fatalf("a second volume = %v, want ErrVolumeExists", err)
	}
}

// ANAS_FAKE_STORAGE=degraded pulls the last disk out of a new mirror, which
// then runs without it until a new disk replaces it, and restores its
// redundancy for a while after; the rebuild reports how far it has come.
func TestSimulatedStorageReplacesThePulledDiskOfAMirror(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.November, 6, 9, 0, 0, 0, time.UTC)
	simulated, err := developmentStorage(fakeStorageDegraded)
	if err != nil {
		t.Fatal(err)
	}
	simulated.now = func() time.Time { return now }
	pair := []storage.DiskTarget{{DiskID: "disk:fake-data-01", Fingerprint: "a"}, {DiskID: "disk:fake-data-02", Fingerprint: "b"}}
	if _, err := simulated.StartCreateVolume(ctx, storage.CreateVolumeRequest{PlanID: "plan:create", Disks: pair, Layout: storage.LayoutMirror}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if done, err := simulated.VolumeOperation(ctx, "plan:create"); err != nil || done.State != storage.OperationSucceeded {
		t.Fatalf("create = %+v, %v", done, err)
	}
	state, err := simulated.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if volume := state.DataVolume; volume == nil || volume.MissingDevices != 1 || len(volume.DiskIDs) != 1 || volume.DiskIDs[0].String() != "disk:fake-data-01" || volume.Restoring {
		t.Fatalf("data volume after the disk was pulled = %+v, want mounted on disk:fake-data-01 without one disk", volume)
	}
	if hasDisk(state, "disk:fake-data-02") {
		t.Fatal("the pulled disk is still on the host")
	}
	// Btrfs keeps the size of a mirror that runs without a disk.
	if capacity := dataCapacity(state); capacity != 512_000_000_000*99/100 {
		t.Fatalf("capacity = %d, want the mirror's", capacity)
	}

	replace := storage.ReplaceDiskRequest{PlanID: "plan:replace", FilesystemUUID: simulatedUUID, Disk: storage.DiskTarget{DiskID: "disk:fake-data-03", Fingerprint: "c"}}
	if _, err := simulated.StartReplaceDisk(ctx, replace); err != nil {
		t.Fatal(err)
	}
	now = now.Add(simulatedStep + simulatedRebuild/2)
	if running, err := simulated.VolumeOperation(ctx, "plan:replace"); err != nil || running.State != storage.OperationRunning || running.Step != storage.StepReplace || running.Progress != 50 {
		t.Fatalf("halfway through the rebuild = %+v, %v; want the replace step at 50%%", running, err)
	}
	now = now.Add(simulatedRebuild)
	if done, err := simulated.VolumeOperation(ctx, "plan:replace"); err != nil || done.State != storage.OperationSucceeded {
		t.Fatalf("replace = %+v, %v", done, err)
	}
	if state, err = simulated.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if volume := state.DataVolume; volume == nil || volume.MissingDevices != 0 || len(volume.DiskIDs) != 2 || !volume.Restoring {
		t.Fatalf("data volume after the replace = %+v, want both disks and restoring", volume)
	}
	if hasDisk(state, "disk:fake-data-02") {
		t.Fatal("the pulled disk came back after it was replaced")
	}
	now = now.Add(simulatedRestore)
	if state, err = simulated.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if state.DataVolume == nil || state.DataVolume.Restoring {
		t.Fatalf("data volume once restored = %+v", state.DataVolume)
	}
	if _, err := simulated.StartReplaceDisk(ctx, storage.ReplaceDiskRequest{PlanID: "plan:again", FilesystemUUID: simulatedUUID, Disk: storage.DiskTarget{DiskID: "disk:fake-data-04"}}); !errors.Is(err, storage.ErrVolumeChanged) {
		t.Fatalf("replacing a disk of a whole mirror = %v, want ErrVolumeChanged", err)
	}
}

// A span that loses a disk is not mounted; its other disk only carries the
// volume's signature.
func TestSimulatedStorageTakesASpanOfflineWithoutADisk(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.November, 6, 9, 0, 0, 0, time.UTC)
	simulated, err := developmentStorage(fakeStorageDegraded)
	if err != nil {
		t.Fatal(err)
	}
	simulated.now = func() time.Time { return now }
	pair := []storage.DiskTarget{{DiskID: "disk:fake-data-01", Fingerprint: "a"}, {DiskID: "disk:fake-data-02", Fingerprint: "b"}}
	if _, err := simulated.StartCreateVolume(ctx, storage.CreateVolumeRequest{PlanID: "plan:create", Disks: pair, Layout: storage.LayoutSpan}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := simulated.VolumeOperation(ctx, "plan:create"); err != nil {
		t.Fatal(err)
	}
	state, err := simulated.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.DataVolume != nil || hasDisk(state, "disk:fake-data-02") {
		t.Fatalf("span without a disk = %+v, want it offline and the disk gone", state.DataVolume)
	}
	for _, disk := range state.Disks {
		if disk.ID.String() == "disk:fake-data-01" && (disk.Role != hoststate.DiskRoleUnassigned || len(disk.Signatures) != 1 || disk.Signatures[0].UUID != simulatedUUID) {
			t.Fatalf("the span's other disk = %+v, want it unmounted with the volume's signature", disk)
		}
	}
}

// Fake mode runs on the default host unless ANAS_FAKE_STORAGE names another,
// and refuses one it does not know.
func TestDevelopmentStorageNamesItsHost(t *testing.T) {
	simulated, err := developmentStorage("")
	if err != nil || simulated.pullDisk {
		t.Fatalf("default = %+v, %v", simulated, err)
	}
	state, err := simulated.Read(context.Background())
	if err != nil || len(state.Disks) != 3 {
		t.Fatalf("default host = %d disks, %v; want the system disk, a data disk and a spare", len(state.Disks), err)
	}
	if _, err := developmentStorage("broken"); err == nil {
		t.Fatal("an unknown host was accepted")
	}
}

func hasDisk(state hoststate.State, id string) bool {
	for _, disk := range state.Disks {
		if disk.ID.String() == id {
			return true
		}
	}
	return false
}

func dataCapacity(state hoststate.State) uint64 {
	for _, disk := range state.Disks {
		if disk.FilesystemCapacityBytes != nil {
			return *disk.FilesystemCapacityBytes
		}
	}
	return 0
}

// Fake mode keeps the task manager: the simulated storage passes its reads
// on to the Fake Adapter, temperatures included.
func TestSimulatedStorageServesTheTaskManager(t *testing.T) {
	simulated := newSimulatedStorage(fake.NewWithSpareDisk())
	device, err := simulated.ReadDeviceStatus(context.Background())
	if err != nil || !device.Valid() || device.CPU.Temperature == nil {
		t.Fatalf("ReadDeviceStatus() = %+v, %v; want a valid snapshot with the CPU temperature", device, err)
	}
	if tasks, err := simulated.ReadTasks(context.Background()); err != nil || !tasks.Valid() {
		t.Fatalf("ReadTasks() = %+v, %v", tasks, err)
	}
}
