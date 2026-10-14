package files_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/files"
)

func scheduledNames(snapshots []files.Snapshot) []string {
	return origins(snapshots)[files.SnapshotOriginScheduled]
}

func TestTieredRetentionKeepsTheNewestOfEachPeriod(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	f.upload(f.admin, f.private, "", "notes.txt", "hello")
	f.setSchedule(f.admin, f.private, files.SnapshotScheduleSettings{
		Enabled: true, Frequency: files.FrequencyHourly, Time: "00:00", Keep: 24,
		Retention: files.RetentionTiered, Tiers: files.RetentionTiers{Hourly: 3, Daily: 2},
	})
	// Hourly from 11:00 on Monday to 05:00 on Tuesday.
	for hour := 11; hour <= 29; hour++ {
		f.clock.Set(at(0, hour, 0))
		f.runDue()
	}
	want := []string{"定时快照 2026-10-13 05:00", "定时快照 2026-10-13 04:00", "定时快照 2026-10-13 03:00", "定时快照 2026-10-12 23:00"}
	if got := scheduledNames(f.snapshots(f.private)); !slices.Equal(got, want) {
		t.Fatalf("hourly tiers kept %v, want %v", got, want)
	}
	if schedule := f.schedule(f.admin, f.private); schedule.Retention != files.RetentionTiered || schedule.Tiers.Hourly != 3 || schedule.Tiers.Daily != 2 {
		t.Fatalf("schedule = %+v", schedule)
	}

	// Daily runs keep the newest of each day, ISO week and month.
	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{
		Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 14,
		Retention: files.RetentionTiered, Tiers: files.RetentionTiers{Daily: 2, Weekly: 2, Monthly: 2},
	})
	f.upload(f.admin, f.shared, "", "family.txt", "hello")
	// 2026-10-13 (Tuesday) to 2026-11-21 (Saturday).
	for day := 1; day <= 40; day++ {
		f.clock.Set(at(day, 3, 0))
		f.runDue()
	}
	want = []string{"定时快照 2026-11-21 03:00", "定时快照 2026-11-20 03:00", "定时快照 2026-11-15 03:00", "定时快照 2026-10-31 03:00"}
	if got := scheduledNames(f.snapshots(f.shared)); !slices.Equal(got, want) {
		t.Fatalf("daily tiers kept %v, want %v", got, want)
	}
}

func TestRetentionSettingsAreChecked(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	base := files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 14}
	for name, change := range map[string]func(*files.SnapshotScheduleSettings){
		"tiers under count": func(s *files.SnapshotScheduleSettings) { s.Tiers.Daily = 7 },
		"unknown retention": func(s *files.SnapshotScheduleSettings) { s.Retention = "forever" },
		"nothing kept":      func(s *files.SnapshotScheduleSettings) { s.Retention = files.RetentionTiered },
		"hours of a daily plan": func(s *files.SnapshotScheduleSettings) {
			s.Retention, s.Tiers = files.RetentionTiered, files.RetentionTiers{Hourly: 24, Daily: 7}
		},
		"days of a weekly plan": func(s *files.SnapshotScheduleSettings) {
			s.Frequency, s.Retention, s.Tiers = files.FrequencyWeekly, files.RetentionTiered, files.RetentionTiers{Daily: 7, Weekly: 4}
		},
		"more than 100": func(s *files.SnapshotScheduleSettings) {
			s.Retention, s.Tiers = files.RetentionTiered, files.RetentionTiers{Daily: 60, Monthly: 41}
		},
		"a negative tier": func(s *files.SnapshotScheduleSettings) {
			s.Retention, s.Tiers = files.RetentionTiered, files.RetentionTiers{Daily: 7, Weekly: -1}
		},
	} {
		settings := base
		change(&settings)
		if _, err := f.files.UpdateSnapshotSchedule(ctx, f.admin, f.private.ID, settings); !errors.Is(err, files.ErrInvalidSchedule) {
			t.Errorf("%s: error = %v, want ErrInvalidSchedule", name, err)
		}
	}
	// Clients that know nothing of retention keep the newest snapshots.
	schedule, err := f.files.UpdateSnapshotSchedule(ctx, f.admin, f.private.ID, base)
	if err != nil || schedule.Retention != files.RetentionCount {
		t.Fatalf("UpdateSnapshotSchedule() = %+v, %v", schedule, err)
	}
}

