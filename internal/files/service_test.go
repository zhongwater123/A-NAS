package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// countingFileSystem records MkdirAll calls; in production each one is a File
// Broker round trip.
type countingFileSystem struct {
	files.FileSystem
	mu       sync.Mutex
	mkdirAll map[string]int
}

func (c *countingFileSystem) MkdirAll(ctx context.Context, path string) error {
	c.mu.Lock()
	c.mkdirAll[path]++
	c.mu.Unlock()
	return c.FileSystem.MkdirAll(ctx, path)
}

func TestSpaceRootIsPreparedOnceRatherThanOnEveryOperation(t *testing.T) {
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
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	privateSpace := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "volume")
	fsys := &countingFileSystem{FileSystem: files.NewRootFileSystem(volumeRoot, true), mkdirAll: map[string]int{}}
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, FileSystem: fsys,
	})

	for range 3 {
		if _, err := service.List(ctx, admin, privateSpace.ID, ""); err != nil {
			t.Fatalf("List() error = %v", err)
		}
	}
	if _, err := service.CreateDirectory(ctx, admin, privateSpace.ID, "", "docs"); err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	root := filepath.Join(volumeRoot, "spaces", "private", "owner")
	if got := fsys.mkdirAll[root]; got != 1 {
		t.Fatalf("space root MkdirAll calls = %d, want 1", got)
	}
}

func TestMemberCanManageFilesWithoutSeeingAnotherPrivateSpace(t *testing.T) {
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
	bob, err := accountService.CreateMember(ctx, admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	spaces, err := accountService.ListSpaces(ctx, alice)
	if err != nil {
		t.Fatalf("list Alice spaces: %v", err)
	}
	privateSpace := findPrivateSpace(t, spaces)

	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "volume")
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})

	directory, err := service.CreateDirectory(ctx, alice, privateSpace.ID, "", "家庭")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	uploaded, err := service.Upload(ctx, alice, privateSpace.ID, directory.ID, "你好.txt", bytes.NewBufferString("hello A-NAS"))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if got, want := uploaded.Name, "你好.txt"; got != want {
		t.Fatalf("uploaded name = %q, want %q", got, want)
	}
	entries, err := service.List(ctx, alice, privateSpace.ID, directory.ID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(entries), 1; got != want || entries[0].ID != uploaded.ID {
		t.Fatalf("entries = %#v, want uploaded entry", entries)
	}
	trashed, err := service.Delete(ctx, alice, privateSpace.ID, uploaded.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	userTrash := filepath.Join(volumeRoot, "spaces", "private", "alice", ".a-nas-trash", "alice")
	if items, err := os.ReadDir(userTrash); err != nil || len(items) != 1 {
		t.Fatalf("Web delete did not use the shared Web/SMB per-user trash: %v %v", items, err)
	}
	entries, err = service.List(ctx, alice, privateSpace.ID, directory.ID)
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("deleted entry remained visible: %#v", entries)
	}
	restored, err := service.Restore(ctx, alice, trashed.ID, directory.ID, "恢复.txt")
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	content, err := service.OpenContent(ctx, alice, restored.ID)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	defer content.Reader.Close()
	gotContents, err := io.ReadAll(content.Reader)
	if err != nil {
		t.Fatalf("read restored content: %v", err)
	}
	if items, err := os.ReadDir(userTrash); err != nil || len(items) != 0 {
		t.Fatalf("restore left a trash container or removed the per-user trash: %v %v", items, err)
	}
	if got, want := string(gotContents), "hello A-NAS"; got != want {
		t.Fatalf("restored content = %q, want %q", got, want)
	}
	moved, err := service.Move(ctx, alice, restored.ID, "", "", "整理后.txt")
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := moved.ParentID, ""; got != want {
		t.Fatalf("moved parent = %q, want root", got)
	}
	copied, err := service.Copy(ctx, alice, moved.ID, "", directory.ID, "副本.txt")
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if copied.ID == moved.ID {
		t.Fatal("copied file reused the source stable ID")
	}
	copyContent, err := service.OpenContent(ctx, alice, copied.ID)
	if err != nil {
		t.Fatalf("open copied content: %v", err)
	}
	copyBytes, err := io.ReadAll(copyContent.Reader)
	_ = copyContent.Reader.Close()
	if err != nil || string(copyBytes) != "hello A-NAS" {
		t.Fatalf("copied content = %q, err = %v", copyBytes, err)
	}
	snapshot, err := service.CreateSnapshot(ctx, alice, privateSpace.ID, "整理完成")
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	snapshotEntries, err := service.ListSnapshotEntries(ctx, alice, snapshot.ID, "")
	if err != nil {
		t.Fatalf("ListSnapshotEntries() error = %v", err)
	}
	var snapshottedFile files.SnapshotEntry
	for _, entry := range snapshotEntries {
		if entry.Name == moved.Name {
			snapshottedFile = entry
		}
	}
	if snapshottedFile.ID == "" {
		t.Fatalf("snapshot did not contain %q: %#v", moved.Name, snapshotEntries)
	}
	fromSnapshot, err := service.RestoreSnapshotFile(ctx, alice, snapshot.ID, snapshottedFile.ID, directory.ID, "快照恢复.txt")
	if err != nil {
		t.Fatalf("RestoreSnapshotFile() error = %v", err)
	}
	snapshotContent, err := service.OpenContent(ctx, alice, fromSnapshot.ID)
	if err != nil {
		t.Fatalf("open snapshot-restored file: %v", err)
	}
	snapshotBytes, err := io.ReadAll(snapshotContent.Reader)
	_ = snapshotContent.Reader.Close()
	if err != nil || string(snapshotBytes) != "hello A-NAS" {
		t.Fatalf("snapshot-restored content = %q, err = %v", snapshotBytes, err)
	}

	if _, err := service.List(ctx, bob, privateSpace.ID, ""); err != files.ErrForbidden {
		t.Fatalf("Bob List(Alice private) error = %v, want ErrForbidden", err)
	}
}

