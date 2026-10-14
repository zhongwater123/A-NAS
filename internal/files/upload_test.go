package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

type uploadFixture struct {
	ctx     context.Context
	service *files.Service
	alice   accounts.User
	space   accounts.Space
	root    string
	now     time.Time
}

func newUploadFixture(t *testing.T) *uploadFixture {
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
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	spaces, err := accountService.ListSpaces(ctx, alice)
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	fixture := &uploadFixture{ctx: ctx, alice: alice, space: findPrivateSpace(t, spaces), now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	volume := filepath.Join(t.TempDir(), "volume")
	fixture.root = filepath.Join(volume, "spaces", "private", "alice")
	fixture.service = files.NewService(catalog, volume, accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, Now: func() time.Time { return fixture.now },
	})
	return fixture
}

func (f *uploadFixture) begin(t *testing.T, request files.UploadRequest) *files.PendingUpload {
	t.Helper()
	request.SpaceID = f.space.ID
	if request.Conflict == "" {
		request.Conflict = files.ConflictAsk
	}
	upload, err := f.service.BeginUpload(f.ctx, f.alice, request)
	if err != nil {
		t.Fatalf("BeginUpload(%s) error = %v", request.Name, err)
	}
	return upload
}

// send uploads content in two parts and saves it.
func (f *uploadFixture) send(t *testing.T, request files.UploadRequest, content string) files.CommittedUpload {
	t.Helper()
	request.Size = int64(len(content))
	upload := f.begin(t, request)
	half := len(content) / 2
	for _, part := range []string{content[:half], content[half:]} {
		if _, err := f.service.WriteUpload(f.ctx, upload, upload.Offset(), strings.NewReader(part)); err != nil {
			t.Fatalf("WriteUpload(%s) error = %v", request.Name, err)
		}
	}
	committed, err := f.service.CommitUpload(f.ctx, f.alice, upload)
	if err != nil {
		t.Fatalf("CommitUpload(%s) error = %v", request.Name, err)
	}
	return committed
}

func (f *uploadFixture) names(t *testing.T, parentID string) []string {
	t.Helper()
	entries, err := f.service.List(f.ctx, f.alice, f.space.ID, parentID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	slices.Sort(names)
	return names
}

func TestUploadInPartsResumesFromItsOffset(t *testing.T) {
	f := newUploadFixture(t)
	upload := f.begin(t, files.UploadRequest{Name: "报告.txt", Size: 11})
	// A dropped connection delivers only part of the bytes it carried.
	written, err := f.service.WriteUpload(f.ctx, upload, 0, io.MultiReader(strings.NewReader("hello"), failingReader{}))
	if err == nil || written != 5 || upload.Offset() != 5 {
		t.Fatalf("interrupted part wrote %d (offset %d), err = %v; want 5 and the read error", written, upload.Offset(), err)
	}
	if _, err := f.service.WriteUpload(f.ctx, upload, 0, strings.NewReader("hello")); !errors.Is(err, files.ErrUploadOffset) {
		t.Fatalf("part that restarts at 0 error = %v, want ErrUploadOffset", err)
	}
	if _, err := f.service.CommitUpload(f.ctx, f.alice, upload); !errors.Is(err, files.ErrUploadIncomplete) {
		t.Fatalf("early commit error = %v, want ErrUploadIncomplete", err)
	}
	if got := f.names(t, ""); len(got) != 0 {
		t.Fatalf("an unfinished upload is listed: %v", got)
	}
	// Bytes past the declared size are not read.
	if _, err := f.service.WriteUpload(f.ctx, upload, 5, strings.NewReader(" A-NAS and more")); err != nil {
		t.Fatalf("resumed part error = %v", err)
	}
	committed, err := f.service.CommitUpload(f.ctx, f.alice, upload)
	if err != nil {
		t.Fatalf("CommitUpload() error = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(f.root, "报告.txt")); err != nil || string(content) != "hello A-NAS" {
		t.Fatalf("saved content = %q, %v", content, err)
	}
	if committed.Entry.Name != "报告.txt" || committed.Entry.SizeBytes != 11 || committed.Replaced {
		t.Fatalf("committed = %#v", committed)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-upload-*")); len(leftovers) != 0 {
		t.Fatalf("temporaries left after commit: %v", leftovers)
	}
}

func TestUploadConflictPolicies(t *testing.T) {
	f := newUploadFixture(t)
	f.send(t, files.UploadRequest{Name: "photo.jpg"}, "first")

	if _, err := f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: f.space.ID, Name: "photo.jpg", Size: 3, Conflict: files.ConflictAsk}); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("ask with a taken name error = %v, want ErrConflict before any byte", err)
	}
	if got := f.send(t, files.UploadRequest{Name: "photo.jpg", Conflict: files.ConflictRename}, "second"); got.Entry.Name != "photo (1).jpg" {
		t.Fatalf("rename saved %q, want photo (1).jpg", got.Entry.Name)
	}
	replaced := f.send(t, files.UploadRequest{Name: "photo.jpg", Conflict: files.ConflictReplace}, "third")
	if !replaced.Replaced || replaced.Entry.Name != "photo.jpg" {
		t.Fatalf("replace = %#v", replaced)
	}
	if content, _ := os.ReadFile(filepath.Join(f.root, "photo.jpg")); string(content) != "third" {
		t.Fatalf("replaced content = %q", content)
	}
	trash, err := f.service.ListTrash(f.ctx, f.alice)
	if err != nil || len(trash) != 1 || trash[0].Name != "photo.jpg" {
		t.Fatalf("replaced file is not in the trash: %#v, %v", trash, err)
	}

	// A name taken while the bytes arrive keeps both instead of failing.
	late := f.begin(t, files.UploadRequest{Name: "notes.txt", Size: 4})
	f.send(t, files.UploadRequest{Name: "notes.txt"}, "else")
	if _, err := f.service.WriteUpload(f.ctx, late, 0, strings.NewReader("mine")); err != nil {
		t.Fatalf("WriteUpload() error = %v", err)
	}
	kept, err := f.service.CommitUpload(f.ctx, f.alice, late)
	if err != nil || kept.Entry.Name != "notes (1).txt" {
		t.Fatalf("late conflict saved %#v, %v; want notes (1).txt", kept.Entry, err)
	}
	if got, want := f.names(t, ""), []string{"notes (1).txt", "notes.txt", "photo (1).jpg", "photo.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	// A folder is never replaced; the upload goes beside it.
	if _, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "相册"); err != nil {
		t.Fatal(err)
	}
	if got := f.send(t, files.UploadRequest{Name: "相册", Conflict: files.ConflictReplace}, "file"); got.Replaced || got.Entry.Name != "相册 (1)" {
		t.Fatalf("replacing a folder saved %#v", got)
	}
}

