package files_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// hookedFileSystem lets a test stop an operation half way: after a scan has
// read the space, or after a rename has moved files but before their rows
// are written.
type hookedFileSystem struct {
	files.FileSystem
	mu          sync.Mutex
	afterScan   func(path string)
	afterRename func(from, to string)
}

func (h *hookedFileSystem) Scan(ctx context.Context, path string, skip files.ScanSkip) ([]files.ScanEntry, error) {
	entries, err := h.FileSystem.Scan(ctx, path, skip)
	h.mu.Lock()
	hook := h.afterScan
	h.mu.Unlock()
	if hook != nil {
		hook(path)
	}
	return entries, err
}

func (h *hookedFileSystem) Rename(ctx context.Context, from, to string) error {
	err := h.FileSystem.Rename(ctx, from, to)
	h.mu.Lock()
	hook := h.afterRename
	h.mu.Unlock()
	if err == nil && hook != nil {
		hook(from, to)
	}
	return err
}

func (h *hookedFileSystem) hooks(afterScan func(string), afterRename func(string, string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterScan, h.afterRename = afterScan, afterRename
}

// pause holds the first call that reaches it until let.
type pause struct {
	taken   atomic.Bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPause() *pause { return &pause{reached: make(chan struct{}), release: make(chan struct{})} }

func (p *pause) hold() {
	if p.taken.CompareAndSwap(false, true) {
		close(p.reached)
		<-p.release
	}
}

func (p *pause) let() { p.once.Do(func() { close(p.release) }) }

type raceFixture struct {
	t       *testing.T
	ctx     context.Context
	service *files.Service
	owner   accounts.User
	space   accounts.Space
	root    string
	fs      *hookedFileSystem
	db      *sql.DB
}

func newRaceFixture(t *testing.T) *raceFixture {
	t.Helper()
	return newRaceFixtureOn(t, func(fsys files.FileSystem) files.FileSystem { return fsys })
}

// newRaceFixtureOn is newRaceFixture with wrap between the hooks and the
// volume.
func newRaceFixtureOn(t *testing.T, wrap func(files.FileSystem) files.FileSystem) *raceFixture {
	t.Helper()
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	owner, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(t.TempDir(), "files.db")
	catalog, err := files.OpenSQLite(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(t.TempDir(), "volume")
	fixture := &raceFixture{
		t: t, ctx: ctx, owner: owner, space: findPrivateSpace(t, mustSpaces(t, accountService, owner)),
		root: filepath.Join(volume, "spaces", "private", "owner"),
		fs:   &hookedFileSystem{FileSystem: wrap(files.NewRootFileSystem(volume, true))},
		db:   catalogDB(t, catalogPath),
	}
	fixture.service = files.NewService(catalog, volume, accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, FileSystem: fixture.fs,
	})
	return fixture
}

func (f *raceFixture) mkdir(parentID, name string) files.Entry {
	f.t.Helper()
	entry, err := f.service.CreateDirectory(f.ctx, f.owner, f.space.ID, parentID, name)
	if err != nil {
		f.t.Fatalf("CreateDirectory(%s) error = %v", name, err)
	}
	return entry
}

func (f *raceFixture) upload(parentID, name, content string) files.Entry {
	f.t.Helper()
	entry, err := f.service.Upload(f.ctx, f.owner, f.space.ID, parentID, name, bytes.NewBufferString(content))
	if err != nil {
		f.t.Fatalf("Upload(%s) error = %v", name, err)
	}
	return entry
}

// ids lists a folder as name → ID.
func (f *raceFixture) ids(parentID string) map[string]string {
	f.t.Helper()
	entries, err := f.service.List(f.ctx, f.owner, f.space.ID, parentID)
	if err != nil {
		f.t.Fatalf("List(%q) error = %v", parentID, err)
	}
	ids := map[string]string{}
	for _, entry := range entries {
		ids[entry.Name] = entry.ID
	}
	return ids
}

// project creates 项目/{计划.txt, 资料/预算.txt}.
func (f *raceFixture) project() (folder, plan, sub, budget files.Entry) {
	f.t.Helper()
	folder = f.mkdir("", "项目")
	plan = f.upload(folder.ID, "计划.txt", "plan")
	sub = f.mkdir(folder.ID, "资料")
	budget = f.upload(sub.ID, "预算.txt", "budget")
	return folder, plan, sub, budget
}

// whileScanned lists the space root, holding the listing's reconciliation
// after its scan while change runs.
func (f *raceFixture) whileScanned(change func()) {
	f.t.Helper()
	held := newPause()
	f.fs.hooks(func(path string) {
		if path == f.root {
			held.hold()
		}
	}, nil)
	defer f.fs.hooks(nil, nil)
	defer held.let()
	listed := make(chan error, 1)
	go func() {
		_, err := f.service.List(f.ctx, f.owner, f.space.ID, "")
		listed <- err
	}()
	<-held.reached
	change()
	held.let()
	if err := <-listed; err != nil {
		f.t.Fatalf("List() during the change error = %v", err)
	}
}

// deleteHeld deletes an entry, holding the deletion after it moved the entry
// into the recycle bin and before it recorded that, while during runs.
func (f *raceFixture) deleteHeld(entryID string, during func()) files.TrashItem {
	f.t.Helper()
	held := newPause()
	f.fs.hooks(nil, func(_, to string) {
		if strings.Contains(to, ".a-nas-trash") {
			held.hold()
		}
	})
	defer f.fs.hooks(nil, nil)
	defer held.let()
	type result struct {
		item files.TrashItem
		err  error
	}
	deleted := make(chan result, 1)
	go func() {
		item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, entryID)
		deleted <- result{item, err}
	}()
	<-held.reached
	during()
	held.let()
	outcome := <-deleted
	if outcome.err != nil {
		f.t.Fatalf("Delete() error = %v", outcome.err)
	}
	return outcome.item
}

func (f *raceFixture) restore(item files.TrashItem, conflict files.ConflictPolicy) files.Restored {
	f.t.Helper()
	restored, err := f.service.Restore(f.ctx, f.owner, item.ID, "", "", conflict)
	if err != nil {
		f.t.Fatalf("Restore(%s) error = %v", item.Name, err)
	}
	return restored
}

// checkProject checks that a restored project kept the IDs of its contents.
func (f *raceFixture) checkProject(folderID string, plan, sub, budget files.Entry) {
	f.t.Helper()
	f.ids("") // reconcile, so that rows the restore left for it are placed
	if got := f.ids(folderID); got["计划.txt"] != plan.ID || got["资料"] != sub.ID || len(got) != 2 {
		f.t.Fatalf("restored folder lists %v, want 计划.txt=%s and 资料=%s", got, plan.ID, sub.ID)
	}
	if got := f.ids(sub.ID); got["预算.txt"] != budget.ID || len(got) != 1 {
		f.t.Fatalf("restored subfolder lists %v, want 预算.txt=%s", got, budget.ID)
	}
	content, err := f.service.OpenContent(f.ctx, f.owner, budget.ID)
	if err != nil {
		f.t.Fatalf("OpenContent(预算.txt) error = %v", err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "budget" {
		f.t.Fatalf("restored 预算.txt = %q", data)
	}
}

// The failure #164 reported: a listing scans the space, the folder is deleted,
// and the listing's stale scan then wrote the folder's original paths into
// its recycle-bin rows, so restoring it failed on every try.
func TestFolderDeletedWhileTheSpaceIsReconciledCanBeRestored(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	var item files.TrashItem
	f.whileScanned(func() {
		var err error
		if item, err = f.service.Delete(f.ctx, f.owner, f.space.ID, folder.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	})
	if _, listed := f.ids("")["项目"]; listed {
		t.Fatal("the deleted folder is still listed")
	}
	restored := f.restore(item, files.ConflictAsk)
	if restored.ID != folder.ID || restored.Name != "项目" {
		t.Fatalf("Restore() = %+v, want 项目 with ID %s", restored, folder.ID)
	}
	f.checkProject(folder.ID, plan, sub, budget)
}

// A reconciliation that scans after a deletion moved the folder, but before
// the deletion recorded it, must not drop the folder's rows as missing.
func TestReconciliationDuringADeletionKeepsTheFolderRows(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	item := f.deleteHeld(folder.ID, func() {
		if _, err := f.service.List(f.ctx, f.owner, f.space.ID, ""); err != nil {
			t.Fatalf("List() during the deletion error = %v", err)
		}
	})
	if item.Kind != files.EntryKindDirectory || item.SizeBytes != int64(len("plan")+len("budget")) {
		t.Fatalf("trash item = %+v, want the folder with both files", item)
	}
	restored := f.restore(item, files.ConflictAsk)
	if restored.ID != folder.ID {
		t.Fatalf("Restore() ID = %s, want %s", restored.ID, folder.ID)
	}
	f.checkProject(folder.ID, plan, sub, budget)
}

// Samba recycle import scans the user's recycle directory: a folder that a
// Web deletion just moved there is not yet a recorded item, and its files
// must not be imported one by one as if SMB had deleted them.
func TestSambaRecycleImportLeavesAFolderBeingDeletedAlone(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	f.deleteHeld(folder.ID, func() {
		if _, err := f.service.ListTrash(f.ctx, f.owner); err != nil {
			t.Fatalf("ListTrash() during the deletion error = %v", err)
		}
	})
	items, err := f.service.ListTrash(f.ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].EntryID != folder.ID {
		t.Fatalf("ListTrash() = %+v, want only the folder", items)
	}
	f.restore(items[0], files.ConflictAsk)
	f.checkProject(folder.ID, plan, sub, budget)
}

// Entries created or moved after a reconciliation's scan keep their IDs and
// places: the stale scan neither drops nor moves them back.
func TestEntriesChangedDuringReconciliationKeepTheirIDs(t *testing.T) {
	f := newRaceFixture(t)
	target := f.mkdir("", "归档")
	moving := f.upload("", "会议.txt", "minutes")
	var created, uploaded files.Entry
	f.whileScanned(func() {
		created = f.mkdir("", "新建")
		uploaded = f.upload("", "新.txt", "new")
		if _, err := f.service.Move(f.ctx, f.owner, moving.ID, "", target.ID, ""); err != nil {
			t.Fatalf("Move() error = %v", err)
		}
	})
	content, err := f.service.OpenContent(f.ctx, f.owner, moving.ID)
	if err != nil {
		t.Fatalf("OpenContent(moved file) error = %v", err)
	}
	_ = content.Reader.Close()
	for range 2 {
		ids := f.ids("")
		if ids["新建"] != created.ID || ids["新.txt"] != uploaded.ID {
			t.Fatalf("root lists %v, want 新建=%s and 新.txt=%s", ids, created.ID, uploaded.ID)
		}
		if _, listed := ids["会议.txt"]; listed {
			t.Fatalf("root still lists the moved file: %v", ids)
		}
	}
	if got := f.ids(target.ID); got["会议.txt"] != moving.ID {
		t.Fatalf("归档 lists %v, want 会议.txt=%s", got, moving.ID)
	}
}

// Deleting and restoring a folder while other requests keep reconciling the
// space always succeeds and keeps every ID.
func TestDeleteAndRestoreWhileReconcilingAlwaysSucceed(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	stop := make(chan struct{})
	var background sync.WaitGroup
	var failure atomic.Pointer[error]
	for _, reconcile := range []func() error{
		func() error { _, err := f.service.List(f.ctx, f.owner, f.space.ID, ""); return err },
		func() error { _, err := f.service.ListTrash(f.ctx, f.owner); return err },
		func() error { return f.service.ReconcileVisibleSpaces(f.ctx, f.owner) },
		func() error { _, err := f.service.Tree(f.ctx, f.owner, f.space.ID, ""); return err },
	} {
		background.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := reconcile(); err != nil {
					failure.CompareAndSwap(nil, &err)
				}
			}
		})
	}
	var stopping sync.Once
	halt := func() {
		stopping.Do(func() {
			close(stop)
			background.Wait()
		})
	}
	defer halt()
	for round := range 25 {
		item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, folder.ID)
		if err != nil {
			t.Fatalf("round %d: Delete() error = %v", round, err)
		}
		restored, err := f.service.Restore(f.ctx, f.owner, item.ID, "", "", files.ConflictAsk)
		if err != nil {
			t.Fatalf("round %d: Restore() error = %v", round, err)
		}
		if restored.ID != folder.ID {
			t.Fatalf("round %d: restored ID = %s, want %s", round, restored.ID, folder.ID)
		}
	}
	halt()
	if err := failure.Load(); err != nil {
		t.Fatalf("reconciliation failed: %v", *err)
	}
	f.checkProject(folder.ID, plan, sub, budget)
}

