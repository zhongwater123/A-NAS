package photos_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestImportPersistsOriginalAcrossRestart(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	root := filepath.Join(t.TempDir(), "photos")
	service := openService(t, root, c.Now)
	private, shared := libraries(t, service, alice)
	original := jpegBytes(t)

	asset := importPhoto(t, service, alice, private.ID, "", "IMG_0001.JPG", original)
	if !strings.HasPrefix(asset.ID, "photo:") || asset.MediaType != "image/jpeg" || asset.SizeBytes != int64(len(original)) ||
		asset.UploadedBy != alice.UserID || !asset.ImportedAt.Equal(c.Now()) || asset.Trash != nil {
		t.Fatalf("Import() = %+v", asset)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openService(t, root, c.Now)
	reopenedPrivate, reopenedShared := libraries(t, reopened, alice)
	if reopenedPrivate.ID != private.ID || reopenedShared.ID != shared.ID {
		t.Fatalf("library IDs changed across restart")
	}
	got, err := reopened.Get(ctx, alice, asset.ID)
	if err != nil || !reflect.DeepEqual(got, asset) {
		t.Fatalf("Get() = %+v, %v; want %+v", got, err, asset)
	}
	content, err := reopened.Open(ctx, alice, asset.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer content.Reader.Close()
	stored, err := io.ReadAll(content.Reader)
	if err != nil || !bytes.Equal(stored, original) || content.MediaType != "image/jpeg" || content.Name != "IMG_0001.JPG" {
		t.Fatalf("Open() content mismatch: err=%v name=%q type=%q", err, content.Name, content.MediaType)
	}
	if _, err := content.Reader.Write([]byte("x")); err == nil {
		t.Fatalf("original was opened writable")
	}
}

// pngHeader returns a PNG signature and IHDR chunk declaring the given size;
// nothing after the header is needed to read the image configuration.
func pngHeader(width, height uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], width)
	binary.BigEndian.PutUint32(ihdr[4:], height)
	ihdr[8], ihdr[9] = 8, 2
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(ihdr)))
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	return append([]byte("\x89PNG\r\n\x1a\n"), chunk.Bytes()...)
}

func TestImportRejectsUnsupportedOrUnsafeContent(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	root := filepath.Join(t.TempDir(), "photos")
	service, err := photos.Open(root, photos.Options{Now: c.Now, DisableCapacityReserve: true, MaxImportBytes: 4096})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, _ := libraries(t, service, alice)

	cases := []struct {
		name    string
		content []byte
		want    error
	}{
		{"empty", nil, photos.ErrUnsupportedType},
		{"text", []byte("hello, this is not a photo"), photos.ErrUnsupportedType},
		{"gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), photos.ErrUnsupportedType},
		{"jpeg signature with garbage", append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0x42}, 64)...), photos.ErrUnsupportedType},
		{"png declaring too many pixels", pngHeader(70000, 70000), photos.ErrTooLarge},
		{"over the size limit", append(encodePNG(9), make([]byte, 5000)...), photos.ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Import(ctx, alice, photos.ImportRequest{
				LibraryID: private.ID, Name: "upload.png", Content: bytes.NewReader(tc.content),
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Import() error = %v, want %v", err, tc.want)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, "staging")); len(entries) != 0 {
				t.Fatalf("staging left behind: %v", entries)
			}
			if files := objectFiles(t, root); len(files) != 0 {
				t.Fatalf("objects left behind: %v", files)
			}
		})
	}
}

func TestImportRejectsInvalidNames(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	for _, name := range []string{"", "   ", ".", "..", ".hidden.png", "a/b.png", `a\b.png`, "tab\tname.png", strings.Repeat("x", 256)} {
		_, err := service.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: name, Content: bytes.NewReader(encodePNG(1))})
		if !errors.Is(err, photos.ErrInvalidName) {
			t.Fatalf("Import(name %q) error = %v, want ErrInvalidName", name, err)
		}
	}
	asset := importPhoto(t, service, alice, private.ID, "", "  trimmed.png  ", encodePNG(1))
	if asset.Name != "trimmed.png" {
		t.Fatalf("Name = %q, want trimmed", asset.Name)
	}
}

