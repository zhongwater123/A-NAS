package files_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/files"
)

// recordingFileSystem records the scans a service makes. In production each
// scan is a File Broker round trip that carries every entry it found.
type recordingFileSystem struct {
	files.FileSystem
	mu    sync.Mutex
	scans []recordedScan
}

type recordedScan struct {
	path    string
	shallow bool
	entries int
}

func (r *recordingFileSystem) Scan(ctx context.Context, path string, skip files.ScanSkip) ([]files.ScanEntry, error) {
	entries, err := r.FileSystem.Scan(ctx, path, skip)
	r.mu.Lock()
	r.scans = append(r.scans, recordedScan{path: path, shallow: skip.Shallow, entries: len(entries)})
	r.mu.Unlock()
	return entries, err
}

// take returns the scans recorded since the last call.
func (r *recordingFileSystem) take() []recordedScan {
	r.mu.Lock()
	defer r.mu.Unlock()
	scans := r.scans
	r.scans = nil
	return scans
}

type scopeFixture struct {
	*raceFixture
	recorder *recordingFileSystem
	// writes counts the rows of entries written so far.
	writes func() int
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	recorder := &recordingFileSystem{}
	f := newRaceFixtureOn(t, func(fsys files.FileSystem) files.FileSystem {
		recorder.FileSystem = fsys
		return recorder
	})
	return &scopeFixture{raceFixture: f, recorder: recorder, writes: countEntryWrites(t, f.db)}
}

