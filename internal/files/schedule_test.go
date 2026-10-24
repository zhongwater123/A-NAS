package files_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/files"
)

var shanghai = time.FixedZone("CST", 8*60*60)

// switchableVolume stands in for the data volume guard; it can go offline.
type switchableVolume struct{ offline atomic.Bool }

func (v *switchableVolume) Check(context.Context, string, bool) error {
	if v.offline.Load() {
		return errors.New("data volume is offline")
	}
	return nil
}

type snapshotFixture struct {
	t             *testing.T
	accounts      *accounts.Service
	files         *files.Service
	catalogPath   string
	volumeRoot    string
	admin, member accounts.User
	private       accounts.Space // the administrator's
	memberPrivate accounts.Space
	shared        accounts.Space
	clock         *clocktest.Clock
	volume        *switchableVolume
	// backend replaces the development snapshot backend when set before open.
	backend files.SnapshotBackend
}

// at is a local time on 2026-10-12, a Monday, plus whole days.
func at(days, hour, minute int) time.Time {
	return time.Date(2026, 10, 12+days, hour, minute, 0, 0, shanghai)
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("setup administrator: %v", err)
	}
	member, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	fixture := &snapshotFixture{
		t: t, accounts: accountService, admin: admin, member: member,
		catalogPath: filepath.Join(t.TempDir(), "files.db"), volumeRoot: filepath.Join(t.TempDir(), "volume"),
		clock: clocktest.New(at(0, 10, 30)), volume: &switchableVolume{},
	}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			fixture.shared = space
		} else {
			fixture.private = space
		}
	}
	memberSpaces, err := accountService.ListSpaces(ctx, member)
	if err != nil {
		t.Fatalf("list member spaces: %v", err)
	}
	fixture.memberPrivate = findPrivateSpace(t, memberSpaces)
	return fixture
}

// open opens the catalog; tests that prepare an older catalog call it later.
func (f *snapshotFixture) open() *files.Service {
	f.t.Helper()
	catalog, err := files.OpenSQLite(f.catalogPath)
	if err != nil {
		f.t.Fatalf("open file catalog: %v", err)
	}
	f.t.Cleanup(func() { _ = catalog.Close() })
	f.files = files.NewService(catalog, f.volumeRoot, f.accounts, files.Options{
		DisableCapacityReserve: true, VolumeGuard: f.volume, Now: f.clock.Now, Location: shanghai,
		SnapshotBackend: f.backend,
	})
	return f.files
}

func (f *snapshotFixture) upload(user accounts.User, space accounts.Space, parentID, name, contents string) files.Entry {
	f.t.Helper()
	entry, err := f.files.Upload(context.Background(), user, space.ID, parentID, name, bytes.NewBufferString(contents))
	if err != nil {
		f.t.Fatalf("upload %s: %v", name, err)
	}
	return entry
}

func (f *snapshotFixture) runDue() {
	f.t.Helper()
	if err := f.files.RunDueSnapshots(context.Background(), f.accounts, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		f.t.Fatalf("RunDueSnapshots() error = %v", err)
	}
}

func (f *snapshotFixture) snapshots(space accounts.Space) []files.Snapshot {
	f.t.Helper()
	snapshots, err := f.files.ListSnapshots(context.Background(), f.admin, space.ID)
	if err != nil {
		f.t.Fatalf("ListSnapshots() error = %v", err)
	}
	return snapshots
}

func (f *snapshotFixture) schedule(user accounts.User, space accounts.Space) files.SnapshotSchedule {
	f.t.Helper()
	schedule, err := f.files.SnapshotSchedule(context.Background(), user, space.ID)
	if err != nil {
		f.t.Fatalf("SnapshotSchedule() error = %v", err)
	}
	return schedule
}

func (f *snapshotFixture) setSchedule(user accounts.User, space accounts.Space, settings files.SnapshotScheduleSettings) {
	f.t.Helper()
	if _, err := f.files.UpdateSnapshotSchedule(context.Background(), user, space.ID, settings); err != nil {
		f.t.Fatalf("UpdateSnapshotSchedule() error = %v", err)
	}
}