func TestIdenticalImportsShareOneOriginalAndFormADuplicateSet(t *testing.T) {
	ctx := context.Background()
	service, c, root := newService(t)
	private, shared := libraries(t, service, alice)
	same := encodePNG(7)

	first := importPhoto(t, service, alice, private.ID, "", "a.png", same)
	c.Advance(time.Second)
	second := importPhoto(t, service, alice, private.ID, "", "b.png", same)
	other := importPhoto(t, service, alice, private.ID, "", "c.png", encodePNG(8))
	inShared := importPhoto(t, service, bob, shared.ID, "", "a.png", same)

	if first.ID == second.ID {
		t.Fatalf("identical imports were merged into one asset")
	}
	if files := objectFiles(t, root); len(files) != 2 {
		t.Fatalf("object files = %d, want 2 (one per distinct original)", len(files))
	}
	roles := map[string]photos.DuplicateRole{}
	for _, asset := range []photos.Asset{first, second, other} {
		got, err := service.Get(ctx, alice, asset.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		roles[asset.Name] = got.Duplicate
	}
	if roles["a.png"] != photos.DuplicateFirst || roles["b.png"] != photos.DuplicateRepeated || roles["c.png"] != photos.DuplicateNone {
		t.Fatalf("duplicate roles = %v", roles)
	}
	// Duplicate sets never span libraries.
	if got, _ := service.Get(ctx, bob, inShared.ID); got.Duplicate != photos.DuplicateNone {
		t.Fatalf("shared copy duplicate role = %q, want none", got.Duplicate)
	}

	if _, err := service.Trash(ctx, alice, first.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	if got, _ := service.Get(ctx, alice, second.ID); got.Duplicate != photos.DuplicateNone {
		t.Fatalf("remaining asset duplicate role = %q after its twin was trashed", got.Duplicate)
	}
}

func TestTimelinePagesNewestFirstAndSkipsTrash(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	private, _ := libraries(t, service, alice)
	var imported []photos.Asset
	for i := range 5 {
		imported = append(imported, importPhoto(t, service, alice, private.ID, "", "p.png", encodePNG(uint8(i))))
		c.Advance(time.Minute)
	}
	if _, err := service.Trash(ctx, alice, imported[2].ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}

	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		result, err := service.Timeline(ctx, alice, private.ID, cursor, 2)
		if err != nil {
			t.Fatalf("Timeline() error = %v", err)
		}
		for _, asset := range result.Assets {
			seen = append(seen, asset.ID)
		}
		if result.Next == "" {
			break
		}
		if page > 3 {
			t.Fatalf("timeline did not terminate")
		}
		cursor = result.Next
	}
	want := []string{imported[4].ID, imported[3].ID, imported[1].ID, imported[0].ID}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("timeline = %v, want %v", seen, want)
	}
	if _, err := service.Timeline(ctx, alice, private.ID, "not a cursor", 2); !errors.Is(err, photos.ErrInvalidCursor) {
		t.Fatalf("Timeline(bad cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestDirectoryListingPagesAssetsByName(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	album, err := service.CreateDirectory(ctx, alice, private.ID, "", "Album")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	// Two assets share a name, so the cursor must also order by ID.
	for i, name := range []string{"d.png", "a.png", "c.png", "a.png", "b.png"} {
		importPhoto(t, service, alice, private.ID, "", name, encodePNG(uint8(i)))
	}

	var names []string
	var directories int
	cursor := ""
	for page := 0; ; page++ {
		listing, err := service.ListDirectory(ctx, alice, private.ID, "", cursor, 2)
		if err != nil {
			t.Fatalf("ListDirectory() error = %v", err)
		}
		if page == 0 && (len(listing.Directories) != 1 || listing.Directories[0].ID != album.ID) {
			t.Fatalf("first page directories = %+v", listing.Directories)
		}
		directories += len(listing.Directories)
		for _, asset := range listing.Assets {
			names = append(names, asset.Name)
		}
		if listing.Next == "" {
			break
		}
		if page > 3 {
			t.Fatalf("listing did not terminate")
		}
		cursor = listing.Next
	}
	if got := strings.Join(names, ","); got != "a.png,a.png,b.png,c.png,d.png" || directories != 1 {
		t.Fatalf("listing = %s with %d directories", got, directories)
	}
	if _, err := service.ListDirectory(ctx, alice, private.ID, "", "not a cursor", 2); !errors.Is(err, photos.ErrInvalidCursor) {
		t.Fatalf("ListDirectory(bad cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestVirtualDirectoriesOrganiseWithoutChangingAssets(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	private, shared := libraries(t, service, alice)
	year, err := service.CreateDirectory(ctx, alice, private.ID, "", "2026")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	month, err := service.CreateDirectory(ctx, alice, private.ID, year.ID, "October")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	if _, err := service.CreateDirectory(ctx, alice, private.ID, "", "2026"); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("duplicate sibling error = %v, want ErrConflict", err)
	}
	if _, err := service.CreateDirectory(ctx, alice, private.ID, month.ID, "2026"); err != nil {
		t.Fatalf("same name under another parent error = %v", err)
	}
	if _, err := service.UpdateDirectory(ctx, alice, year.ID, photos.DirectoryUpdate{ParentID: new(month.ID)}); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("move into descendant error = %v, want ErrConflict", err)
	}
	if _, err := service.UpdateDirectory(ctx, alice, year.ID, photos.DirectoryUpdate{ParentID: new(year.ID)}); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("move into itself error = %v, want ErrConflict", err)
	}
	sharedDirectory, err := service.CreateDirectory(ctx, alice, shared.ID, "", "Elsewhere")
	if err != nil {
		t.Fatalf("CreateDirectory(shared) error = %v", err)
	}

	asset := importPhoto(t, service, alice, private.ID, month.ID, "cat.png", encodePNG(1))
	rejected := photos.AssetUpdate{Name: new("dog.png"), DirectoryID: new(sharedDirectory.ID)}
	if _, err := service.Update(ctx, alice, asset.ID, rejected); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("move into another library's directory error = %v, want ErrNotFound", err)
	}
	// The rename sent with the rejected move did not apply either.
	if got, err := service.Get(ctx, alice, asset.ID); err != nil || got.Name != "cat.png" || got.DirectoryID != month.ID {
		t.Fatalf("after a rejected update Get() = %+v, %v", got, err)
	}
	moved, err := service.Update(ctx, alice, asset.ID, photos.AssetUpdate{DirectoryID: new(year.ID)})
	if err != nil || moved.ID != asset.ID || moved.DirectoryID != year.ID {
		t.Fatalf("Move() = %+v, %v", moved, err)
	}
	renamed, err := service.Update(ctx, alice, asset.ID, photos.AssetUpdate{Name: new("kitten.png")})
	if err != nil || renamed.ID != asset.ID || renamed.Name != "kitten.png" {
		t.Fatalf("Rename() = %+v, %v", renamed, err)
	}
	listing, err := service.ListDirectory(ctx, alice, private.ID, year.ID, "", 0)
	if err != nil || len(listing.Directories) != 1 || listing.Directories[0].ID != month.ID ||
		len(listing.Assets) != 1 || listing.Assets[0].ID != asset.ID {
		t.Fatalf("ListDirectory() = %+v, %v", listing, err)
	}

	if err := service.DeleteDirectory(ctx, alice, year.ID); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("delete non-empty directory error = %v, want ErrConflict", err)
	}
	if _, err := service.Update(ctx, alice, asset.ID, photos.AssetUpdate{DirectoryID: new(month.ID)}); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if _, err := service.Trash(ctx, alice, asset.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	child, err := service.ListDirectory(ctx, alice, private.ID, month.ID, "", 0)
	if err != nil || len(child.Directories) != 1 {
		t.Fatalf("ListDirectory(month) = %+v, %v", child, err)
	}
	if err := service.DeleteDirectory(ctx, alice, child.Directories[0].ID); err != nil {
		t.Fatalf("DeleteDirectory(empty) error = %v", err)
	}
	if err := service.DeleteDirectory(ctx, alice, month.ID); err != nil {
		t.Fatalf("DeleteDirectory(only trashed assets) error = %v", err)
	}
	restored, err := service.Restore(ctx, alice, asset.ID)
	if err != nil || restored.DirectoryID != "" {
		t.Fatalf("Restore() = %+v, %v; want library root", restored, err)
	}
}