func TestFileServiceDoesNotCreateSpaceOnAnUnavailableVolume(t *testing.T) {
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	privateSpace := findPrivateSpace(t, spaces)

	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "missing-volume")
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{})

	_, err = service.CreateDirectory(ctx, admin, privateSpace.ID, "", "must-not-land-on-system-disk")
	if !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("CreateDirectory() error = %v, want ErrVolumeUnavailable", err)
	}
	if _, statErr := os.Stat(volumeRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("volume root was created despite unavailable volume: %v", statErr)
	}
}

func TestSambaRecycleItemIsImportedAndCanBeRestored(t *testing.T) {
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	privateSpace := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "volume")
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	spaceRoot := filepath.Join(volumeRoot, "spaces", "private", "owner")
	if err := os.MkdirAll(spaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaceRoot, "from-smb.txt"), []byte("smb recycle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(ctx, admin, privateSpace.ID, ""); err != nil {
		t.Fatal(err)
	}
	recycleRoot := filepath.Join(spaceRoot, ".a-nas-trash", "owner")
	if err := os.MkdirAll(recycleRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(spaceRoot, "from-smb.txt"), filepath.Join(recycleRoot, "from-smb.txt")); err != nil {
		t.Fatal(err)
	}
	items, err := service.ListTrash(ctx, admin)
	if err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	if len(items) != 1 || items[0].DeletedBy != admin.ID {
		t.Fatalf("Samba trash items = %#v", items)
	}
	restored, err := service.Restore(ctx, admin, items[0].ID, "", "restored-from-smb.txt")
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	content, err := service.OpenContent(ctx, admin, restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "smb recycle" {
		t.Fatalf("restored content = %q", data)
	}
}

