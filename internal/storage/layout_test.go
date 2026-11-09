package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

const volumeUUID = "0f6e1c2a-data"

func TestUsableBytesFollowsTheLayout(t *testing.T) {
	for _, test := range []struct {
		layout storage.Layout
		sizes  []uint64
		want   uint64
	}{
		{storage.LayoutSingle, []uint64{500}, 500},
		{storage.LayoutMirror, []uint64{500, 500}, 500},
		{storage.LayoutMirror, []uint64{1000, 500}, 500},
		{storage.LayoutMirror, []uint64{1000, 500, 500}, 1000},
		{storage.LayoutSpan, []uint64{1000, 500}, 1500},
		{storage.LayoutStripe, []uint64{500, 500}, 1000},
	} {
		if got := storage.UsableBytes(test.layout, test.sizes); got != test.want {
			t.Errorf("UsableBytes(%s, %v) = %d, want %d", test.layout, test.sizes, got, test.want)
		}
	}
}

func TestTwoDisksBecomeAMirrorUnlessAnotherLayoutIsChosen(t *testing.T) {
	ctx := context.Background()
	executor := &recordingExecutor{}
	service := newService(t, fake.NewWithSpareDisk(), executor, storage.Options{NewID: func() string { return "plan-two" }})

	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01"}, storage.LayoutMirror); !errors.Is(err, storage.ErrInvalidLayout) {
		t.Fatalf("mirror on one disk error = %v, want ErrInvalidLayout", err)
	}
	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01", "disk:fake-data-02"}, storage.LayoutSingle); !errors.Is(err, storage.ErrInvalidLayout) {
		t.Fatalf("single layout on two disks error = %v, want ErrInvalidLayout", err)
	}
	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01", "disk:fake-data-01"}, ""); !errors.Is(err, storage.ErrDiskNotFound) {
		t.Fatalf("repeated disk error = %v, want ErrDiskNotFound", err)
	}

	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01", "disk:fake-data-02"}, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if plan.Kind != storage.PlanKindCreateVolume || plan.Layout != storage.LayoutMirror || plan.CapacityAfterBytes != 512_000_000_000 {
		t.Fatalf("plan = %+v, want a 512 GB mirror", plan)
	}
	if got, want := plan.ConfirmationPhrase, "ERASE data-01 data-02"; got != want {
		t.Fatalf("confirmation phrase = %q, want %q", got, want)
	}
	// The md signature another NAS left is listed for erasing, not a reason
	// to refuse the disk.
	if got, want := plan.Disks[1].Signatures, []storage.Signature{{Type: "linux_raid_member", Label: "OrangePi3B:0"}}; !slices.Equal(got, want) {
		t.Fatalf("signatures = %#v, want %#v", got, want)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID); err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if got := executor.created; got.Layout != storage.LayoutMirror || len(got.Disks) != 2 || got.Disks[1].DiskID != "disk:fake-data-02" || got.Disks[1].Fingerprint == "" {
		t.Fatalf("create request = %+v, want both disks as a mirror", got)
	}
}