func origins(snapshots []files.Snapshot) map[files.SnapshotOrigin][]string {
	names := map[files.SnapshotOrigin][]string{}
	for _, snapshot := range snapshots {
		names[snapshot.Origin] = append(names[snapshot.Origin], snapshot.Name)
	}
	return names
}

func TestScheduledSnapshotsRunOncePerSlotAndKeepOnlyTheNewest(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	f.upload(f.admin, f.private, "", "notes.txt", "hello")
	if _, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "整理前"); err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	f.setSchedule(f.admin, f.private, files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyHourly, Time: "00:15", Keep: 2})

	// 10:15 passed before the schedule was set, so the first run is at 11:15.
	f.clock.Set(at(0, 10, 31))
	f.runDue()
	if got := origins(f.snapshots(f.private))[files.SnapshotOriginScheduled]; len(got) != 0 {
		t.Fatalf("scheduled snapshots before the first slot = %v", got)
	}
	if next := f.schedule(f.admin, f.private).NextRunAt; !next.Equal(at(0, 11, 15)) {
		t.Fatalf("next run = %v, want 11:15", next.In(shanghai))
	}
	f.clock.Set(at(0, 11, 16))
	f.runDue()
	f.clock.Set(at(0, 11, 40))
	f.runDue()
	if got := origins(f.snapshots(f.private))[files.SnapshotOriginScheduled]; len(got) != 1 || got[0] != "定时快照 2026-10-12 11:16" {
		t.Fatalf("scheduled snapshots after one slot = %v", got)
	}
	f.clock.Set(at(0, 12, 15))
	f.runDue()
	f.clock.Set(at(0, 13, 15))
	f.runDue()

	got := origins(f.snapshots(f.private))
	if want := []string{"定时快照 2026-10-12 13:15", "定时快照 2026-10-12 12:15"}; len(got[files.SnapshotOriginScheduled]) != 2 ||
		got[files.SnapshotOriginScheduled][0] != want[0] || got[files.SnapshotOriginScheduled][1] != want[1] {
		t.Fatalf("scheduled snapshots = %v, want %v", got[files.SnapshotOriginScheduled], want)
	}
	if manual := got[files.SnapshotOriginManual]; len(manual) != 1 || manual[0] != "整理前" {
		t.Fatalf("manual snapshots = %v, want the one taken by hand", manual)
	}
	schedule := f.schedule(f.admin, f.private)
	if schedule.LastRun == nil || schedule.LastRun.Status != files.ScheduleRunSucceeded || schedule.LastRun.SnapshotID != f.snapshots(f.private)[0].ID {
		t.Fatalf("last run = %+v", schedule.LastRun)
	}
	if !schedule.NextRunAt.Equal(at(0, 14, 15)) {
		t.Fatalf("next run = %v, want 14:15", schedule.NextRunAt.In(shanghai))
	}

	// A kept snapshot no longer counts against the schedule.
	kept, err := f.files.KeepSnapshot(ctx, f.admin, f.snapshots(f.private)[1].ID)
	if err != nil || kept.Origin != files.SnapshotOriginManual {
		t.Fatalf("KeepSnapshot() = %+v, %v", kept, err)
	}
	f.clock.Set(at(0, 14, 15))
	f.runDue()
	f.clock.Set(at(0, 15, 15))
	f.runDue()
	got = origins(f.snapshots(f.private))
	if len(got[files.SnapshotOriginScheduled]) != 2 || len(got[files.SnapshotOriginManual]) != 2 {
		t.Fatalf("snapshots after keeping one = %v", got)
	}
}