func TestTrashRestoreAndPurge(t *testing.T) {
	ctx := context.Background()
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	asset := importPhoto(t, service, alice, private.ID, "", "beach.png", encodePNG(4))

	if _, err := service.Restore(ctx, alice, asset.ID); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("Restore(available) error = %v, want ErrConflict", err)
	}
	if err := service.Purge(ctx, alice, asset.ID); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("Purge(available) error = %v, want ErrConflict", err)
	}
	trashed, err := service.Trash(ctx, alice, asset.ID)
	if err != nil || trashed.Trash == nil || trashed.Trash.TrashedBy != alice.UserID ||
		!trashed.Trash.PurgeAfter.Equal(c.Now().Add(photos.DefaultTrashRetention)) {
		t.Fatalf("Trash() = %+v, %v", trashed, err)
	}
	if _, err := service.Update(ctx, alice, asset.ID, photos.AssetUpdate{Name: new("x.png")}); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("Rename(trashed) error = %v, want ErrConflict", err)
	}
	if page, _ := service.Timeline(ctx, alice, private.ID, "", 10); len(page.Assets) != 0 {
		t.Fatalf("trashed asset still in timeline")
	}
	if trash, err := service.ListTrash(ctx, alice, private.ID); err != nil || len(trash) != 1 || trash[0].ID != asset.ID {
		t.Fatalf("ListTrash() = %+v, %v", trash, err)
	}
	if _, err := service.Restore(ctx, alice, asset.ID); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if _, err := service.Trash(ctx, alice, asset.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	if err := service.Purge(ctx, alice, asset.ID); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := service.Get(ctx, alice, asset.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Get(purged) error = %v, want ErrNotFound", err)
	}
	if files := objectFiles(t, root); len(files) != 0 {
		t.Fatalf("purged original still stored: %v", files)
	}
}