func TestAddingADiskConvertsTheVolumeAndReportsTheProgress(t *testing.T) {
	ctx := context.Background()
	reader := spareDiskReader(t)
	executor := &recordingExecutor{}
	service := newService(t, reader, executor, storage.Options{
		Now:   clocktest.Ticking(time.Date(2026, time.October, 20, 9, 0, 0, 0, time.UTC), time.Second).Now,
		NewID: planIDs(),
	})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 120_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileSingle, UsedBytes: 118_000_000_000}},
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileDup, UsedBytes: 2_000_000_000}})

	volumes, err := service.ListVolumes(ctx)
	if err != nil || len(volumes) != 1 || volumes[0].Layout != storage.LayoutSingle || volumes[0].Conversion != nil {
		t.Fatalf("volumes = %+v, %v; want a single disk with nothing to convert", volumes, err)
	}

	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-02"}, storage.LayoutMirror)
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if plan.Kind != storage.PlanKindAddDisks || plan.CapacityBeforeBytes != 512_000_000_000 || plan.CapacityAfterBytes != 512_000_000_000 {
		t.Fatalf("plan = %+v, want adding to a 512 GB mirror", plan)
	}
	if !slices.ContainsFunc(plan.Actions, func(action storage.Action) bool { return action.Kind == "convert_layout" }) {
		t.Fatalf("actions = %+v, want the conversion", plan.Actions)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	executed, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID)
	if err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if len(executor.added) != 1 || executor.added[0].FilesystemUUID != volumeUUID || executor.added[0].Layout != storage.LayoutMirror {
		t.Fatalf("add requests = %+v", executor.added)
	}
	if got := executed.Volume; got == nil || got.ID != "volume:data" || !slices.Equal(got.DiskIDs, []string{"disk:fake-data-01", "disk:fake-data-02"}) {
		t.Fatalf("executed volume = %+v", got)
	}

	// Btrfs has converted 40 of 120 GB so far.
	mountVolume(reader, []string{"disk:fake-data-01", "disk:fake-data-02"}, 120_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 39_000_000_000}, {Profile: hoststate.ProfileSingle, UsedBytes: 79_000_000_000}},
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 1_000_000_000}, {Profile: hoststate.ProfileDup, UsedBytes: 1_000_000_000}})
	reader.state.DataVolume.Operation = hoststate.VolumeOperationBalance
	volumes, err = service.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conversion := volumes[0].Conversion
	if volumes[0].Layout != storage.LayoutMirror || conversion == nil || conversion.State != storage.ConversionRunning || conversion.ConvertedBytes != 40_000_000_000 || conversion.TotalBytes != 120_000_000_000 {
		t.Fatalf("volume = %+v, conversion = %+v; want a running mirror conversion at 40 of 120 GB", volumes[0], conversion)
	}
	if !slices.Equal(volumes[0].DiskIDs, []string{"disk:fake-data-01", "disk:fake-data-02"}) {
		t.Fatalf("disks = %v, want both", volumes[0].DiskIDs)
	}
	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-03"}, storage.LayoutMirror); !errors.Is(err, storage.ErrVolumeBusy) {
		t.Fatalf("plan during a conversion error = %v, want ErrVolumeBusy", err)
	}
	if err := service.ContinueConversion(ctx, storageAdministrator); !errors.Is(err, storage.ErrNoConversion) || executor.continued != 0 {
		t.Fatalf("continue a running conversion = %v (%d calls), want ErrNoConversion", err, executor.continued)
	}

	// A shutdown cancelled the balance; the conversion waits for the Host Agent.
	reader.state.DataVolume.Operation = hoststate.VolumeOperationNone
	volumes, _ = service.ListVolumes(ctx)
	if volumes[0].Conversion == nil || volumes[0].Conversion.State != storage.ConversionWaiting {
		t.Fatalf("conversion = %+v, want waiting", volumes[0].Conversion)
	}
	if err := service.ContinueConversion(ctx, storageAdministrator); err != nil || executor.continued != 1 {
		t.Fatalf("continue a stopped conversion = %v (%d calls)", err, executor.continued)
	}

	mountVolume(reader, []string{"disk:fake-data-01", "disk:fake-data-02"}, 120_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 118_000_000_000}, {Profile: hoststate.ProfileSingle, UsedBytes: 0}},
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 2_000_000_000}})
	volumes, _ = service.ListVolumes(ctx)
	if volumes[0].Conversion != nil {
		t.Fatalf("conversion = %+v after Btrfs finished, want none", volumes[0].Conversion)
	}
}

func TestAMirrorMustHoldTheFilesAlreadyThere(t *testing.T) {
	ctx := context.Background()
	reader := spareDiskReader(t)
	reader.state.Disks[0].CapacityBytes = 1_000_000_000_000
	service := newService(t, reader, &recordingExecutor{}, storage.Options{})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 600_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileSingle, UsedBytes: 600_000_000_000}}, nil)

	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-02"}, storage.LayoutMirror); !errors.Is(err, storage.ErrLayoutTooSmall) {
		t.Fatalf("mirror of 512 GB under 600 GB of files error = %v, want ErrLayoutTooSmall", err)
	}
	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-02"}, storage.LayoutSpan)
	if err != nil || plan.CapacityAfterBytes != 1_512_000_000_000 {
		t.Fatalf("span plan = %+v, %v; want 1.5 TB", plan, err)
	}
}

