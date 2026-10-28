package files_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/sqlitetest"
)

// memorySnapshots is a snapshot backend that remembers which snapshots exist
// and lists the same objects for each.
type memorySnapshots struct {
	mu      sync.Mutex
	taken   map[string]bool
	listing func(ctx context.Context, visit func(files.SnapshotObject) error) error
	deleted []string
	// deleteErr, when set, fails every Delete, as while the Host Agent
	// restarts.
	deleteErr error
	// surveyed is what the last Survey was asked about; nil before one.
	surveyed []string
}

func newMemorySnapshots(listing func(context.Context, func(files.SnapshotObject) error) error) *memorySnapshots {
	return &memorySnapshots{taken: map[string]bool{}, listing: listing}
}

func (m *memorySnapshots) Create(_ context.Context, _, snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taken[snapshotID] = true
	return nil
}

func (m *memorySnapshots) Objects(ctx context.Context, snapshotID string, visit func(files.SnapshotObject) error) error {
	if !m.exists(snapshotID) {
		return files.ErrNotFound
	}
	return m.listing(ctx, visit)
}

func (*memorySnapshots) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return nil, files.SnapshotObject{}, files.ErrNotFound
}

// Delete reports a missing snapshot as the Host Agent does for a space it no
// longer knows, and refuses an ended context as a real request would.
func (m *memorySnapshots) Delete(ctx context.Context, _, snapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if !m.taken[snapshotID] {
		return files.ErrNotFound
	}
	delete(m.taken, snapshotID)
	m.deleted = append(m.deleted, snapshotID)
	return nil
}

func (m *memorySnapshots) Survey(_ context.Context, known []string) (files.SnapshotSurvey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.surveyed = append([]string{}, known...)
	survey := files.SnapshotSurvey{Present: []string{}}
	for id := range m.taken {
		if slices.Contains(known, id) {
			survey.Present = append(survey.Present, id)
		} else {
			survey.Unknown++
		}
	}
	return survey, nil
}

func (m *memorySnapshots) exists(snapshotID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taken[snapshotID]
}

func (m *memorySnapshots) ids() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.taken {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// folderOfFiles lists one folder holding count files of 1,000 bytes each.
func folderOfFiles(count int) func(context.Context, func(files.SnapshotObject) error) error {
	return func(_ context.Context, visit func(files.SnapshotObject) error) error {
		if err := visit(files.SnapshotObject{Key: "照片", Name: "照片", Kind: files.EntryKindDirectory}); err != nil {
			return err
		}
		for index := range count {
			name := fmt.Sprintf("IMG_%06d.jpg", index)
			if err := visit(files.SnapshotObject{Key: "照片/" + name, ParentKey: "照片", Name: name, Kind: files.EntryKindFile,
				SizeBytes: 1000, ModifiedAt: time.Date(2026, 10, 1, 0, 0, index, 0, time.UTC)}); err != nil {
				return err
			}
		}
		return nil
	}
}

// A snapshot whose listing fails, or whose request ends, is deleted again
// instead of staying behind as a subvolume nobody records (#162).
func TestSnapshotsThatFailAreDeletedAgain(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(func(_ context.Context, visit func(files.SnapshotObject) error) error {
		if err := visit(files.SnapshotObject{Key: "a.txt", Name: "a.txt", Kind: files.EntryKindFile}); err != nil {
			return err
		}
		return errors.New("listing cut short")
	})
	f.backend = backend
	f.open()
	if _, err := f.files.CreateSnapshot(context.Background(), f.admin, f.private.ID, "失败"); err == nil {
		t.Fatal("CreateSnapshot() succeeded with a listing cut short")
	}
	if ids := backend.ids(); len(ids) != 0 || len(backend.deleted) != 1 {
		t.Fatalf("snapshots left = %v, deleted = %v", ids, backend.deleted)
	}

	// The tab closes while the space is walked.
	ctx, cancel := context.WithCancel(context.Background())
	backend.listing = func(context.Context, func(files.SnapshotObject) error) error {
		cancel()
		return context.Canceled
	}
	if _, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "关闭了标签页"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateSnapshot() with a closed request error = %v", err)
	}
	if ids := backend.ids(); len(ids) != 0 || len(backend.deleted) != 2 {
		t.Fatalf("snapshots left after a closed request = %v, deleted = %v", ids, backend.deleted)
	}
	if got := f.snapshots(f.private); len(got) != 0 {
		t.Fatalf("failed snapshots are listed: %+v", got)
	}

	// A record whose snapshot is already gone can still be deleted.
	backend.listing = folderOfFiles(1)
	snapshot, err := f.files.CreateSnapshot(context.Background(), f.admin, f.private.ID, "已丢失")
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	backend.mu.Lock()
	delete(backend.taken, snapshot.ID)
	backend.mu.Unlock()
	if err := f.files.DeleteSnapshot(context.Background(), f.admin, snapshot.ID); err != nil {
		t.Fatalf("DeleteSnapshot() of a snapshot already gone error = %v", err)
	}
	if got := f.snapshots(f.private); len(got) != 0 {
		t.Fatalf("snapshots after deleting = %+v", got)
	}
	// Nothing that failed is left for a sweep.
	if sweep, err := f.files.SweepSnapshots(context.Background()); err != nil || sweep.Snapshots != 0 || sweep.Unrecorded != 0 {
		t.Fatalf("SweepSnapshots() after the failures = %+v, %v; want nothing to do", sweep, err)
	}
}

