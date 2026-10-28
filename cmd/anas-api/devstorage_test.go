package main

import (
	"context"
	"errors"
	"testing"
	"time"

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