func TestCopyIsIndependentButSharesTheOriginal(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, shared := libraries(t, service, alice)
	source := importPhoto(t, service, alice, private.ID, "", "dog.png", encodePNG(5))

	copied, err := service.Copy(ctx, alice, source.ID, shared.ID, "")
	if err != nil || copied.ID == source.ID || copied.LibraryID != shared.ID || copied.UploadedBy != alice.UserID {
		t.Fatalf("Copy() = %+v, %v", copied, err)
	}
	if _, err := service.Update(ctx, alice, copied.ID, photos.AssetUpdate{Name: new("family dog.png")}); err != nil {
		t.Fatalf("Rename(copy) error = %v", err)
	}
	if got, _ := service.Get(ctx, alice, source.ID); got.Name != "dog.png" {
		t.Fatalf("renaming the copy renamed the source: %+v", got)
	}
	if files := objectFiles(t, root); len(files) != 1 {
		t.Fatalf("object files = %d, want the original stored once", len(files))
	}

	if _, err := service.Trash(ctx, alice, source.ID); err != nil {
		t.Fatalf("Trash(source) error = %v", err)
	}
	if _, err := service.Copy(ctx, alice, source.ID, shared.ID, ""); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("Copy(trashed) error = %v, want ErrConflict", err)
	}
	if err := service.Purge(ctx, alice, source.ID); err != nil {
		t.Fatalf("Purge(source) error = %v", err)
	}
	content, err := service.Open(ctx, bob, copied.ID)
	if err != nil {
		t.Fatalf("Open(copy) after purging the source error = %v", err)
	}
	_ = content.Reader.Close()

	if _, err := service.Trash(ctx, alice, copied.ID); err != nil {
		t.Fatalf("Trash(copy) error = %v", err)
	}
	if err := service.Purge(ctx, admin, copied.ID); err != nil {
		t.Fatalf("admin Purge(shared copy) error = %v", err)
	}
	if files := objectFiles(t, root); len(files) != 0 {
		t.Fatalf("original kept after its last reference was purged: %v", files)
	}
}

