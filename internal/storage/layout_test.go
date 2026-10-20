package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

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
	service := storage.NewService(fake.NewWithSpareDisk(), executor, storage.Options{NewID: func() string { return "plan-two" }})

	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-01"}, storage.LayoutMirror); !errors.Is(err, storage.ErrInvalidLayout) {
		t.Fatalf("mirror on one disk error = %v, want ErrInvalidLayout", err)
	}
	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-01", "disk:fake-data-02"}, storage.LayoutSingle); !errors.Is(err, storage.ErrInvalidLayout) {
		t.Fatalf("single layout on two disks error = %v, want ErrInvalidLayout", err)
	}
	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-01", "disk:fake-data-01"}, ""); !errors.Is(err, storage.ErrDiskNotFound) {
		t.Fatalf("repeated disk error = %v, want ErrDiskNotFound", err)
	}

	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-01", "disk:fake-data-02"}, "")
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
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, plan.ID); err != nil {
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
	clock := time.Date(2026, time.October, 20, 9, 0, 0, 0, time.UTC)
	ids := 0
	service := storage.NewService(reader, executor, storage.Options{
		Now:   func() time.Time { clock = clock.Add(time.Second); return clock },
		NewID: func() string { ids++; return "plan-" + string(rune('0'+ids)) },
	})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 120_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileSingle, UsedBytes: 118_000_000_000}},
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileDup, UsedBytes: 2_000_000_000}})

	volumes, err := service.ListVolumes(ctx)
	if err != nil || len(volumes) != 1 || volumes[0].Layout != storage.LayoutSingle || volumes[0].Conversion != nil {
		t.Fatalf("volumes = %+v, %v; want a single disk with nothing to convert", volumes, err)
	}

	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-02"}, storage.LayoutMirror)
	if err != nil {
		t.Fatalf("PlanVolume() error = %v", err)
	}
	if plan.Kind != storage.PlanKindAddDisks || plan.CapacityBeforeBytes != 512_000_000_000 || plan.CapacityAfterBytes != 512_000_000_000 {
		t.Fatalf("plan = %+v, want adding to a 512 GB mirror", plan)
	}
	if !slices.ContainsFunc(plan.Actions, func(action storage.Action) bool { return action.Kind == "convert_layout" }) {
		t.Fatalf("actions = %+v, want the conversion", plan.Actions)
	}
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	executed, err := service.ExecutePlan(ctx, plan.ID)
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
	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-03"}, storage.LayoutMirror); !errors.Is(err, storage.ErrVolumeBusy) {
		t.Fatalf("plan during a conversion error = %v, want ErrVolumeBusy", err)
	}
	if err := service.ContinueConversion(ctx); !errors.Is(err, storage.ErrNoConversion) || executor.continued != 0 {
		t.Fatalf("continue a running conversion = %v (%d calls), want ErrNoConversion", err, executor.continued)
	}

	// A shutdown cancelled the balance; the conversion waits for the Host Agent.
	reader.state.DataVolume.Operation = hoststate.VolumeOperationNone
	volumes, _ = service.ListVolumes(ctx)
	if volumes[0].Conversion == nil || volumes[0].Conversion.State != storage.ConversionWaiting {
		t.Fatalf("conversion = %+v, want waiting", volumes[0].Conversion)
	}
	if err := service.ContinueConversion(ctx); err != nil || executor.continued != 1 {
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
	service := storage.NewService(reader, &recordingExecutor{}, storage.Options{})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 600_000_000_000,
		[]hoststate.ProfileUsage{{Profile: hoststate.ProfileSingle, UsedBytes: 600_000_000_000}}, nil)

	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-02"}, storage.LayoutMirror); !errors.Is(err, storage.ErrLayoutTooSmall) {
		t.Fatalf("mirror of 512 GB under 600 GB of files error = %v, want ErrLayoutTooSmall", err)
	}
	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-02"}, storage.LayoutSpan)
	if err != nil || plan.CapacityAfterBytes != 1_512_000_000_000 {
		t.Fatalf("span plan = %+v, %v; want 1.5 TB", plan, err)
	}
}

func TestAnOfflineMemberOfTheVolumeIsNeverErased(t *testing.T) {
	ctx := context.Background()
	reader := spareDiskReader(t)
	service := storage.NewService(reader, &recordingExecutor{}, storage.Options{})
	createSingleVolume(t, service)
	mountVolume(reader, []string{"disk:fake-data-01"}, 1, nil, nil)
	for index := range reader.state.Disks {
		if reader.state.Disks[index].ID.String() == "disk:fake-data-02" {
			reader.state.Disks[index].Signatures = []hoststate.Signature{{Type: "btrfs", Label: "ANAS_DATA", UUID: volumeUUID}}
		}
	}
	if _, err := service.PlanVolume(ctx, []string{"disk:fake-data-02"}, storage.LayoutMirror); !errors.Is(err, storage.ErrDiskNotEligible) {
		t.Fatalf("plan for a disk of the volume error = %v, want ErrDiskNotEligible", err)
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
	plan, err := service.PlanVolume(ctx, []string{"disk:fake-data-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmPlan(ctx, plan.ID, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecutePlan(ctx, plan.ID); err != nil {
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