func TestScheduleCatchesUpOnceAndRetriesWhileTheVolumeIsOffline(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	f.upload(f.admin, f.shared, "", "family.txt", "hello")
	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 7})

	f.volume.offline.Store(true)
	f.clock.Set(at(1, 3, 0))
	f.runDue()
	schedule := f.schedule(f.member, f.shared)
	if schedule.LastRun == nil || schedule.LastRun.Status != files.ScheduleRunFailed || schedule.LastRun.Error != "volume_unavailable" {
		t.Fatalf("last run while offline = %+v", schedule.LastRun)
	}
	if !schedule.NextRunAt.Equal(at(1, 3, 15)) {
		t.Fatalf("retry = %v, want 03:15", schedule.NextRunAt.In(shanghai))
	}
	if schedule.CanManage {
		t.Fatal("a member can manage the shared schedule")
	}
	if _, err := f.files.ListSnapshots(context.Background(), f.admin, f.shared.ID); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("ListSnapshots() offline error = %v, want ErrVolumeUnavailable", err)
	}
	f.clock.Set(at(1, 3, 10))
	f.runDue()
	if again := f.schedule(f.admin, f.shared).LastRun; !again.At.Equal(schedule.LastRun.At) {
		t.Fatalf("retried before the delay at %v", again.At)
	}
	f.volume.offline.Store(false)
	f.clock.Set(at(1, 3, 15))
	f.runDue()
	if got := f.snapshots(f.shared); len(got) != 1 || got[0].Origin != files.SnapshotOriginScheduled || got[0].FileCount != 1 || got[0].SizeBytes != 5 {
		t.Fatalf("snapshots after the retry = %+v", got)
	}

	// Off for three days: one snapshot on return, not three.
	f.clock.Set(at(5, 9, 0))
	f.runDue()
	f.runDue()
	if got := f.snapshots(f.shared); len(got) != 2 {
		t.Fatalf("snapshots after downtime = %d, want 2", len(got))
	}

	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyWeekly, Time: "08:00", Weekday: 1, Keep: 4})
	if next := f.schedule(f.admin, f.shared).NextRunAt; !next.Equal(at(7, 8, 0)) {
		t.Fatalf("weekly next run = %v, want Monday 2026-10-19 08:00", next.In(shanghai))
	}
	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{Frequency: files.FrequencyWeekly, Time: "08:00", Weekday: 1, Keep: 4})
	if schedule := f.schedule(f.admin, f.shared); !schedule.NextRunAt.IsZero() || schedule.Enabled {
		t.Fatalf("disabled schedule = %+v", schedule)
	}
}

func TestOnlyOwnersAndAdministratorsManageSnapshots(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	f.upload(f.admin, f.shared, "", "family.txt", "hello")
	daily := files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 7}

	if _, err := f.files.CreateSnapshot(ctx, f.member, f.shared.ID, "成员快照"); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("member CreateSnapshot(Shared) error = %v, want ErrForbidden", err)
	}
	if _, err := f.files.UpdateSnapshotSchedule(ctx, f.member, f.shared.ID, daily); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("member UpdateSnapshotSchedule(Shared) error = %v, want ErrForbidden", err)
	}
	if _, err := f.files.UpdateSnapshotSchedule(ctx, f.admin, f.memberPrivate.ID, daily); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("admin UpdateSnapshotSchedule(member private) error = %v, want ErrForbidden", err)
	}
	if _, err := f.files.SnapshotSchedule(ctx, f.admin, f.memberPrivate.ID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("admin SnapshotSchedule(member private) error = %v, want ErrForbidden", err)
	}
	if !f.schedule(f.member, f.memberPrivate).CanManage || !f.schedule(f.admin, f.shared).CanManage {
		t.Fatal("owner or administrator cannot manage their schedule")
	}
	snapshot, err := f.files.CreateSnapshot(ctx, f.admin, f.shared.ID, "全家")
	if err != nil {
		t.Fatalf("admin CreateSnapshot(Shared) error = %v", err)
	}
	if err := f.files.DeleteSnapshot(ctx, f.member, snapshot.ID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("member DeleteSnapshot(Shared) error = %v, want ErrForbidden", err)
	}
	if _, err := f.files.KeepSnapshot(ctx, f.member, snapshot.ID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("member KeepSnapshot(Shared) error = %v, want ErrForbidden", err)
	}
	// Members still browse and restore from the shared snapshots.
	if _, err := f.files.ListSnapshotEntries(ctx, f.member, snapshot.ID, ""); err != nil {
		t.Fatalf("member ListSnapshotEntries(Shared) error = %v", err)
	}

	for _, invalid := range []files.SnapshotScheduleSettings{
		{Frequency: "monthly", Time: "03:00", Keep: 7},
		{Frequency: files.FrequencyDaily, Time: "24:00", Keep: 7},
		{Frequency: files.FrequencyDaily, Time: "3:00", Keep: 7},
		{Frequency: files.FrequencyDaily, Time: "03:00", Keep: 0},
		{Frequency: files.FrequencyDaily, Time: "03:00", Keep: files.MaxScheduledSnapshots + 1},
		{Frequency: files.FrequencyWeekly, Time: "03:00", Weekday: 7, Keep: 7},
	} {
		if _, err := f.files.UpdateSnapshotSchedule(ctx, f.admin, f.shared.ID, invalid); !errors.Is(err, files.ErrInvalidSchedule) {
			t.Fatalf("UpdateSnapshotSchedule(%+v) error = %v, want ErrInvalidSchedule", invalid, err)
		}
	}
}