func TestFileVersionsChangesAndReplacingRestores(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	folder, err := f.files.CreateDirectory(ctx, f.admin, f.private.ID, "", "项目")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	plan := f.upload(f.admin, f.private, folder.ID, "计划.txt", "first draft")
	notes := f.upload(f.admin, f.private, folder.ID, "笔记.txt", "notes")
	f.upload(f.admin, f.private, folder.ID, "预算.txt", "budget")
	a, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "A")
	if err != nil {
		t.Fatalf("CreateSnapshot(A) error = %v", err)
	}
	// Give the edited plan its own modification time.
	time.Sleep(20 * time.Millisecond)
	if _, err := f.files.DeleteByID(ctx, f.admin, plan.ID); err != nil {
		t.Fatal(err)
	}
	plan = f.upload(f.admin, f.private, folder.ID, "计划.txt", "second draft")
	f.clock.Set(at(0, 11, 0))
	b, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "B")
	if err != nil {
		t.Fatalf("CreateSnapshot(B) error = %v", err)
	}
	if _, err := f.files.DeleteByID(ctx, f.admin, notes.ID); err != nil {
		t.Fatal(err)
	}

	versions, err := f.files.EntryVersions(ctx, f.admin, plan.ID)
	if err != nil || versions.Path != "项目/计划.txt" || len(versions.Versions) != 2 {
		t.Fatalf("EntryVersions() = %+v, %v", versions, err)
	}
	if newest, oldest := versions.Versions[0], versions.Versions[1]; newest.SnapshotID != b.ID || !newest.Current || oldest.SnapshotID != a.ID || oldest.Current {
		t.Fatalf("versions = %+v", versions.Versions)
	}
	if _, err := f.files.EntryVersions(ctx, f.member, plan.ID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("a member reading the owner's versions: error = %v", err)
	}

	// A's folder is still in the space with one file removed and one edited.
	root, err := f.files.ListSnapshotEntries(ctx, f.admin, a.ID, "")
	if err != nil || len(root) != 1 || root[0].Change != files.ChangeInside {
		t.Fatalf("A's top level = %+v, %v", root, err)
	}
	inside, err := f.files.ListSnapshotEntries(ctx, f.admin, a.ID, root[0].ID)
	if err != nil {
		t.Fatalf("ListSnapshotEntries() error = %v", err)
	}
	changes := map[string]files.SnapshotChange{}
	for _, entry := range inside {
		changes[entry.Name] = entry.Change
	}
	if changes["计划.txt"] != files.ChangeModified || changes["笔记.txt"] != files.ChangeRemoved || changes["预算.txt"] != "" {
		t.Fatalf("changes since A = %v", changes)
	}

	// Going back to A's plan sends the current one to the recycle bin.
	var oldPlan files.SnapshotEntry
	for _, entry := range inside {
		if entry.Name == "计划.txt" {
			oldPlan = entry
		}
	}
	if _, err := f.files.RestoreSnapshotEntry(ctx, f.admin, a.ID, root[0].ID, "", "项目", true); !errors.Is(err, files.ErrUnsupportedType) {
		t.Fatalf("replacing a folder: error = %v, want ErrUnsupportedType", err)
	}
	replaced, err := f.files.RestoreSnapshotEntry(ctx, f.admin, a.ID, oldPlan.ID, folder.ID, "计划.txt", true)
	if err != nil || replaced.Name != "计划.txt" {
		t.Fatalf("RestoreSnapshotEntry(replace) = %+v, %v", replaced, err)
	}
	content, err := f.files.OpenContent(ctx, f.admin, replaced.ID)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "first draft" {
		t.Fatalf("replaced content = %q", data)
	}
	// The plan comes back as A saved it, so it is A's version again.
	if !replaced.ModifiedAt.Equal(oldPlan.ModifiedAt) {
		t.Fatalf("replaced plan modified at %v, want A's %v", replaced.ModifiedAt, oldPlan.ModifiedAt)
	}
	if versions, err := f.files.EntryVersions(ctx, f.admin, replaced.ID); err != nil || !versions.Versions[1].Current || versions.Versions[0].Current {
		t.Fatalf("versions after going back to A = %+v, %v", versions.Versions, err)
	}
	trash, err := f.files.ListTrash(ctx, f.admin)
	if err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	drafts := 0
	for _, item := range trash {
		if item.Name == "计划.txt" {
			drafts++
		}
	}
	if drafts != 2 {
		t.Fatalf("recycle bin holds %d plans, want the first edit and the replaced one", drafts)
	}
	listing, err := f.files.List(ctx, f.admin, f.private.ID, folder.ID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if names := entryNames(listing); !slices.Equal(names, []string{"计划.txt", "预算.txt"}) {
		t.Fatalf("folder after replacing = %v", names)
	}

	// The whole snapshot comes back as a new folder.
	whole, err := f.files.RestoreSnapshot(ctx, f.admin, a.ID, "", "A 的全部文件")
	if err != nil || whole.Kind != files.EntryKindDirectory {
		t.Fatalf("RestoreSnapshot() = %+v, %v", whole, err)
	}
	top, err := f.files.List(ctx, f.admin, f.private.ID, whole.ID)
	if err != nil || len(top) != 1 || top[0].Name != "项目" {
		t.Fatalf("restored snapshot = %+v, %v", top, err)
	}
	below, err := f.files.List(ctx, f.admin, f.private.ID, top[0].ID)
	if err != nil || !slices.Equal(entryNames(below), []string{"笔记.txt", "计划.txt", "预算.txt"}) {
		t.Fatalf("restored folder = %v, %v", entryNames(below), err)
	}
	for _, entry := range below {
		if saved := inside[slices.IndexFunc(inside, func(item files.SnapshotEntry) bool { return item.Name == entry.Name })]; !entry.ModifiedAt.Equal(saved.ModifiedAt) {
			t.Fatalf("restored %s modified at %v, want %v", entry.Name, entry.ModifiedAt, saved.ModifiedAt)
		}
	}
	if _, err := f.files.RestoreSnapshot(ctx, f.admin, a.ID, "", "A 的全部文件"); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("restoring over an existing folder: error = %v, want ErrConflict", err)
	}
	if _, err := f.files.RestoreSnapshot(ctx, f.member, a.ID, "", "偷看"); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("a member restoring the owner's snapshot: error = %v, want ErrForbidden", err)
	}
}