// leaveUnfinished takes a snapshot whose listing fails while the backend
// cannot delete, as when the Host Agent restarts meanwhile: the snapshot
// stays on the volume, and so does its pending note. It returns its ID.
func leaveUnfinished(t *testing.T, f *snapshotFixture, backend *memorySnapshots, space accounts.Space, name string) string {
	t.Helper()
	before := backend.ids()
	listing := backend.listing
	backend.listing = func(context.Context, func(files.SnapshotObject) error) error { return errors.New("listing cut short") }
	backend.mu.Lock()
	backend.deleteErr = errors.New("host agent restarting")
	backend.mu.Unlock()
	if _, err := f.files.CreateSnapshot(context.Background(), f.admin, space.ID, name); err == nil {
		t.Fatal("CreateSnapshot() succeeded with a listing cut short")
	}
	backend.listing = listing
	backend.mu.Lock()
	backend.deleteErr = nil
	backend.mu.Unlock()
	left := slices.DeleteFunc(backend.ids(), func(id string) bool { return slices.Contains(before, id) })
	if len(left) != 1 {
		t.Fatalf("snapshots left by the failed discard = %v, want one", left)
	}
	return left[0]
}

// notePending writes the pending note of a snapshot, as a crash while it was
// taken leaves it.
func (f *snapshotFixture) notePending(snapshotID string, space accounts.Space) {
	f.t.Helper()
	db, err := sql.Open("sqlite3", f.catalogPath)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("INSERT INTO pending_snapshots(id, space_id, created_at) VALUES(?,?,?)",
		snapshotID, space.ID, "2026-10-12T02:00:00Z"); err != nil {
		f.t.Fatal(err)
	}
}

// auditDetails returns the details of the catalog's events of action.
func (f *snapshotFixture) auditDetails(action string) []string {
	f.t.Helper()
	db, err := sql.Open("sqlite3", f.catalogPath)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT detail FROM audit_events WHERE action = ? ORDER BY rowid", action)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			f.t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return details
}