func TestRestoreSearchAndDownloadFromSnapshots(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	folder, err := f.files.CreateDirectory(ctx, f.admin, f.private.ID, "", "报告")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	attachments, err := f.files.CreateDirectory(ctx, f.admin, f.private.ID, folder.ID, "附件")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	first := f.upload(f.admin, f.private, folder.ID, "2026.txt", "first draft")
	f.upload(f.admin, f.private, attachments.ID, "图.png", "png")
	f.upload(f.admin, f.private, "", "100%.txt", "percent")
	take := func(name string) files.Snapshot {
		t.Helper()
		snapshot, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, name)
		if err != nil {
			t.Fatalf("CreateSnapshot(%s) error = %v", name, err)
		}
		return snapshot
	}
	a := take("A")
	if a.FileCount != 3 || a.SizeBytes != int64(len("first draft")+len("png")+len("percent")) {
		t.Fatalf("snapshot totals = %d files, %d bytes", a.FileCount, a.SizeBytes)
	}
	if _, err := f.files.DeleteByID(ctx, f.admin, first.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	// Keep the second version's modification time apart from the first.
	time.Sleep(20 * time.Millisecond)
	f.upload(f.admin, f.private, folder.ID, "2026.txt", "v2")
	f.clock.Set(at(0, 11, 0))
	b := take("B")
	f.clock.Set(at(0, 12, 0))
	c := take("C")

	matches, truncated, err := f.files.SearchSnapshots(ctx, f.admin, f.private.ID, "2026")
	if err != nil || truncated || len(matches) != 1 {
		t.Fatalf("SearchSnapshots(2026) = %+v, %v, %v", matches, truncated, err)
	}
	match := matches[0]
	if match.Path != "报告/2026.txt" || !match.InSpace || len(match.Versions) != 2 {
		t.Fatalf("match = %+v", match)
	}
	newest, oldest := match.Versions[0], match.Versions[1]
	if newest.SnapshotID != c.ID || newest.Snapshots != 2 || !newest.OldestAt.Equal(b.CreatedAt) || newest.SizeBytes != 2 || newest.ModifiedAt.IsZero() {
		t.Fatalf("newest version = %+v", newest)
	}
	if oldest.SnapshotID != a.ID || oldest.Snapshots != 1 || oldest.SizeBytes != int64(len("first draft")) {
		t.Fatalf("oldest version = %+v", oldest)
	}
	if percent, _, err := f.files.SearchSnapshots(ctx, f.admin, f.private.ID, "%"); err != nil || len(percent) != 1 || percent[0].Name != "100%.txt" {
		t.Fatalf("SearchSnapshots(%%) = %+v, %v", percent, err)
	}
	if folders, _, err := f.files.SearchSnapshots(ctx, f.admin, f.private.ID, "报告"); err != nil || len(folders) != 1 ||
		folders[0].Kind != files.EntryKindDirectory || len(folders[0].Versions) != 1 || folders[0].Versions[0].Snapshots != 3 {
		t.Fatalf("SearchSnapshots(报告) = %+v, %v", folders, err)
	}
	if _, _, err := f.files.SearchSnapshots(ctx, f.admin, f.private.ID, "  "); !errors.Is(err, files.ErrInvalidQuery) {
		t.Fatalf("SearchSnapshots(blank) error = %v, want ErrInvalidQuery", err)
	}

	content, err := f.files.OpenSnapshotContent(ctx, f.admin, a.ID, oldest.EntryID)
	if err != nil {
		t.Fatalf("OpenSnapshotContent() error = %v", err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "first draft" || content.Name != "2026.txt" || content.SizeBytes != int64(len(data)) {
		t.Fatalf("snapshot content = %q (%+v)", data, content)
	}

	// The whole folder goes to the recycle bin, then comes back from A.
	if _, err := f.files.DeleteByID(ctx, f.admin, folder.ID); err != nil {
		t.Fatalf("Delete(folder) error = %v", err)
	}
	if gone, _, _ := f.files.SearchSnapshots(ctx, f.admin, f.private.ID, "2026"); len(gone) != 1 || gone[0].InSpace {
		t.Fatalf("search after deleting the folder = %+v", gone)
	}
	root, err := f.files.ListSnapshotEntries(ctx, f.admin, a.ID, "")
	if err != nil {
		t.Fatalf("ListSnapshotEntries() error = %v", err)
	}
	var snapshotFolder files.SnapshotEntry
	for _, entry := range root {
		if entry.Name == "报告" {
			snapshotFolder = entry
		}
	}
	restored, err := f.files.RestoreSnapshotEntry(ctx, f.admin, a.ID, snapshotFolder.ID, "", "", false)
	if err != nil {
		t.Fatalf("RestoreSnapshotEntry(folder) error = %v", err)
	}
	if restored.Kind != files.EntryKindDirectory || restored.Name != "报告" {
		t.Fatalf("restored = %+v", restored)
	}
	tree, err := f.files.List(ctx, f.admin, f.private.ID, restored.ID)
	if err != nil || len(tree) != 2 {
		t.Fatalf("restored folder = %+v, %v", tree, err)
	}
	for _, entry := range tree {
		if entry.Name == "2026.txt" {
			opened, err := f.files.OpenContent(ctx, f.admin, entry.ID)
			if err != nil {
				t.Fatalf("OpenContent() error = %v", err)
			}
			restoredData, _ := io.ReadAll(opened.Reader)
			_ = opened.Reader.Close()
			if string(restoredData) != "first draft" {
				t.Fatalf("restored 2026.txt = %q", restoredData)
			}
		}
		if entry.Name == "附件" {
			inner, err := f.files.List(ctx, f.admin, f.private.ID, entry.ID)
			if err != nil || len(inner) != 1 || inner[0].Name != "图.png" {
				t.Fatalf("restored 附件 = %+v, %v", inner, err)
			}
		}
	}
	if _, err := f.files.RestoreSnapshotEntry(ctx, f.admin, a.ID, snapshotFolder.ID, "", "", false); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("restoring over an existing folder error = %v, want ErrConflict", err)
	}
}

func TestCatalogsFromEarlierVersionsGainSnapshotTotals(t *testing.T) {
	f := newSnapshotFixture(t)
	db, err := sql.Open("sqlite3", f.catalogPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE snapshots (id TEXT PRIMARY KEY, space_id TEXT NOT NULL, name TEXT NOT NULL, created_by TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(space_id, name))`,
		`CREATE TABLE snapshot_entries (id TEXT PRIMARY KEY, snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE, parent_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('file', 'directory')), size_bytes INTEGER NOT NULL, backend_key TEXT NOT NULL, UNIQUE(snapshot_id, backend_key))`,
		`INSERT INTO snapshots VALUES ('snapshot:old', '` + f.private.ID + `', '旧快照', '` + f.admin.ID + `', '2026-10-01T00:00:00Z')`,
		`INSERT INTO snapshot_entries VALUES ('snapshot-entry:dir', 'snapshot:old', '', 'docs', 'directory', 4096, 'docs')`,
		`INSERT INTO snapshot_entries VALUES ('snapshot-entry:file', 'snapshot:old', 'snapshot-entry:dir', 'a.txt', 'file', 5, 'docs/a.txt')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare earlier catalog: %v", err)
		}
	}
	_ = db.Close()

	f.open()
	snapshots := f.snapshots(f.private)
	if len(snapshots) != 1 || snapshots[0].Origin != files.SnapshotOriginManual || snapshots[0].FileCount != 1 || snapshots[0].SizeBytes != 5 {
		t.Fatalf("migrated snapshots = %+v", snapshots)
	}
	entries, err := f.files.ListSnapshotEntries(context.Background(), f.admin, "snapshot:old", "snapshot-entry:dir")
	if err != nil || len(entries) != 1 || !entries[0].ModifiedAt.IsZero() {
		t.Fatalf("migrated entries = %+v, %v", entries, err)
	}
}