func TestAnOfflineMemberOfTheVolumeIsNeverErased(t *testing.T) {
	ctx := context.Background()
	reader := spareDiskReader(t)
	service := newService(t, reader, &recordingExecutor{}, storage.Options{})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 1, nil, nil)
	for index := range reader.state.Disks {
		if reader.state.Disks[index].ID.String() == "disk:fake-data-02" {
			reader.state.Disks[index].Signatures = []hoststate.Signature{{Type: "btrfs", Label: "ANAS_DATA", UUID: volumeUUID}}
		}
	}
	if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-02"}, storage.LayoutMirror); !errors.Is(err, storage.ErrDiskNotEligible) {
		t.Fatalf("plan for a disk of the volume error = %v, want ErrDiskNotEligible", err)
	}
}

// A plan that has not run can be given up, or replaced by a new one, so the
// administrator is not held in the wizard, or kept from planning again after
// a page reload forgot it, until it expires.
func TestAPlanThatHasNotRunCanBeCancelled(t *testing.T) {
	ctx := context.Background()
	executor := &recordingExecutor{}
	service := newService(t, fake.NewWithSpareDisk(), executor, storage.Options{NewID: planIDs()})
	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-02"}, "")
	if err != nil {
		t.Fatalf("plan while another is pending = %v, want it to take that one's place", err)
	}
	if replaced, _ := service.GetPlan(plan.ID); replaced.State != storage.PlanStateCancelled {
		t.Fatalf("replaced plan = %q, want cancelled", replaced.State)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("confirm a replaced plan = %v, want ErrPlanState", err)
	}
	plan = replacement
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.CancelPlan(ctx, storageAdministrator, plan.ID)
	if err != nil || cancelled.State != storage.PlanStateCancelled {
		t.Fatalf("CancelPlan() = %+v, %v; want cancelled", cancelled, err)
	}
	if again, err := service.CancelPlan(ctx, storageAdministrator, plan.ID); err != nil || again.State != storage.PlanStateCancelled {
		t.Fatalf("cancel twice = %+v, %v; want the cancelled plan again", again, err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID); !errors.Is(err, storage.ErrPlanState) || executor.created.PlanID != "" {
		t.Fatalf("execute a cancelled plan = %v, executor got %+v; want refused before any disk", err, executor.created)
	}
	next, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatalf("plan after cancelling = %v, want allowed at once", err)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, next.ID, next.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelPlan(ctx, storageAdministrator, next.ID); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("cancel a plan that ran = %v, want ErrPlanState", err)
	}
	if _, err := service.CancelPlan(ctx, storageAdministrator, "plan:missing"); !errors.Is(err, storage.ErrPlanNotFound) {
		t.Fatalf("cancel a missing plan = %v, want ErrPlanNotFound", err)
	}
}