func TestTreeListsEverythingBelowAFolderWithStableIDs(t *testing.T) {
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
	adminSpaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	privateSpace := findPrivateSpace(t, adminSpaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "volume")
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	root := filepath.Join(volumeRoot, "spaces", "private", "owner")
	// "Music Videos" and "Music-2" sort near "Music/" but are not below it.
	for _, name := range []string{"Music/a.mp3", "Music/Live/b.mp3", "Music Videos/c.mp3", "Music-2/d.mp3", "e.mp3"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	all, err := service.Tree(ctx, admin, privateSpace.ID, "")
	if err != nil {
		t.Fatalf("Tree(root): %v", err)
	}
	var musicID, songID string
	for _, entry := range all {
		switch entry.Path {
		case "Music":
			musicID = entry.ID
		case "Music/Live/b.mp3":
			songID = entry.ID
		}
	}
	if len(all) != 9 || musicID == "" || songID == "" {
		t.Fatalf("Tree(root) listed %d entries: %+v", len(all), all)
	}
	below, err := service.Tree(ctx, admin, privateSpace.ID, musicID)
	if err != nil {
		t.Fatalf("Tree(Music): %v", err)
	}
	var paths []string
	for _, entry := range below {
		paths = append(paths, entry.Path)
	}
	if want := []string{"Live", "Live/b.mp3", "a.mp3"}; !slices.Equal(paths, want) {
		t.Errorf("Tree(Music) = %q, want %q", paths, want)
	}
	if _, err := service.Tree(ctx, admin, privateSpace.ID, songID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Tree(file) error = %v, want not found", err)
	}
	if _, err := service.Tree(ctx, alice, privateSpace.ID, ""); !errors.Is(err, files.ErrForbidden) {
		t.Errorf("another member listed the space: %v", err)
	}

	// A rename keeps the entry ID, which the music library and the media
	// center rely on; Lookup reports the new path.
	if err := os.Rename(filepath.Join(root, "Music", "Live"), filepath.Join(root, "Music", "Concerts")); err != nil {
		t.Fatal(err)
	}
	below, err = service.Tree(ctx, admin, privateSpace.ID, musicID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(below, func(entry files.TreeEntry) bool { return entry.ID == songID && entry.Path == "Concerts/b.mp3" }) {
		t.Errorf("renamed folder lost the song's ID: %+v", below)
	}
	if _, path, err := service.Lookup(ctx, admin, songID); err != nil || path != "Music/Concerts/b.mp3" {
		t.Errorf("Lookup(song) = %q, %v", path, err)
	}
	if _, _, err := service.Lookup(ctx, alice, songID); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("another member looked up the song: %v", err)
	}
}

func TestCutAndCopyBetweenSpaces(t *testing.T) {
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
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
	private := findPrivateSpace(t, spaces)
	var shared accounts.Space
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			shared = space
		}
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	service := files.NewService(catalog, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	read := func(user accounts.User, id string) string {
		t.Helper()
		content, err := service.OpenContent(ctx, user, id)
		if err != nil {
			t.Fatalf("OpenContent(%s) error = %v", id, err)
		}
		defer content.Reader.Close()
		data, _ := io.ReadAll(content.Reader)
		return string(data)
	}
	names := func(user accounts.User, spaceID, parentID string) []string {
		t.Helper()
		entries, err := service.List(ctx, user, spaceID, parentID)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
		return names
	}

	films, _ := service.CreateDirectory(ctx, alice, private.ID, "", "电影")
	heat, _ := service.Upload(ctx, alice, private.ID, films.ID, "Heat (1995).mp4", bytes.NewBufferString("heat"))
	extras, _ := service.CreateDirectory(ctx, alice, private.ID, films.ID, "花絮")
	if _, err := service.Upload(ctx, alice, private.ID, extras.ID, "making.txt", bytes.NewBufferString("making of")); err != nil {
		t.Fatal(err)
	}
	library, err := service.CreateDirectory(ctx, alice, shared.ID, "", "影视")
	if err != nil {
		t.Fatal(err)
	}

	copied, err := service.Copy(ctx, alice, heat.ID, shared.ID, library.ID, "")
	if err != nil {
		t.Fatalf("Copy() to the shared space error = %v", err)
	}
	if copied.SpaceID != shared.ID || copied.ParentID != library.ID || copied.Name != "Heat (1995).mp4" || read(bob, copied.ID) != "heat" {
		t.Fatalf("copy = %+v", copied)
	}
	if read(alice, heat.ID) != "heat" {
		t.Fatal("copying to another space changed the original")
	}
	// A folder only names a target inside the target space.
	if _, err := service.Copy(ctx, alice, heat.ID, shared.ID, films.ID, ""); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Copy() into a folder of another space error = %v, want ErrNotFound", err)
	}

	moved, err := service.Move(ctx, alice, films.ID, shared.ID, "", "")
	if err != nil {
		t.Fatalf("Move() of a folder to the shared space error = %v", err)
	}
	if moved.SpaceID != shared.ID || moved.ParentID != "" || moved.Name != "电影" || moved.ID == films.ID {
		t.Fatalf("moved = %+v", moved)
	}
	if got := names(alice, private.ID, ""); slices.Contains(got, "电影") {
		t.Fatalf("the moved folder is still in the private space: %v", got)
	}
	if _, err := service.OpenContent(ctx, alice, heat.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("OpenContent(moved original) error = %v, want ErrNotFound", err)
	}
	tree, err := service.Tree(ctx, bob, shared.ID, moved.ID)
	if err != nil {
		t.Fatalf("Tree(moved folder) error = %v", err)
	}
	var paths []string
	for _, entry := range tree {
		paths = append(paths, entry.Path)
	}
	if !slices.Equal(paths, []string{"Heat (1995).mp4", "花絮", "花絮/making.txt"}) {
		t.Fatalf("moved folder holds %v", paths)
	}

	again, _ := service.Upload(ctx, alice, private.ID, "", "Heat (1995).mp4", bytes.NewBufferString("heat"))
	if _, err := service.Move(ctx, alice, again.ID, shared.ID, library.ID, ""); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("Move() onto an existing name error = %v, want ErrConflict", err)
	}
	if read(alice, again.ID) != "heat" {
		t.Fatal("a refused move removed the original")
	}
	if _, err := service.Move(ctx, bob, again.ID, shared.ID, "", ""); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("Bob moved Alice's private file: %v", err)
	}
	if _, err := service.Copy(ctx, bob, again.ID, shared.ID, "", ""); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("Bob copied Alice's private file: %v", err)
	}
}

func findPrivateSpace(t *testing.T, spaces []accounts.Space) accounts.Space {
	t.Helper()
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate {
			return space
		}
	}
	t.Fatal("private space not found")
	return accounts.Space{}
}

type acceptingCredentials struct{}

func (acceptingCredentials) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (acceptingCredentials) DisableCredential(context.Context, string) error { return nil }
