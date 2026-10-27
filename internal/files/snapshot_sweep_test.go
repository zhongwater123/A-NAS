package files_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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
	kept    []string
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
	if !m.taken[snapshotID] {
		return files.ErrNotFound
	}
	delete(m.taken, snapshotID)
	m.deleted = append(m.deleted, snapshotID)
	return nil
}

func (m *memorySnapshots) Prune(_ context.Context, keep []string) (files.SnapshotPrune, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kept = append([]string{}, keep...)
	pruned := files.SnapshotPrune{Present: []string{}}
	for id := range m.taken {
		if slices.Contains(keep, id) {
			pruned.Present = append(pruned.Present, id)
			continue
		}
		delete(m.taken, id)
		pruned.Removed++
	}
	return pruned, nil
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
}

func TestSweepRemovesSnapshotsAndRecordsWithoutTheirCounterpart(t *testing.T) {
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
	backend.mu.Lock()
	delete(backend.taken, lost.ID)
	backend.taken["snapshot:orphan"] = true
	backend.mu.Unlock()

	f.volume.offline.Store(true)
	if _, err := f.files.SweepSnapshots(ctx); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("SweepSnapshots() offline error = %v, want ErrVolumeUnavailable", err)
	}
	if !backend.exists("snapshot:orphan") {
		t.Fatal("a sweep without the data volume removed a snapshot")
	}
	f.volume.offline.Store(false)

	sweep, err := f.files.SweepSnapshots(ctx)
	if err != nil {
		t.Fatalf("SweepSnapshots() error = %v", err)
	}
	if sweep.Snapshots != 1 || len(sweep.Records) != 1 || sweep.Records[0].ID != lost.ID {
		t.Fatalf("sweep = %+v, want the orphan snapshot and the lost record", sweep)
	}
	if ids := backend.ids(); !slices.Equal(ids, []string{kept.ID}) {
		t.Fatalf("snapshots after the sweep = %v, want only %s", ids, kept.ID)
	}
	if got := f.snapshots(f.shared); len(got) != 0 {
		t.Fatalf("the lost snapshot is still listed: %+v", got)
	}
	if got := f.snapshots(f.private); len(got) != 1 || got[0].ID != kept.ID {
		t.Fatalf("kept snapshots = %+v", got)
	}
	if again, err := f.files.SweepSnapshots(ctx); err != nil || again.Snapshots != 0 || len(again.Records) != 0 {
		t.Fatalf("second sweep = %+v, %v; want nothing to do", again, err)
	}
}

// A sweep while a snapshot is being taken keeps it, although the catalog
// does not record it yet.
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
	if _, err := f.files.SweepSnapshots(context.Background()); err != nil {
		t.Fatalf("SweepSnapshots() error = %v", err)
	}
	if len(taking) != 1 || !slices.Contains(backend.kept, taking[0]) || !backend.exists(taking[0]) {
		t.Fatalf("sweep kept %v while %v was being taken", backend.kept, taking)
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
// the sweep: the snapshots it did not read are not orphans (#269).
func TestSweepStopsWhenReadingTheCatalogFails(t *testing.T) {
	for _, after := range []int{0, 2} {
		t.Run(fmt.Sprintf("after %d rows", after), func(t *testing.T) {
			f := newSnapshotFixture(t)
			backend := newMemorySnapshots(folderOfFiles(1))
			f.backend = backend
			f.open()
			for _, name := range []string{"一", "二", "三"} {
				if _, err := f.files.CreateSnapshot(context.Background(), f.admin, f.private.ID, name); err != nil {
					t.Fatal(err)
				}
			}
			taken := backend.ids()
			f.driver = sqlitetest.Register("FROM snapshots ORDER BY created_at", after)
			f.open()
			if _, err := f.files.SweepSnapshots(context.Background()); !errors.Is(err, sqlitetest.ErrCut) {
				t.Fatalf("SweepSnapshots() error = %v, want ErrCut", err)
			}
			if backend.kept != nil || !slices.Equal(backend.ids(), taken) {
				t.Fatalf("the sweep pruned keeping %v: snapshots %v, were %v", backend.kept, backend.ids(), taken)
			}
		})
	}
}

// The scheduler sweeps before it takes anything, so a restart clears what a
// crash left behind.
func TestSchedulerSweepsWhenItStarts(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := newMemorySnapshots(folderOfFiles(1))
	backend.taken["snapshot:left-by-a-crash"] = true
	f.backend = backend
	f.open()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		f.files.RunSnapshotSchedules(ctx, f.accounts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for backend.exists("snapshot:left-by-a-crash") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-stopped
	if backend.exists("snapshot:left-by-a-crash") {
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

// The development backend copies spaces into directories; its sweep removes
// copies nobody records and ones a stopped Create left unfinished.
func TestDevelopmentSnapshotsAreSweptToo(t *testing.T) {
	f := newSnapshotFixture(t)
	f.open()
	ctx := context.Background()
	f.upload(f.admin, f.private, "", "notes.txt", "hello")
	snapshot, err := f.files.CreateSnapshot(ctx, f.admin, f.private.ID, "开发")
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	snapshots := filepath.Join(f.volumeRoot, ".a-nas-snapshots")
	for _, leftover := range []string{"snapshotorphan", "snapshotstopped.incoming"} {
		if err := os.MkdirAll(filepath.Join(snapshots, leftover, "docs"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sweep, err := f.files.SweepSnapshots(ctx)
	if err != nil || sweep.Snapshots != 2 || len(sweep.Records) != 0 {
		t.Fatalf("SweepSnapshots() = %+v, %v", sweep, err)
	}
	left, _ := os.ReadDir(snapshots)
	if len(left) != 1 {
		t.Fatalf("development snapshots after the sweep = %v, want only %s", left, snapshot.ID)
	}
	content, err := f.files.ListSnapshotEntries(ctx, f.admin, snapshot.ID, "")
	if err != nil || len(content) != 1 || content[0].Name != "notes.txt" || content[0].Change != "" {
		t.Fatalf("snapshot entries = %+v, %v; want notes.txt unchanged", content, err)
	}
}