// Plans stored before volumes could span disks named one disk at the top level.
func TestPlansStoredForOneDiskStillRead(t *testing.T) {
	var plan storage.ExecutionPlan
	legacy := `{"id":"plan:old","diskId":"disk:old","diskModel":"ST500DM002-1BD142","capacityBytes":500107862016,
	  "fingerprint":"abc","signatures":["linux_raid_member"],"confirmationPhrase":"ERASE isk:old","actions":[],
	  "state":"succeeded","createdAt":"2026-10-07T09:00:00Z","expiresAt":"2026-10-07T09:10:00Z",
	  "volume":{"id":"volume:data","diskId":"disk:old","filesystemUuid":"uuid-old","state":"available"}}`
	if err := json.Unmarshal([]byte(legacy), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Kind != storage.PlanKindCreateVolume || plan.Layout != storage.LayoutSingle || len(plan.Disks) != 1 {
		t.Fatalf("plan = %+v, want a single-disk creation", plan)
	}
	if disk := plan.Disks[0]; disk.ID != "disk:old" || disk.Fingerprint != "abc" || disk.CapacityBytes != 500107862016 || disk.Signatures[0].Type != "linux_raid_member" {
		t.Fatalf("disk = %+v", disk)
	}
	if plan.Volume == nil || !slices.Equal(plan.Volume.DiskIDs, []string{"disk:old"}) || plan.Volume.Layout != storage.LayoutSingle {
		t.Fatalf("volume = %+v", plan.Volume)
	}
}

func spareDiskReader(t *testing.T) *stateReader {
	t.Helper()
	state, err := fake.NewWithSpareDisk().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	third := state.Disks[0]
	third.ID, _ = hoststate.NewResourceID("disk:fake-data-03")
	third.Signatures, third.Filesystems = nil, nil
	state.Disks = append(state.Disks, third)
	return &stateReader{state: state}
}

func createSingleVolume(t *testing.T, service *storage.Service) {
	t.Helper()
	ctx := context.Background()
	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID); err != nil {
		t.Fatal(err)
	}
}

// mountVolume shows the disks as the mounted data volume with used bytes of
// files in the profiles given.
func mountVolume(reader *stateReader, diskIDs []string, used uint64, data, metadata []hoststate.ProfileUsage) {
	volume := &hoststate.DataVolume{FilesystemUUID: volumeUUID, Data: data, Metadata: metadata}
	first := true
	for index := range reader.state.Disks {
		disk := &reader.state.Disks[index]
		disk.FilesystemCapacityBytes, disk.FilesystemAvailableBytes = nil, nil
		if !slices.Contains(diskIDs, disk.ID.String()) {
			continue
		}
		disk.Role = hoststate.DiskRoleData
		volume.DiskIDs = append(volume.DiskIDs, disk.ID)
		if first {
			total := disk.CapacityBytes
			available := total - min(total, used)
			disk.FilesystemCapacityBytes, disk.FilesystemAvailableBytes = &total, &available
			first = false
		}
	}
	reader.state.DataVolume = volume
}

// joinedMirror is a service with a mirror of disks 01 and 02, 500 GB each,
// mounted with the allocation given in GB, and disk 03 spare.
func joinedMirror(t *testing.T, allocated uint64) (*storage.Service, *stateReader, *recordingExecutor) {
	t.Helper()
	reader := spareDiskReader(t)
	for index := range reader.state.Disks {
		if strings.HasPrefix(reader.state.Disks[index].ID.String(), "disk:fake-data-") {
			reader.state.Disks[index].CapacityBytes = 500 * gb
		}
	}
	executor := &recordingExecutor{}
	service := newService(t, reader, executor, storage.Options{NewID: planIDs()})
	ctx := context.Background()
	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-01", "disk:fake-data-02"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID); err != nil {
		t.Fatal(err)
	}
	raid1 := []hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: allocated * gb}}
	mountVolume(reader, []string{"disk:fake-data-01", "disk:fake-data-02"}, allocated*gb, raid1, raid1)
	reader.state.DataVolume.Devices = []hoststate.VolumeDevice{
		{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: allocated * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: allocated * gb},
	}
	return service, reader, executor
}

const gb = uint64(1_000_000_000)

// Disks join a volume on several disks in its layout: changing a mirror into
// a span or a stripe would give up the redundancy, a step of its own.
func TestDisksJoinAMirrorAsAMirror(t *testing.T) {
	ctx := context.Background()
	service, _, _ := joinedMirror(t, 100)

	for _, layout := range []storage.Layout{storage.LayoutSpan, storage.LayoutStripe} {
		if _, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-03"}, layout); !errors.Is(err, storage.ErrLayoutChange) {
			t.Fatalf("adding a disk as %s error = %v, want ErrLayoutChange", layout, err)
		}
	}
	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-03"}, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if plan.Layout != storage.LayoutMirror || plan.CapacityBeforeBytes != 500*gb || plan.CapacityAfterBytes != 750*gb {
		t.Fatalf("plan = %+v, want a mirror growing from 500 to 750 GB", plan)
	}
	// The disks with room pair with the new one: nothing has to move.
	if kinds := actionKinds(plan); !slices.Equal(kinds, []string{"erase_signatures", "create_partition_table", "add_devices"}) {
		t.Fatalf("actions = %q, want the disk added and nothing converted or moved", kinds)
	}
	if again, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-03"}, storage.LayoutMirror); err != nil || again.Layout != storage.LayoutMirror {
		t.Fatalf("PlanVolume(mirror) = %+v, %v", again, err)
	}
}