func TestRemovedEntriesListTheTopmostRemovedItems(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	contracts, err := f.files.CreateDirectory(ctx, f.admin, f.private.ID, "", "合同")
	if err != nil {
		t.Fatal(err)
	}
	f.upload(f.admin, f.private, contracts.ID, "租房.pdf", "lease")
	f.upload(f.admin, f.private, contracts.ID, "保险.pdf", "insurance")
	photo := f.upload(f.admin, f.private, "", "照片.jpg", "jpeg")
	f.upload(f.admin, f.private, "", "留着.txt", "kept")
	a, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	// The photo is gone for good; 合同 below stays in the recycle bin.
	trashed, err := f.files.DeleteByID(ctx, f.admin, photo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.files.Purge(ctx, f.admin, trashed.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(at(0, 11, 0))
	b, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.DeleteByID(ctx, f.admin, contracts.ID); err != nil {
		t.Fatal(err)
	}

	items, truncated, err := f.files.RemovedEntries(ctx, f.admin, f.private.ID)
	if err != nil || truncated || len(items) != 2 {
		t.Fatalf("RemovedEntries() = %+v, %v, %v", items, truncated, err)
	}
	folder, file := items[0], items[1]
	if folder.Path != "合同" || folder.Kind != files.EntryKindDirectory || folder.FileCount != 2 ||
		folder.SizeBytes != int64(len("lease")+len("insurance")) || folder.SnapshotID != b.ID || !folder.GoneBy.IsZero() || !folder.InTrash {
		t.Fatalf("removed folder = %+v", folder)
	}
	if file.Path != "照片.jpg" || file.FileCount != 1 || file.SnapshotID != a.ID || !file.GoneBy.Equal(b.CreatedAt) || file.InTrash {
		t.Fatalf("removed file = %+v", file)
	}
	if _, _, err := f.files.RemovedEntries(ctx, f.member, f.private.ID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("a member listing the owner's removed items: error = %v", err)
	}
}

func entryNames(entries []files.Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	slices.Sort(names)
	return names
}