// A row whose path now holds another file does not open that file.
func TestOpeningAnEntryChecksItIsStillTheSameFile(t *testing.T) {
	f := newRaceFixture(t)
	original := f.upload("", "报告.txt", "first")
	replacement := filepath.Join(f.root, "报告.txt.tmp")
	if err := os.WriteFile(replacement, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(f.root, "报告.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.OpenContent(f.ctx, f.owner, original.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("OpenContent(replaced file's old ID) error = %v, want ErrNotFound", err)
	}
	current := f.ids("")["报告.txt"]
	if current == "" || current == original.ID {
		t.Fatalf("报告 after reconciliation has ID %q, want a new one", current)
	}
	content, err := f.service.OpenContent(f.ctx, f.owner, current)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "second" {
		t.Fatalf("content = %q, want the replacement", data)
	}
}

// breakTrashPaths rewrites a recycle-bin item's rows back to the paths they
// had in the space, as a stale scan of an earlier version did.
func (f *raceFixture) breakTrashPaths(paths map[string]string) {
	f.t.Helper()
	for id, path := range paths {
		if _, err := f.db.Exec("UPDATE entries SET relative_path = ? WHERE id = ? AND trashed = 1", path, id); err != nil {
			f.t.Fatal(err)
		}
	}
}

// Catalogs that an earlier version's stale scan already damaged: restoring
// the folder works and its contents keep their IDs.
func TestRestoreRepairsRowsAStaleScanRewrote(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.breakTrashPaths(map[string]string{
		folder.ID: "项目", plan.ID: "项目/计划.txt", sub.ID: "项目/资料", budget.ID: "项目/资料/预算.txt",
	})
	f.restore(item, files.ConflictAsk)
	f.checkProject(folder.ID, plan, sub, budget)
}

// A damaged recycle-bin row that holds a path of the space no longer blocks
// new entries there, and the item still comes back beside them.
func TestDamagedRecycleBinRowsDoNotBlockTheirOldPaths(t *testing.T) {
	f := newRaceFixture(t)
	folder, plan, sub, budget := f.project()
	item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.breakTrashPaths(map[string]string{
		folder.ID: "项目", plan.ID: "项目/计划.txt", sub.ID: "项目/资料", budget.ID: "项目/资料/预算.txt",
	})
	again := f.mkdir("", "项目")
	f.upload(again.ID, "计划.txt", "new plan")
	restored := f.restore(item, files.ConflictRename)
	if restored.ID != folder.ID || restored.Name != "项目 (恢复)" {
		t.Fatalf("Restore() = %+v, want 项目 (恢复) with ID %s", restored, folder.ID)
	}
	f.checkProject(folder.ID, plan, sub, budget)
	if got := f.ids(again.ID); len(got) != 1 || got["计划.txt"] == "" || got["计划.txt"] == plan.ID {
		t.Fatalf("new 项目 lists %v, want its own 计划.txt", got)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

// A recycle-bin row whose file is gone from the recycle bin no longer claims
// the inode, so a file that now has it is listed.
func TestAStaleRecycleBinRowDoesNotHideALiveFile(t *testing.T) {
	f := newRaceFixture(t)
	live := f.upload("", "现在.txt", "live")
	gone := f.upload("", "旧.txt", "old")
	item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	var trashRelative string
	if err := f.db.QueryRow("SELECT trash_relative_path FROM trash WHERE id = ?", item.ID).Scan(&trashRelative); err != nil {
		t.Fatal(err)
	}
	// The recycled file disappears outside A-NAS and its inode number is
	// taken by the live file, which the catalog has not seen yet.
	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(trashRelative))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM entries WHERE id = ?", live.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE entries SET inode = ? WHERE id = ?", inodeOf(t, filepath.Join(f.root, "现在.txt")), gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, listed := f.ids("")["现在.txt"]; !listed {
		t.Fatal("the live file is hidden by the stale recycle-bin row")
	}
}

// A recycle-bin row whose file the recycle bin still holds keeps it, even
// when the same file also appears in the space.
func TestARecycleBinRowKeepsAFileTheBinStillHolds(t *testing.T) {
	f := newRaceFixture(t)
	kept := f.upload("", "保留.txt", "kept")
	item, err := f.service.Delete(f.ctx, f.owner, f.space.ID, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	var trashRelative string
	if err := f.db.QueryRow("SELECT trash_relative_path FROM trash WHERE id = ?", item.ID).Scan(&trashRelative); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(f.root, filepath.FromSlash(trashRelative)), filepath.Join(f.root, "链接.txt")); err != nil {
		t.Fatal(err)
	}
	f.ids("")
	items, err := f.service.ListTrash(f.ctx, f.owner)
	if err != nil || len(items) != 1 || items[0].EntryID != kept.ID || items[0].SizeBytes != int64(len("kept")) {
		t.Fatalf("ListTrash() = %+v, %v; want the deleted file", items, err)
	}
}