// A disk joining two full disks of a mirror stays mostly unused until block
// groups move off them; the plan says so, and the volume shows the space
// that cannot be used yet while the Host Agent moves them.
func TestAMirrorShowsTheSpaceItCannotUseYet(t *testing.T) {
	ctx := context.Background()
	service, reader, executor := joinedMirror(t, 480)

	plan, err := service.PlanVolume(ctx, storageAdministrator, []string{"disk:fake-data-03"}, "")
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if kinds := actionKinds(plan); !slices.Equal(kinds, []string{"erase_signatures", "create_partition_table", "add_devices", "rebalance"}) {
		t.Fatalf("actions = %q, want the disk added and block groups moved", kinds)
	}
	if _, err := service.ConfirmPlan(ctx, storageAdministrator, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, storageAdministrator, plan.ID); err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if len(executor.added) != 1 || executor.added[0].Layout != storage.LayoutMirror {
		t.Fatalf("add requests = %+v, want the mirror kept", executor.added)
	}

	raid1 := []hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 480 * gb}}
	mountVolume(reader, []string{"disk:fake-data-01", "disk:fake-data-02", "disk:fake-data-03"}, 480*gb, raid1, raid1)
	reader.state.DataVolume.Devices = []hoststate.VolumeDevice{
		{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 480 * gb}, {DevID: 3, SizeBytes: 500 * gb},
	}
	reader.state.DataVolume.Operation = hoststate.VolumeOperationBalance
	volumes, err := service.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := volumes[0]; got.Conversion != nil || got.Rebalance == nil || got.Rebalance.State != storage.ConversionRunning || got.Rebalance.UnusableBytes != 230*gb {
		t.Fatalf("volume = %+v, rebalance = %+v; want 230 GB unusable while block groups move", got, got.Rebalance)
	}
	if err := service.ContinueConversion(ctx, storageAdministrator); !errors.Is(err, storage.ErrNoConversion) || executor.continued != 0 {
		t.Fatalf("continue while moving = %v (%d calls), want ErrNoConversion", err, executor.continued)
	}

	// A shutdown stopped the balance: the Host Agent moves the rest at its
	// next check, or now when asked.
	reader.state.DataVolume.Operation = hoststate.VolumeOperationNone
	volumes, _ = service.ListVolumes(ctx)
	if volumes[0].Rebalance == nil || volumes[0].Rebalance.State != storage.ConversionWaiting {
		t.Fatalf("rebalance = %+v, want waiting", volumes[0].Rebalance)
	}
	if err := service.ContinueConversion(ctx, storageAdministrator); err != nil || executor.continued != 1 {
		t.Fatalf("continue a stopped move = %v (%d calls)", err, executor.continued)
	}

	reader.state.DataVolume.Devices = []hoststate.VolumeDevice{
		{DevID: 1, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb}, {DevID: 2, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb}, {DevID: 3, SizeBytes: 500 * gb, AllocatedBytes: 320 * gb},
	}
	volumes, _ = service.ListVolumes(ctx)
	if volumes[0].Rebalance != nil {
		t.Fatalf("rebalance = %+v once spread, want none", volumes[0].Rebalance)
	}
}

func actionKinds(plan storage.ExecutionPlan) []string {
	kinds := make([]string, len(plan.Actions))
	for i, action := range plan.Actions {
		kinds[i] = action.Kind
	}
	return kinds
}