// A sweep deletes the snapshots whose taking never finished and the records
// whose snapshot is gone. A snapshot the catalog does not know it leaves
// alone (#269).
func TestSweepRemovesUnfinishedSnapshotsAndRecordsWithoutTheirSnapshot(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(folderOfFiles(2))
	f.backend = backend
	f.open()
	ctx := context.Background()
	kept, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "保留")
	if err != nil {
		t.Fatal(err)
	}
	lost, err := f.files.CreateSnapshot(ctx, f.admin, f.shared.ID, "已丢失")
	if err != nil {
		t.Fatal(err)
	}
	unfinished := leaveUnfinished(t, f, backend, f.private, "重启时")
	backend.mu.Lock()
	delete(backend.taken, lost.ID)
	backend.taken["snapshot:unknown"] = true
	backend.mu.Unlock()

	f.volume.offline.Store(true)
	if _, err := f.files.SweepSnapshots(ctx); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("SweepSnapshots() offline error = %v, want ErrVolumeUnavailable", err)
	}
	if !backend.exists(unfinished) {
		t.Fatal("a sweep without the data volume removed a snapshot")
	}
	f.volume.offline.Store(false)

	sweep, err := f.files.SweepSnapshots(ctx)
	if err != nil {
		t.Fatalf("SweepSnapshots() error = %v", err)
	}
	if sweep.Snapshots != 1 || len(sweep.Records) != 1 || sweep.Records[0].ID != lost.ID || sweep.Unrecorded != 1 || sweep.Unfound != 0 {
		t.Fatalf("sweep = %+v, want the unfinished snapshot and the lost record removed and 1 unknown", sweep)
	}
	if ids := backend.ids(); !slices.Equal(ids, []string{kept.ID, "snapshot:unknown"}) {
		t.Fatalf("snapshots after the sweep = %v, want %s and the unknown one", ids, kept.ID)
	}
	if got := f.snapshots(f.shared); len(got) != 0 {
		t.Fatalf("the lost snapshot is still listed: %+v", got)
	}
	if got := f.snapshots(f.private); len(got) != 1 || got[0].ID != kept.ID {
		t.Fatalf("kept snapshots = %+v", got)
	}
	if again, err := f.files.SweepSnapshots(ctx); err != nil || again.Snapshots != 0 || len(again.Records) != 0 || again.Unrecorded != 1 {
		t.Fatalf("second sweep = %+v, %v; want nothing to do", again, err)
	}
}

// A sweep while a snapshot is being taken keeps it, although the catalog
// does not record it yet, and does not count it as unknown.
func TestSweepKeepsASnapshotBeingTaken(t *testing.T) {
	f := newSnapshotFixture(t)
	listing, release := make(chan struct{}), make(chan struct{})
	backend := newMemorySnapshots(func(ctx context.Context, visit func(files.SnapshotObject) error) error {
		close(listing)
		<-release
		return folderOfFiles(1)(ctx, visit)
	})
	f.backend = backend
	f.open()
	created := make(chan error, 1)
	go func() {
		_, err := f.files.CreateSnapshot(context.Background(), f.admin, f.private.ID, "进行中")
		created <- err
	}()
	<-listing
	taking := backend.ids()
	sweep, err := f.files.SweepSnapshots(context.Background())
	if err != nil || sweep.Snapshots != 0 || sweep.Unrecorded != 0 {
		t.Fatalf("SweepSnapshots() = %+v, %v; want nothing removed or unknown", sweep, err)
	}
	if len(taking) != 1 || !slices.Contains(backend.surveyed, taking[0]) || !backend.exists(taking[0]) {
		t.Fatalf("sweep knew %v while %v was being taken", backend.surveyed, taking)
	}
	close(release)
	if err := <-created; err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	if got := f.snapshots(f.private); len(got) != 1 || got[0].ID != taking[0] {
		t.Fatalf("snapshots = %+v", got)
	}
}