func TestFolderUploadCreatesAndReusesFolders(t *testing.T) {
	f := newUploadFixture(t)
	first := f.send(t, files.UploadRequest{Directories: []string{"旅行", "2026"}, Name: "a.jpg"}, "aaaa")
	second := f.send(t, files.UploadRequest{Directories: []string{"旅行", "2026"}, Name: "b.jpg"}, "bbbb")
	if first.Entry.ParentID == "" || first.Entry.ParentID != second.Entry.ParentID {
		t.Fatalf("files of one folder upload landed in different folders: %q, %q", first.Entry.ParentID, second.Entry.ParentID)
	}
	if got := f.names(t, first.Entry.ParentID); !slices.Equal(got, []string{"a.jpg", "b.jpg"}) {
		t.Fatalf("folder holds %v", got)
	}
	folders, err := f.service.Folders(f.ctx, f.alice, f.space.ID, first.Entry.ParentID)
	if err != nil || len(folders) != 2 || folders[0].Name != "旅行" || folders[1].Name != "2026" {
		t.Fatalf("Folders() = %#v, %v", folders, err)
	}
	f.send(t, files.UploadRequest{Name: "旅行.txt"}, "x")
	if _, err := f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: f.space.ID, Directories: []string{"旅行.txt"}, Name: "c.jpg", Size: 1, Conflict: files.ConflictAsk}); !errors.Is(err, files.ErrFolderBlocked) {
		t.Fatalf("folder blocked by a file error = %v, want ErrFolderBlocked", err)
	}
	for _, name := range []string{"..", "a/b", ".a-nas-trash", strings.Repeat("长", 90)} {
		if _, err := f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: f.space.ID, Name: name, Size: 1, Conflict: files.ConflictAsk}); !errors.Is(err, files.ErrInvalidName) {
			t.Errorf("BeginUpload(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestUploadFollowsItsFolderWhenItMoves(t *testing.T) {
	f := newUploadFixture(t)
	folder, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "收件箱")
	if err != nil {
		t.Fatal(err)
	}
	upload := f.begin(t, files.UploadRequest{ParentID: folder.ID, Name: "movie.mkv", Size: 4})
	if _, err := f.service.WriteUpload(f.ctx, upload, 0, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Move(f.ctx, f.alice, folder.ID, "", "", "已整理"); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if _, err := f.service.CommitUpload(f.ctx, f.alice, upload); err != nil {
		t.Fatalf("CommitUpload() after the folder moved error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "已整理", "movie.mkv")); err != nil {
		t.Fatalf("upload is not in the moved folder: %v", err)
	}
	// An upload whose folder went to the trash cannot be saved.
	gone, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "临时")
	if err != nil {
		t.Fatal(err)
	}
	orphan := f.begin(t, files.UploadRequest{ParentID: gone.ID, Name: "x.bin", Size: 0})
	if _, err := f.service.Delete(f.ctx, f.alice, f.space.ID, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CommitUpload(f.ctx, f.alice, orphan); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("commit into a trashed folder error = %v, want ErrNotFound", err)
	}
}

func TestReconciliationRemovesOnlyUnheldUploadTemporaries(t *testing.T) {
	f := newUploadFixture(t)
	held := f.begin(t, files.UploadRequest{Name: "big.iso", Size: 8})
	if _, err := f.service.WriteUpload(f.ctx, held, 0, strings.NewReader("1234")); err != nil {
		t.Fatal(err)
	}
	// A leftover of a restart: nothing holds it.
	leftover := filepath.Join(f.root, ".a-nas-upload-0123456789abcdef")
	if err := os.WriteFile(leftover, bytes.Repeat([]byte("x"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(leftover, f.now, f.now)
	if got := f.names(t, ""); len(got) != 0 {
		t.Fatalf("temporaries are listed: %v", got)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("a fresh temporary was removed: %v", err)
	}
	f.now = f.now.Add(time.Hour)
	_ = os.Chtimes(leftover, f.now.Add(-time.Hour), f.now.Add(-time.Hour))
	f.names(t, "")
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary was kept: %v", err)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-upload-*")); len(temporaries) != 1 {
		t.Fatalf("held temporary was removed: %v", temporaries)
	}
	if err := f.service.AbortUpload(f.ctx, f.alice, held); err != nil {
		t.Fatalf("AbortUpload() error = %v", err)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-upload-*")); len(temporaries) != 0 {
		t.Fatalf("aborted temporary was kept: %v", temporaries)
	}
}

func TestReconciliationRemovesCopiesARestartLeftBehind(t *testing.T) {
	f := newUploadFixture(t)
	// The service restarted in the middle of copying a folder.
	leftover := filepath.Join(f.root, ".a-nas-copy-0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(leftover, "春节"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "春节", "a.jpg"), []byte("aaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(leftover, f.now, f.now)
	if got := f.names(t, ""); len(got) != 0 {
		t.Fatalf("a copy in progress is listed: %v", got)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("a recent copy was removed: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.names(t, "")
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a copy left behind was kept: %v", err)
	}
	// A copy that runs is never removed, however long it takes.
	source := f.send(t, files.UploadRequest{Name: "片段.mp4"}, "clip")
	copied, err := f.service.Copy(f.ctx, f.alice, source.Entry.ID, f.space.ID, "", "片段 副本.mp4")
	if err != nil || copied.Name != "片段 副本.mp4" {
		t.Fatalf("Copy() = %#v, %v", copied, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-copy-*")); len(leftovers) != 0 {
		t.Fatalf("a finished copy left %v", leftovers)
	}
}

func TestArchiveItemsListSelectionsOfOneSpace(t *testing.T) {
	f := newUploadFixture(t)
	a := f.send(t, files.UploadRequest{Directories: []string{"相册", "春节"}, Name: "a.jpg"}, "aaaa")
	f.send(t, files.UploadRequest{Directories: []string{"相册"}, Name: "b.jpg"}, "bb")
	note := f.send(t, files.UploadRequest{Name: "说明.txt"}, "n")
	folders, err := f.service.Folders(f.ctx, f.alice, f.space.ID, a.Entry.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	album := folders[0]
	// An upload in progress below the folder is not part of the archive.
	f.begin(t, files.UploadRequest{Directories: []string{"相册"}, Name: "c.jpg", Size: 9})
	items, err := f.service.ArchiveItems(f.ctx, f.alice, []string{album.ID, note.Entry.ID})
	if err != nil {
		t.Fatalf("ArchiveItems() error = %v", err)
	}
	var paths []string
	var total int64
	for _, item := range items {
		paths = append(paths, item.Path)
		total += item.Size
	}
	if paths[0] != "相册" || paths[len(paths)-1] != "说明.txt" {
		t.Fatalf("archive paths = %v, want the folder first and each entry in turn", paths)
	}
	slices.Sort(paths)
	if want := []string{"相册", "相册/b.jpg", "相册/春节", "相册/春节/a.jpg", "说明.txt"}; !slices.Equal(paths, want) || total != 7 {
		t.Fatalf("archive paths = %v (%d bytes), want %v (7 bytes)", paths, total, want)
	}
	file, err := f.service.OpenArchiveItem(f.ctx, items[3])
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(file)
	_ = file.Close()
	if string(content) != "aaaa" {
		t.Fatalf("archive item content = %q", content)
	}
	if _, err := f.service.ArchiveItems(f.ctx, f.alice, nil); !errors.Is(err, files.ErrInvalidSelection) {
		t.Fatalf("empty selection error = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
