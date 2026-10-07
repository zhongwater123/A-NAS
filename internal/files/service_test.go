package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

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
	moved, err := service.Move(ctx, alice, restored.ID, "", "整理后.txt")
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := moved.ParentID, ""; got != want {
		t.Fatalf("moved parent = %q, want root", got)
	}
	copied, err := service.Copy(ctx, alice, moved.ID, directory.ID, "副本.txt")
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