func TestExpireTrashAfterRetention(t *testing.T) {
	ctx := context.Background()
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	asset := importPhoto(t, service, alice, private.ID, "", "old.png", encodePNG(6))
	if _, err := service.Trash(ctx, alice, asset.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}

	c.Advance(photos.DefaultTrashRetention - time.Second)
	if purged, err := service.ExpireTrash(ctx); err != nil || purged != 0 {
		t.Fatalf("ExpireTrash() before retention = %d, %v", purged, err)
	}
	c.Advance(time.Second)
	if purged, err := service.ExpireTrash(ctx); err != nil || purged != 1 {
		t.Fatalf("ExpireTrash() at retention = %d, %v", purged, err)
	}
	if _, err := service.Get(ctx, alice, asset.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Get(expired) error = %v, want ErrNotFound", err)
	}
	if files := objectFiles(t, root); len(files) != 0 {
		t.Fatalf("expired original still stored: %v", files)
	}
}

func TestEmptyTrashOnlyPurgesWhatTheCallerMay(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, shared := libraries(t, service, alice)
	aliceShared := importPhoto(t, service, alice, shared.ID, "", "alice.png", encodePNG(10))
	bobShared := importPhoto(t, service, bob, shared.ID, "", "bob.png", encodePNG(11))
	private := importPhoto(t, service, alice, alicePrivate.ID, "", "mine.png", encodePNG(12))
	for _, trash := range []struct {
		p     photos.Principal
		asset photos.Asset
	}{{alice, aliceShared}, {bob, bobShared}, {alice, private}} {
		if _, err := service.Trash(ctx, trash.p, trash.asset.ID); err != nil {
			t.Fatalf("Trash() error = %v", err)
		}
	}

	if purged, err := service.EmptyTrash(ctx, bob, shared.ID); err != nil || purged != 1 {
		t.Fatalf("member EmptyTrash(shared) = %d, %v; want only their own", purged, err)
	}
	if _, err := service.Get(ctx, alice, aliceShared.ID); err != nil {
		t.Fatalf("member emptied someone else's shared trash: %v", err)
	}
	viewer := photos.Principal{UserID: admin.UserID, Admin: true, Viewing: []photos.ViewingGrant{
		{OwnerUserID: alice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
	}}
	for _, p := range []photos.Principal{admin, viewer} {
		if purged, _ := service.EmptyTrash(ctx, p, alicePrivate.ID); purged != 0 {
			t.Fatalf("administrator emptied a member's private trash")
		}
	}
	if _, err := service.Get(ctx, alice, private.ID); err != nil {
		t.Fatalf("private trash changed: %v", err)
	}
	if purged, err := service.EmptyTrash(ctx, admin, shared.ID); err != nil || purged != 1 {
		t.Fatalf("admin EmptyTrash(shared) = %d, %v", purged, err)
	}
}

func TestImportFailsCleanlyWhenItsDirectoryDisappears(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	directory, err := service.CreateDirectory(ctx, alice, private.ID, "", "Inbox")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	content := &hookReader{Reader: bytes.NewReader(encodePNG(13)), hook: func() {
		if err := service.DeleteDirectory(ctx, alice, directory.ID); err != nil {
			t.Errorf("DeleteDirectory() during import error = %v", err)
		}
	}}

	_, err = service.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, DirectoryID: directory.ID, Name: "late.png", Content: content})
	if !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Import() error = %v, want ErrNotFound", err)
	}
	if files := objectFiles(t, root); len(files) != 0 {
		t.Fatalf("object of a failed import kept: %v", files)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "staging")); len(entries) != 0 {
		t.Fatalf("staging left behind: %v", entries)
	}
}

// hookReader runs hook once, before its first read.
type hookReader struct {
	io.Reader
	hook func()
}

func (r *hookReader) Read(p []byte) (int, error) {
	if r.hook != nil {
		r.hook()
		r.hook = nil
	}
	return r.Reader.Read(p)
}

func TestOpenRejectsCatalogFromANewerBuild(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	service, err := photos.Open(root, photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	_ = db.Close()
	if service, err := photos.Open(root, photos.Options{DisableCapacityReserve: true}); err == nil {
		_ = service.Close()
		t.Fatalf("Open() accepted a catalog from a newer build")
	}
}