// countEntryWrites counts every row inserted, updated or deleted in
// entries, through triggers that the service's own connection runs too.
func countEntryWrites(t *testing.T, db *sql.DB) func() int {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE test_writes(n INTEGER NOT NULL);
INSERT INTO test_writes VALUES(0);
CREATE TRIGGER test_entries_inserted AFTER INSERT ON entries BEGIN UPDATE test_writes SET n = n + 1; END;
CREATE TRIGGER test_entries_updated AFTER UPDATE ON entries BEGIN UPDATE test_writes SET n = n + 1; END;
CREATE TRIGGER test_entries_deleted AFTER DELETE ON entries BEGIN UPDATE test_writes SET n = n + 1; END;`); err != nil {
		t.Fatal(err)
	}
	return func() int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT n FROM test_writes").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
}

// smb changes the space the way an SMB client does, without A-NAS.
func (f *scopeFixture) smb(relative, content string) {
	f.t.Helper()
	full := filepath.Join(f.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *scopeFixture) smbRename(from, to string) {
	f.t.Helper()
	if err := os.Rename(filepath.Join(f.root, filepath.FromSlash(from)), filepath.Join(f.root, filepath.FromSlash(to))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *scopeFixture) tree(directoryID string) map[string]string {
	f.t.Helper()
	entries, err := f.service.Tree(f.ctx, f.owner, f.space.ID, directoryID)
	if err != nil {
		f.t.Fatalf("Tree(%q) error = %v", directoryID, err)
	}
	ids := map[string]string{}
	for _, entry := range entries {
		ids[entry.Path] = entry.ID
	}
	return ids
}

// wholeScans counts the scans that covered the whole space.
func (f *scopeFixture) wholeScans(scans []recordedScan) int {
	count := 0
	for _, scan := range scans {
		if scan.path == f.root && !scan.shallow {
			count++
		}
	}
	return count
}

// Listing a folder scans and writes what the folder holds, not what the
// space holds (#167).
func TestListingReconcilesOnlyTheListedFolder(t *testing.T) {
	f := newScopeFixture(t)
	for index := range 300 {
		f.smb(fmt.Sprintf("照片/%03d.jpg", index), "photo")
	}
	f.smb("文档/计划.txt", "plan")
	f.smb("文档/预算.txt", "budget")
	f.tree("")
	documents := f.ids("")["文档"]
	f.recorder.take()
	before := f.writes()

	if got := f.ids(documents); len(got) != 2 {
		t.Fatalf("文档 lists %v, want 2 files", got)
	}
	scans := f.recorder.take()
	if len(scans) != 1 || scans[0].path != filepath.Join(f.root, "文档") || !scans[0].shallow || scans[0].entries != 2 {
		t.Fatalf("listing 文档 scanned %+v, want its 2 children only", scans)
	}
	if got := f.ids(""); len(got) != 2 {
		t.Fatalf("root lists %v, want 照片 and 文档", got)
	}
	if scans := f.recorder.take(); len(scans) != 1 || scans[0].path != f.root || !scans[0].shallow || scans[0].entries != 2 {
		t.Fatalf("listing the root scanned %+v, want its 2 folders only", scans)
	}
	if written := f.writes() - before; written != 0 {
		t.Fatalf("listing unchanged folders wrote %d rows, want 0", written)
	}
}

// A folder renamed over SMB keeps its ID and those of everything below it,
// and what is below opens at once, without a pass over the whole space.
func TestFolderRenamedOverSMBTakesItsContentsAlong(t *testing.T) {
	f := newScopeFixture(t)
	folder, plan, sub, budget := f.project()
	f.smbRename("项目", "工程")
	if got := f.ids(""); got["工程"] != folder.ID {
		t.Fatalf("root lists %v, want 工程=%s", got, folder.ID)
	}
	f.recorder.take()
	if got := f.ids(folder.ID); got["计划.txt"] != plan.ID || got["资料"] != sub.ID {
		t.Fatalf("工程 lists %v, want 计划.txt=%s and 资料=%s", got, plan.ID, sub.ID)
	}
	content, err := f.service.OpenContent(f.ctx, f.owner, budget.ID)
	if err != nil {
		t.Fatalf("OpenContent(预算) error = %v", err)
	}
	_ = content.Reader.Close()
	if whole := f.wholeScans(f.recorder.take()); whole != 0 {
		t.Fatalf("opening the renamed folder took %d passes over the whole space, want 0", whole)
	}
	f.checkProject(folder.ID, plan, sub, budget)
}

// A file moved between folders over SMB keeps its ID, even when the folder
// it left is listed before the one it went to.
func TestFileMovedOverSMBKeepsItsID(t *testing.T) {
	f := newScopeFixture(t)
	inbox := f.mkdir("", "收件箱")
	archive := f.mkdir("", "归档")
	contract := f.upload(inbox.ID, "合同.pdf", "contract")
	f.smbRename("收件箱/合同.pdf", "归档/合同.pdf")
	if _, listed := f.ids(inbox.ID)["合同.pdf"]; listed {
		t.Fatal("收件箱 still lists the moved file")
	}
	if got := f.ids(archive.ID)["合同.pdf"]; got != contract.ID {
		t.Fatalf("归档 lists 合同.pdf as %q, want %s", got, contract.ID)
	}
	if got := f.tree(""); got["归档/合同.pdf"] != contract.ID {
		t.Fatalf("whole tree %v, want 归档/合同.pdf=%s", got, contract.ID)
	}
}

// A folder moved over SMB is found when it is opened by its ID, before
// anyone lists where it went.
func TestFolderMovedOverSMBIsFoundWhenOpened(t *testing.T) {
	f := newScopeFixture(t)
	from := f.mkdir("", "甲")
	to := f.mkdir("", "乙")
	album := f.mkdir(from.ID, "相册")
	photo := f.upload(album.ID, "1.jpg", "photo")
	f.smbRename("甲/相册", "乙/相册")
	f.recorder.take()
	if got := f.ids(album.ID); got["1.jpg"] != photo.ID {
		t.Fatalf("相册 lists %v, want 1.jpg=%s", got, photo.ID)
	}
	if whole := f.wholeScans(f.recorder.take()); whole != 1 {
		t.Fatalf("finding the moved folder took %d passes over the whole space, want 1", whole)
	}
	if got := f.ids(to.ID); got["相册"] != album.ID {
		t.Fatalf("乙 lists %v, want 相册=%s", got, album.ID)
	}
	if got := f.tree(to.ID); got["相册/1.jpg"] != photo.ID {
		t.Fatalf("乙's tree %v, want 相册/1.jpg=%s", got, photo.ID)
	}
}

// A folder moved over SMB that is missed in the folder it left and then
// found where it went brings its contents along.
func TestFolderMovedOverSMBFollowsAcrossListings(t *testing.T) {
	f := newScopeFixture(t)
	from := f.mkdir("", "甲")
	to := f.mkdir("", "乙")
	album := f.mkdir(from.ID, "相册")
	trip := f.mkdir(album.ID, "旅行")
	photo := f.upload(trip.ID, "1.jpg", "photo")
	f.smbRename("甲/相册", "乙/相册")
	if got := f.ids(from.ID); len(got) != 0 {
		t.Fatalf("甲 lists %v, want nothing", got)
	}
	if got := f.ids(to.ID); got["相册"] != album.ID {
		t.Fatalf("乙 lists %v, want 相册=%s", got, album.ID)
	}
	f.recorder.take()
	if got := f.ids(trip.ID); got["1.jpg"] != photo.ID {
		t.Fatalf("旅行 lists %v, want 1.jpg=%s", got, photo.ID)
	}
	content, err := f.service.OpenContent(f.ctx, f.owner, photo.ID)
	if err != nil {
		t.Fatalf("OpenContent(1.jpg) error = %v", err)
	}
	_ = content.Reader.Close()
	if whole := f.wholeScans(f.recorder.take()); whole != 0 {
		t.Fatalf("following the moved folder took %d passes over the whole space, want 0", whole)
	}
}

// A folder that another one replaced under the same name keeps its rows
// apart from the new folder's.
func TestFolderReplacedOverSMBKeepsItsContentsApart(t *testing.T) {
	f := newScopeFixture(t)
	folder, plan, sub, budget := f.project()
	f.smbRename("项目", "旧项目")
	f.smb("项目/计划.txt", "new plan")
	f.smb("项目/资料/预算.txt", "new budget")
	root := f.ids("")
	if root["旧项目"] != folder.ID || root["项目"] == "" || root["项目"] == folder.ID {
		t.Fatalf("root lists %v, want 旧项目=%s and a new 项目", root, folder.ID)
	}
	fresh := f.ids(root["项目"])
	if fresh["计划.txt"] == "" || fresh["计划.txt"] == plan.ID || fresh["资料"] == sub.ID {
		t.Fatalf("new 项目 lists %v, want new IDs", fresh)
	}
	f.checkProject(folder.ID, plan, sub, budget)
	if got := f.tree(root["项目"]); got["资料/预算.txt"] == "" || got["资料/预算.txt"] == budget.ID {
		t.Fatalf("new 项目's tree %v, want a new 资料/预算.txt", got)
	}
}

// A file deleted over SMB leaves the listing at once; its row goes in the
// next pass over the whole space, and meanwhile neither opens nor blocks its
// name.
func TestFileDeletedOverSMBIsDroppedByAWholePass(t *testing.T) {
	f := newScopeFixture(t)
	folder := f.mkdir("", "文档")
	readme := f.upload(folder.ID, "readme.txt", "old")
	if err := os.Remove(filepath.Join(f.root, "文档", "readme.txt")); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(folder.ID); len(got) != 0 {
		t.Fatalf("文档 lists %v, want nothing", got)
	}
	if _, _, err := f.service.Lookup(f.ctx, f.owner, readme.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Lookup(deleted file) error = %v, want ErrNotFound", err)
	}
	if _, err := f.service.Upload(f.ctx, f.owner, f.space.ID, folder.ID, "README.txt", bytes.NewBufferString("new")); err != nil {
		t.Fatalf("Upload(README.txt) beside the deleted readme.txt error = %v", err)
	}
	if err := f.service.ReconcileVisibleSpaces(f.ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM entries WHERE id = ?", readme.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the pass over the whole space kept the deleted file's row")
	}
}

// A file A-NAS creates never takes the ID of a row whose file went away,
// even when the file system gives it that file's inode number again (ext4
// does so at once; on CI the test above uploaded into the deleted file's
// row).
func TestANewFileDoesNotTakeTheIDOfAGoneOne(t *testing.T) {
	f := newScopeFixture(t)
	folder := f.mkdir("", "文档")
	gone := f.upload(folder.ID, "旧.txt", "old")
	if err := os.Remove(filepath.Join(f.root, "文档", "旧.txt")); err != nil {
		t.Fatal(err)
	}
	if got := f.ids(folder.ID); len(got) != 0 {
		t.Fatalf("文档 lists %v, want nothing", got)
	}
	// The new file gets the gone file's inode number.
	f.fs.hooks(nil, func(_, to string) {
		if filepath.Base(to) == "新.txt" {
			if _, err := f.db.Exec("UPDATE entries SET inode = ? WHERE id = ?", inodeOf(t, to), gone.ID); err != nil {
				t.Error(err)
			}
		}
	})
	created := f.upload(folder.ID, "新.txt", "new")
	f.fs.hooks(nil, nil)
	if created.ID == gone.ID {
		t.Fatalf("the uploaded file took the ID %s of the file deleted over SMB", gone.ID)
	}
	var rows int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM entries WHERE id = ?", gone.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the gone file's row is still there")
	}
}

// A tree below a folder scans that folder, and writes only what changed.
func TestTreeReconcilesOnlyTheFolderBelow(t *testing.T) {
	f := newScopeFixture(t)
	for index := range 200 {
		f.smb(fmt.Sprintf("照片/%03d.jpg", index), "photo")
	}
	f.smb("影视/电影/甲.mkv", "a")
	f.smb("影视/电影/甲.nfo", "a")
	f.tree("")
	library := f.ids("")["影视"]
	f.smb("影视/电影/乙.mkv", "b")
	f.recorder.take()
	before := f.writes()
	if got := f.tree(library); got["电影/乙.mkv"] == "" || len(got) != 4 {
		t.Fatalf("影视's tree %v, want 电影 and its 3 files", got)
	}
	scans := f.recorder.take()
	if len(scans) != 1 || scans[0].path != filepath.Join(f.root, "影视") || scans[0].shallow || scans[0].entries != 4 {
		t.Fatalf("Tree(影视) scanned %+v, want 影视's 4 entries", scans)
	}
	// The new file and its folder's modification time.
	if written := f.writes() - before; written != 2 {
		t.Fatalf("Tree(影视) wrote %d rows, want 2", written)
	}
}

// Listing folders that did not change while a pass over the whole space
// scans does not discard that pass: on a busy space it would never be
// written, and rows of deleted files would never go.
func TestListingDuringAWholePassLetsItBeWritten(t *testing.T) {
	f := newScopeFixture(t)
	documents := f.mkdir("", "文档")
	other := f.mkdir("", "其他")
	f.upload(other.ID, "不变.txt", "same")
	gone := f.upload(documents.ID, "已删.txt", "gone")
	if err := os.Remove(filepath.Join(f.root, "文档", "已删.txt")); err != nil {
		t.Fatal(err)
	}
	held := newPause()
	f.fs.hooks(func(path string) {
		if path == f.root {
			held.hold()
		}
	}, nil)
	defer f.fs.hooks(nil, nil)
	defer held.let()
	passed := make(chan error, 1)
	go func() {
		_, err := f.service.Tree(f.ctx, f.owner, f.space.ID, "")
		passed <- err
	}()
	<-held.reached
	for range 3 {
		if got := f.ids(other.ID); len(got) != 1 {
			t.Fatalf("其他 lists %v during the pass", got)
		}
	}
	held.let()
	if err := <-passed; err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	var rows int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM entries WHERE id = ?", gone.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the pass over the whole space was discarded: the deleted file's row is still there")
	}
}

// Passes over the whole space run one at a time, and callers that waited
// for one share the next; the background loop skips a space just passed.
func TestPassesOverTheWholeSpaceAreShared(t *testing.T) {
	f := newScopeFixture(t)
	f.smb("a.txt", "a")
	held := newPause()
	f.fs.hooks(func(path string) {
		if path == f.root {
			held.hold()
		}
	}, nil)
	defer f.fs.hooks(nil, nil)
	defer held.let()
	var callers sync.WaitGroup
	tree := func() {
		defer callers.Done()
		if _, err := f.service.Tree(f.ctx, f.owner, f.space.ID, ""); err != nil {
			t.Errorf("Tree() error = %v", err)
		}
	}
	callers.Add(1)
	go tree()
	<-held.reached
	for range 3 {
		callers.Add(1)
		go tree()
	}
	// Let the three wait for the held pass.
	time.Sleep(200 * time.Millisecond)
	held.let()
	callers.Wait()
	if whole := f.wholeScans(f.recorder.take()); whole != 2 {
		t.Fatalf("4 callers took %d passes over the whole space, want 2", whole)
	}
	if err := f.service.ReconcileVisibleSpaces(f.ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	if whole := f.wholeScans(f.recorder.take()); whole != 0 {
		t.Fatalf("the background loop passed over a space just passed over %d times", whole)
	}
}
