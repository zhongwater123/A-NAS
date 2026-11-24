package files_test

import (
	"context"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/files"
)

// A deleted account's personal space leaves the catalog
// (docs/specs/account-enable-and-delete.md): its snapshots are deleted while
// the snapshot backend still knows the space, and its saved schedule goes.
// Other spaces keep theirs.
func TestRemovingADeletedAccountDeletesItsSnapshotsAndSchedule(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	f.upload(f.member, f.memberPrivate, "", "notes.txt", "alice")
	f.upload(f.admin, f.private, "", "keep.txt", "owner")
	f.setSchedule(f.member, f.memberPrivate, files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 7})
	if _, err := f.files.CreateSnapshot(ctx, f.member, f.memberPrivate.ID, "她的"); err != nil {
		t.Fatalf("CreateSnapshot(member) error = %v", err)
	}
	if _, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "管理员的"); err != nil {
		t.Fatalf("CreateSnapshot(admin) error = %v", err)
	}

	if err := f.files.RemoveAccount(ctx, f.member.ID, f.memberPrivate.ID); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	if snapshots, err := f.files.ListSnapshots(ctx, f.member, f.memberPrivate.ID); err != nil || len(snapshots) != 0 {
		t.Fatalf("the removed space's snapshots = %+v, %v", snapshots, err)
	}
	// Its saved schedule is gone; what is shown now is the default a space
	// without one gets. RunDueSnapshots records that default only for active
	// accounts, and a deleted account is disabled.
	if schedule := f.schedule(f.member, f.memberPrivate); schedule.Keep == 7 {
		t.Fatalf("the removed space keeps its schedule: %+v", schedule)
	}
	if kept := f.snapshots(f.private); len(kept) != 1 {
		t.Fatalf("the administrator's snapshots = %+v, want theirs kept", kept)
	}
	if err := f.files.RemoveAccount(ctx, f.member.ID, f.memberPrivate.ID); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
	if err := f.files.RemoveAccount(ctx, f.member.ID, f.shared.ID); err == nil {
		t.Fatal("the Shared space was removed with an account")
	}
}