// A read of the catalog that fails part way, as a failing disk would, ends
// the sweep before it deletes anything (#269).
func TestSweepStopsWhenReadingTheCatalogFails(t *testing.T) {
	for _, cut := range []struct {
		match string
		after int
	}{
		{"FROM snapshots ORDER BY created_at", 0},
		{"FROM snapshots ORDER BY created_at", 2},
		{"FROM pending_snapshots", 0},
	} {
		t.Run(fmt.Sprintf("%s after %d rows", cut.match, cut.after), func(t *testing.T) {
			f := newSnapshotFixture(t)
			backend := newMemorySnapshots(folderOfFiles(1))
			f.backend = backend
			f.open()
			var recorded []files.Snapshot
			for _, name := range []string{"一", "二", "三"} {
				snapshot, err := f.files.CreateSnapshot(context.Background(), f.admin, f.private.ID, name)
				if err != nil {
					t.Fatal(err)
				}
				recorded = append(recorded, snapshot)
			}
			leaveUnfinished(t, f, backend, f.private, "重启时")
			backend.mu.Lock()
			delete(backend.taken, recorded[0].ID)
			backend.mu.Unlock()
			taken := backend.ids()
			f.driver = sqlitetest.Register(cut.match, cut.after)
			f.open()
			if _, err := f.files.SweepSnapshots(context.Background()); !errors.Is(err, sqlitetest.ErrCut) {
				t.Fatalf("SweepSnapshots() error = %v, want ErrCut", err)
			}
			if len(backend.deleted) != 0 || backend.surveyed != nil || !slices.Equal(backend.ids(), taken) {
				t.Fatalf("the sweep deleted %v and surveyed %v; snapshots %v, were %v", backend.deleted, backend.surveyed, backend.ids(), taken)
			}
			if got := f.snapshots(f.private); len(got) != 3 {
				t.Fatalf("records after the failed sweep = %+v, want all 3", got)
			}
		})
	}
}

// Snapshots the catalog neither records nor was taking, as those of a
// catalog that was lost or restored from an older copy, stay on the volume
// (#269). Administrators learn of them in the activity log, once for each
// count.
func TestSweepLeavesSnapshotsTheCatalogDoesNotKnow(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(folderOfFiles(1))
	backend.taken["snapshot:a"] = true
	backend.taken["snapshot:b"] = true
	f.backend = backend
	f.open()
	ctx := context.Background()
	for range 2 {
		sweep, err := f.files.SweepSnapshots(ctx)
		if err != nil || sweep.Unrecorded != 2 || sweep.Snapshots != 0 || len(sweep.Records) != 0 {
			t.Fatalf("SweepSnapshots() of an empty catalog = %+v, %v; want 2 unknown and nothing removed", sweep, err)
		}
	}
	if details := f.auditDetails("snapshot.unrecorded"); !slices.Equal(details, []string{"2"}) {
		t.Fatalf("activity = %v, want one event for 2 snapshots", details)
	}

	backend.mu.Lock()
	delete(backend.taken, "snapshot:b")
	backend.mu.Unlock()
	if _, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "目录中有"); err != nil {
		t.Fatal(err)
	}
	if sweep, err := f.files.SweepSnapshots(ctx); err != nil || sweep.Unrecorded != 1 {
		t.Fatalf("SweepSnapshots() = %+v, %v; want 1 unknown", sweep, err)
	}
	if details := f.auditDetails("snapshot.unrecorded"); !slices.Equal(details, []string{"2", "1"}) {
		t.Fatalf("activity = %v, want a second event for 1 snapshot", details)
	}
	if len(backend.deleted) != 0 || !backend.exists("snapshot:a") {
		t.Fatalf("the sweeps deleted %v", backend.deleted)
	}
}

// When none of the catalog's snapshots is on the volume, their folder is
// more likely gone or unreadable than every one deleted: the records stay,
// and administrators learn of it.
func TestSweepKeepsRecordsWhenNoneIsOnTheVolume(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(folderOfFiles(1))
	f.backend = backend
	f.open()
	ctx := context.Background()
	first, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "一")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "二")
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	clear(backend.taken)
	backend.mu.Unlock()
	sweep, err := f.files.SweepSnapshots(ctx)
	if err != nil || sweep.Unfound != 2 || len(sweep.Records) != 0 {
		t.Fatalf("SweepSnapshots() = %+v, %v; want both records kept", sweep, err)
	}
	if got := f.snapshots(f.private); len(got) != 2 {
		t.Fatalf("records = %+v, want both", got)
	}
	if details := f.auditDetails("snapshot.unfound"); !slices.Equal(details, []string{"2"}) {
		t.Fatalf("activity = %v, want one event for 2 records", details)
	}

	// With one back, the other is gone indeed.
	backend.mu.Lock()
	backend.taken[second.ID] = true
	backend.mu.Unlock()
	sweep, err = f.files.SweepSnapshots(ctx)
	if err != nil || sweep.Unfound != 0 || len(sweep.Records) != 1 || sweep.Records[0].ID != first.ID {
		t.Fatalf("SweepSnapshots() = %+v, %v; want the record of %s removed", sweep, err, first.ID)
	}
}

