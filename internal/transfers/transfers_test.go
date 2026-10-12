package transfers

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

type fixture struct {
	ctx      context.Context
	accounts *accounts.Service
	files    *files.Service
	service  *Service
	path     string
	alice    accounts.User
	bob      accounts.User
	shared   accounts.Space
	root     string
	mu       sync.Mutex
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, credentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := accountService.CreateMember(ctx, admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, accounts: accountService, alice: alice, bob: bob, now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			f.shared = space
		}
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(t.TempDir(), "volume")
	f.root = filepath.Join(volume, "spaces", "shared")
	f.files = files.NewService(catalog, volume, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	f.path = filepath.Join(t.TempDir(), "transfers.db")
	f.open(t)
	return f
}

func (f *fixture) open(t *testing.T) {
	t.Helper()
	service, err := Open(f.path, f.files, f.accounts, Options{Now: f.clock})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(by time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(by)
	f.mu.Unlock()
}

func (f *fixture) beginUpload(t *testing.T, owner accounts.User, name string, size int64) Transfer {
	t.Helper()
	upload, err := f.service.BeginUpload(f.ctx, owner, UploadRequest{
		UploadRequest: files.UploadRequest{SpaceID: f.shared.ID, Name: name, Size: size, Conflict: files.ConflictAsk},
		Batch:         "batch-1", LastModified: 1760000000000,
	})
	if err != nil {
		t.Fatalf("BeginUpload(%s) error = %v", name, err)
	}
	return upload
}

func (f *fixture) state(t *testing.T, owner accounts.User, id string) Transfer {
	t.Helper()
	items, err := f.service.List(f.ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("transfer %s is not listed for %s", id, owner.Username)
	return Transfer{}
}

func TestUploadIsFollowedUntilSavedAndOnlyItsOwnerSeesIt(t *testing.T) {
	f := newFixture(t)
	upload := f.beginUpload(t, f.alice, "家庭视频.mp4", 10)
	if upload.State != StateRunning || upload.Location.SpaceKind != accounts.SpaceKindShared || upload.Batch != "batch-1" {
		t.Fatalf("begun upload = %#v", upload)
	}
	if _, err := f.service.WriteUpload(f.ctx, f.alice, upload.ID, 0, strings.NewReader("01234")); err != nil {
		t.Fatalf("WriteUpload() error = %v", err)
	}
	if got := f.state(t, f.alice, upload.ID); got.TransferredBytes != 5 || got.State != StateRunning {
		t.Fatalf("after a part: %#v", got)
	}
	if _, err := f.service.WriteUpload(f.ctx, f.bob, upload.ID, 5, strings.NewReader("56789")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user wrote to the upload: %v", err)
	}
	if items, _ := f.service.List(f.ctx, f.bob); len(items) != 0 {
		t.Fatalf("another user sees %v", items)
	}
	// Nothing arrives for a while: other browsers see it interrupted.
	f.advance(time.Minute)
	if got := f.state(t, f.alice, upload.ID); got.State != StateInterrupted {
		t.Fatalf("idle upload state = %s, want interrupted", got.State)
	}
	if paused, err := f.service.Pause(f.ctx, f.alice, upload.ID); err != nil || paused.State != StatePaused {
		t.Fatalf("Pause() = %#v, %v", paused, err)
	}
	if _, err := f.service.WriteUpload(f.ctx, f.alice, upload.ID, 5, strings.NewReader("56789")); err != nil {
		t.Fatal(err)
	}
	done, err := f.service.FinishUpload(f.ctx, f.alice, upload.ID)
	if err != nil || done.State != StateCompleted || done.EntryID == "" || done.TransferredBytes != 10 || done.FinishedAt == nil {
		t.Fatalf("FinishUpload() = %#v, %v", done, err)
	}
	if content, _ := os.ReadFile(filepath.Join(f.root, "家庭视频.mp4")); string(content) != "0123456789" {
		t.Fatalf("saved content = %q", content)
	}
	// A browser that missed the final response finds the upload complete.
	status, err := f.service.UploadStatus(f.ctx, f.alice, upload.ID)
	if err != nil || status.State != StateCompleted {
		t.Fatalf("UploadStatus() after completion = %#v, %v", status, err)
	}
	if again, err := f.service.FinishUpload(f.ctx, f.alice, upload.ID); err != nil || again.EntryID != done.EntryID {
		t.Fatalf("second FinishUpload() = %#v, %v", again, err)
	}
}

func TestUploadKeepsBothAndReportsTheSavedName(t *testing.T) {
	f := newFixture(t)
	first := f.beginUpload(t, f.alice, "a.txt", 1)
	second := f.beginUpload(t, f.bob, "a.txt", 1)
	for _, item := range []struct {
		owner accounts.User
		id    string
	}{{f.alice, first.ID}, {f.bob, second.ID}} {
		if _, err := f.service.WriteUpload(f.ctx, item.owner, item.id, 0, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.FinishUpload(f.ctx, f.alice, first.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := f.service.FinishUpload(f.ctx, f.bob, second.ID)
	if err != nil || saved.SavedName != "a (1).txt" {
		t.Fatalf("late conflict = %#v, %v", saved, err)
	}
	if _, err := f.service.BeginUpload(f.ctx, f.alice, UploadRequest{UploadRequest: files.UploadRequest{SpaceID: f.shared.ID, Name: "a.txt", Size: 1, Conflict: files.ConflictAsk}}); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("asking upload of a taken name error = %v", err)
	}
	if failure := FailureOf(files.ErrConflict); failure.Code != "name_conflict" {
		t.Fatalf("FailureOf(ErrConflict) = %#v", failure)
	}
	if failure := FailureOf(errors.New("open /srv/a-nas/data/spaces/shared/x: input/output error")); failure.Code != "internal_error" || strings.Contains(failure.Message, "/srv") {
		t.Fatalf("an internal error reveals %q", failure.Message)
	}
}

func TestCanceledUploadDiscardsItsBytes(t *testing.T) {
	f := newFixture(t)
	upload := f.beginUpload(t, f.alice, "big.iso", 6)
	if _, err := f.service.WriteUpload(f.ctx, f.alice, upload.ID, 0, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	canceled, err := f.service.Cancel(f.ctx, f.alice, upload.ID)
	if err != nil || canceled.State != StateCanceled || canceled.FinishedAt == nil {
		t.Fatalf("Cancel() = %#v, %v", canceled, err)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-upload-*")); len(temporaries) != 0 {
		t.Fatalf("canceled upload left %v", temporaries)
	}
	if _, err := f.service.WriteUpload(f.ctx, f.alice, upload.ID, 3, strings.NewReader("def")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("write after cancel error = %v", err)
	}
	if err := f.service.Remove(f.ctx, f.alice, upload.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if items, _ := f.service.List(f.ctx, f.alice); len(items) != 0 {
		t.Fatalf("removed transfer is listed: %v", items)
	}
}

func TestRestartFailsUploadsAndAbandonedUploadsExpire(t *testing.T) {
	f := newFixture(t)
	lost := f.beginUpload(t, f.alice, "lost.bin", 4)
	if _, err := f.service.WriteUpload(f.ctx, f.alice, lost.ID, 0, strings.NewReader("ab")); err != nil {
		t.Fatal(err)
	}
	f.service.flush(f.ctx)
	_ = f.service.Close()
	f.open(t)
	got := f.state(t, f.alice, lost.ID)
	if got.State != StateFailed || got.Error == nil || got.Error.Code != "service_restarted" || got.TransferredBytes != 2 {
		t.Fatalf("upload after restart = %#v", got)
	}
	if _, err := f.service.UploadStatus(f.ctx, f.alice, lost.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a lost upload can be resumed: %v", err)
	}

	abandoned := f.beginUpload(t, f.alice, "abandoned.bin", 4)
	f.advance(abandonedAfter + time.Minute)
	f.service.endAbandoned(f.ctx)
	got = f.state(t, f.alice, abandoned.ID)
	if got.State != StateFailed || got.Error == nil || got.Error.Code != "expired" {
		t.Fatalf("abandoned upload = %#v", got)
	}
	if temporaries, _ := filepath.Glob(filepath.Join(f.root, ".a-nas-upload-*")); len(temporaries) != 1 {
		// Only the restart's leftover remains, for reconciliation.
		t.Fatalf("temporaries = %v", temporaries)
	}
}

func TestDownloadOfAFileIsFollowedAndResumes(t *testing.T) {
	f := newFixture(t)
	entry, err := f.files.Upload(f.ctx, f.alice, f.shared.ID, "", "电影.mkv", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	download, err := f.service.CreateDownload(f.ctx, f.alice, []string{entry.ID})
	if err != nil || download.Kind != KindFile || download.TotalBytes != 10 || download.State != StatePending || download.ContentURL == "" {
		t.Fatalf("CreateDownload() = %#v, %v", download, err)
	}
	if _, err := f.service.CreateDownload(f.ctx, f.bob, []string{"file:missing"}); err == nil {
		t.Fatal("a missing entry could be downloaded")
	}
	// The browser drops the connection after four bytes, then resumes.
	partial := f.serve(t, f.alice, download.ID, "bytes=0-3")
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "0123" {
		t.Fatalf("first range = %d %q", partial.Code, partial.Body.String())
	}
	if got := f.state(t, f.alice, download.ID); got.State != StateInterrupted || got.TransferredBytes != 4 {
		t.Fatalf("after a partial range: %#v", got)
	}
	rest := f.serve(t, f.alice, download.ID, "bytes=4-")
	if rest.Code != http.StatusPartialContent || rest.Body.String() != "456789" {
		t.Fatalf("resumed range = %d %q", rest.Code, rest.Body.String())
	}
	if disposition := rest.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if got := f.state(t, f.alice, download.ID); got.State != StateCompleted || got.TransferredBytes != 10 || got.FinishedAt == nil {
		t.Fatalf("after the rest: %#v", got)
	}
	request := httptest.NewRequest(http.MethodGet, download.ContentURL, nil)
	if started, err := f.service.ServeDownload(httptest.NewRecorder(), request, f.bob, download.ID); started || !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user's download = %v, %v", started, err)
	}
}

func TestDownloadOfAFolderIsAZIPArchive(t *testing.T) {
	f := newFixture(t)
	folder, err := f.files.CreateDirectory(f.ctx, f.alice, f.shared.ID, "", "相册")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.Upload(f.ctx, f.alice, f.shared.ID, folder.ID, "a.jpg", strings.NewReader("aaaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.CreateDirectory(f.ctx, f.alice, f.shared.ID, folder.ID, "空"); err != nil {
		t.Fatal(err)
	}
	unreadable, err := f.files.Upload(f.ctx, f.alice, f.shared.ID, folder.ID, "locked.bin", strings.NewReader("zz"))
	if err != nil {
		t.Fatal(err)
	}
	download, err := f.service.CreateDownload(f.ctx, f.alice, []string{folder.ID})
	if err != nil || download.Kind != KindArchive || download.Name != "相册.zip" || download.TotalBytes != 6 {
		t.Fatalf("CreateDownload(folder) = %#v, %v", download, err)
	}
	_ = unreadable
	if err := os.Chmod(filepath.Join(f.root, "相册", "locked.bin"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(f.root, "相册", "locked.bin"), 0o600) })
	response := f.serve(t, f.alice, download.ID, "")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("archive response = %d %s", response.Code, response.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	contents := map[string]string{}
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(reader)
		contents[file.Name] = string(data)
	}
	slices.Sort(names)
	if os.Geteuid() == 0 {
		t.Skip("root reads the unreadable file")
	}
	want := []string{"相册/", "相册/a.jpg", "相册/空/", skippedListName}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("archive holds %v, want %v", names, want)
	}
	if contents["相册/a.jpg"] != "aaaa" || !strings.Contains(contents[skippedListName], "相册/locked.bin") {
		t.Fatalf("archive contents = %v", contents)
	}
	if got := f.state(t, f.alice, download.ID); got.State != StateCompleted || got.Skipped != 1 {
		t.Fatalf("archive transfer = %#v", got)
	}

	several, err := f.service.CreateDownload(f.ctx, f.alice, []string{folder.ID, unreadable.ID})
	if err != nil || several.Name != "相册 等 2 项.zip" {
		t.Fatalf("CreateDownload(several) = %#v, %v", several, err)
	}
}

func TestCanceledDownloadStopsAndUnstartedOnesFail(t *testing.T) {
	f := newFixture(t)
	entry, err := f.files.Upload(f.ctx, f.alice, f.shared.ID, "", "slow.bin", bytes.NewReader(make([]byte, 1<<20)))
	if err != nil {
		t.Fatal(err)
	}
	download, err := f.service.CreateDownload(f.ctx, f.alice, []string{entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	writer := &blockingWriter{ResponseRecorder: httptest.NewRecorder(), written: make(chan struct{}, 1), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, download.ContentURL, nil)
		_, err := f.service.ServeDownload(writer, request, f.alice, download.ID)
		result <- err
	}()
	<-writer.written
	if canceled, err := f.service.Cancel(f.ctx, f.alice, download.ID); err != nil || canceled.State != StateCanceled {
		t.Fatalf("Cancel() = %#v, %v", canceled, err)
	}
	close(writer.release)
	if err := <-result; !errors.Is(err, ErrCanceled) {
		t.Fatalf("ServeDownload() after cancel error = %v", err)
	}
	if got := f.state(t, f.alice, download.ID); got.State != StateCanceled {
		t.Fatalf("canceled download = %#v", got)
	}
	if response := f.serve(t, f.alice, download.ID, ""); response.Code == http.StatusOK {
		t.Fatal("a canceled download was sent again")
	}

	unstarted, err := f.service.CreateDownload(f.ctx, f.alice, []string{entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.advance(unstartedAfter + time.Minute)
	if err := f.service.store.failUnstarted(f.ctx, f.clock().Add(-unstartedAfter), f.clock()); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t, f.alice, unstarted.ID); got.State != StateFailed || got.Error.Code != "not_started" {
		t.Fatalf("unstarted download = %#v", got)
	}
	if err := f.service.ClearFinished(f.ctx, f.alice); err != nil {
		t.Fatal(err)
	}
	if items, _ := f.service.List(f.ctx, f.alice); len(items) != 0 {
		t.Fatalf("cleared history still lists %v", items)
	}
}

func TestHistoryKeepsTheNewestFinishedTransfers(t *testing.T) {
	f := newFixture(t)
	for index := 0; index < maxFinished+3; index++ {
		upload := f.beginUpload(t, f.alice, "f"+strconv.Itoa(index)+".txt", 0)
		_, _ = f.service.Cancel(f.ctx, f.alice, upload.ID)
		f.advance(time.Second)
	}
	if err := f.service.store.prune(f.ctx, f.clock().Add(-keepFinished), maxFinished); err != nil {
		t.Fatal(err)
	}
	items, err := f.service.List(f.ctx, f.alice)
	if err != nil || len(items) != maxFinished {
		t.Fatalf("history holds %d, want %d (%v)", len(items), maxFinished, err)
	}
	f.advance(keepFinished + time.Hour)
	if err := f.service.store.prune(f.ctx, f.clock().Add(-keepFinished), maxFinished); err != nil {
		t.Fatal(err)
	}
	if items, _ := f.service.List(f.ctx, f.alice); len(items) != 0 {
		t.Fatalf("expired history holds %d", len(items))
	}
}

func (f *fixture) serve(t *testing.T, owner accounts.User, id, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/transfers/"+id+"/content", nil)
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	recorder := httptest.NewRecorder()
	if started, err := f.service.ServeDownload(recorder, request, owner, id); err != nil && !started {
		recorder.Code = http.StatusConflict
	}
	return recorder
}

// blockingWriter holds a download after its first write until released.
type blockingWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	written chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(data []byte) (int, error) {
	count, err := w.ResponseRecorder.Write(data)
	w.once.Do(func() {
		w.written <- struct{}{}
		<-w.release
	})
	return count, err
}

type credentials struct{}

func (credentials) SetCredential(context.Context, accounts.CredentialRequest) error { return nil }
func (credentials) DisableCredential(context.Context, string) error                 { return nil }