// A space nobody has set a schedule for is still protected: a daily snapshot
// at 03:00, keeping 14, counted from when the scheduler first sees it.
func TestSpacesWithoutAScheduleGetTheDailyDefault(t *testing.T) {
	f := newSnapshotFixture(t)
	f.clock.Set(at(0, 10, 0))
	f.open()
	// The development snapshot backend copies folders that must exist.
	f.upload(f.admin, f.private, "", "notes.txt", "hello")
	f.upload(f.admin, f.shared, "", "family.txt", "hello")
	f.upload(f.member, f.memberPrivate, "", "mine.txt", "hello")

	schedule := f.schedule(f.admin, f.private)
	if !schedule.Enabled || schedule.Frequency != files.FrequencyDaily || schedule.Time != "03:00" || schedule.Keep != 14 {
		t.Fatalf("default schedule = %+v", schedule)
	}
	if !schedule.NextRunAt.Equal(at(1, 3, 0)) {
		t.Fatalf("default next run = %v, want tomorrow 03:00", schedule.NextRunAt.In(shanghai))
	}
	// 03:00 today has passed already; nothing is taken for it.
	f.runDue()
	if got := f.snapshots(f.private); len(got) != 0 {
		t.Fatalf("snapshots on the first run = %v", got)
	}
	f.clock.Set(at(1, 3, 1))
	f.runDue()
	for _, space := range []accounts.Space{f.private, f.shared} {
		if got := origins(f.snapshots(space))[files.SnapshotOriginScheduled]; len(got) != 1 {
			t.Fatalf("scheduled snapshots of %s = %v, want one", space.Name, got)
		}
	}
	if got, err := f.files.ListSnapshots(context.Background(), f.member, f.memberPrivate.ID); err != nil || len(got) != 1 {
		t.Fatalf("the member's scheduled snapshots = %v, %v; want one", got, err)
	}

	// Turning it off sticks: the default does not come back.
	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{Frequency: files.FrequencyDaily, Time: "03:00", Keep: 14})
	f.clock.Set(at(2, 3, 1))
	f.runDue()
	if got := origins(f.snapshots(f.shared))[files.SnapshotOriginScheduled]; len(got) != 1 {
		t.Fatalf("scheduled snapshots of a disabled schedule = %v", got)
	}
	if f.schedule(f.admin, f.shared).Enabled {
		t.Fatal("a disabled schedule came back on")
	}
}