// The scheduler sweeps before it takes anything, so a restart clears what a
// crash or a failed discard left behind, even before the catalog records
// any snapshot.
func TestSchedulerSweepsWhenItStarts(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(folderOfFiles(1))
	f.backend = backend
	f.open()
	unfinished := leaveUnfinished(t, f, backend, f.private, "重启时")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		f.files.RunSnapshotSchedules(ctx, f.accounts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for backend.exists(unfinished) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-stopped
	if backend.exists(unfinished) {
		t.Fatal("the scheduler did not sweep when it started")
	}
}

// A space far beyond the 1 MiB the Host Agent's reply used to be cut at
// (about 6,000 entries) gets its scheduled snapshot.
func TestScheduledSnapshotOfALargeSpace(t *testing.T) {
	f := newSnapshotFixture(t)
	f.backend = newMemorySnapshots(folderOfFiles(20_000))
	f.open()
	f.setSchedule(f.admin, f.shared, files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyDaily, Time: "03:00", Keep: 7})
	f.clock.Set(at(1, 3, 0))
	f.runDue()
	schedule := f.schedule(f.admin, f.shared)
	if schedule.LastRun == nil || schedule.LastRun.Status != files.ScheduleRunSucceeded {
		t.Fatalf("last run = %+v", schedule.LastRun)
	}
	got := f.snapshots(f.shared)
	if len(got) != 1 || got[0].FileCount != 20_000 || got[0].SizeBytes != 20_000_000 {
		t.Fatalf("snapshots = %+v, want one of 20000 files", got)
	}
	top, err := f.files.ListSnapshotEntries(context.Background(), f.admin, got[0].ID, "")
	if err != nil || len(top) != 1 {
		t.Fatalf("ListSnapshotEntries() = %+v, %v", top, err)
	}
	inside, err := f.files.ListSnapshotEntries(context.Background(), f.admin, got[0].ID, top[0].ID)
	if err != nil || len(inside) != 20_000 {
		t.Fatalf("ListSnapshotEntries(照片) = %d entries, %v", len(inside), err)
	}
}

// The development backend copies spaces into directories. Its sweep removes
// the copies whose taking never finished, a copy cut short included, and
// leaves one nobody knows.
func TestDevelopmentSnapshotsAreSweptToo(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	f.upload(f.admin, f.private, "", "notes.txt", "hello")
	snapshot, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "开发")
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	f.notePending("snapshot:crashed", f.private)
	f.notePending("snapshot:stopped", f.private)
	snapshots := filepath.Join(f.volumeRoot, ".a-nas-snapshots")
	for _, leftover := range []string{"snapshotcrashed", "snapshotstopped.incoming", "snapshotforeign"} {
		if err := os.MkdirAll(filepath.Join(snapshots, leftover, "docs"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sweep, err := f.files.SweepSnapshots(ctx)
	if err != nil || sweep.Snapshots != 2 || len(sweep.Records) != 0 || sweep.Unrecorded != 1 {
		t.Fatalf("SweepSnapshots() = %+v, %v", sweep, err)
	}
	items, _ := os.ReadDir(snapshots)
	var left []string
	for _, item := range items {
		left = append(left, item.Name())
	}
	want := []string{"snapshotforeign", strings.ToLower(strings.ReplaceAll(snapshot.ID, ":", ""))}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("development snapshots after the sweep = %v, want %v", left, want)
	}
	content, err := f.files.ListSnapshotEntries(ctx, f.admin, snapshot.ID, "")
	if err != nil || len(content) != 1 || content[0].Name != "notes.txt" || content[0].Change != "" {
		t.Fatalf("snapshot entries = %+v, %v; want notes.txt unchanged", content, err)
	}
}
