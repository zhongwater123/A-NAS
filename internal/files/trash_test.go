package files_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// The recycle bin says where an item came from, what it holds and when it
// goes, and restoring still works once the original folder is gone.
func TestTrashItemsSayWhereTheyCameFromAndWhenTheyGo(t *testing.T) {
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
	spaces, err := accountService.ListSpaces(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	space := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	deletedAt := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	service := files.NewService(catalog, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, Now: func() time.Time { return deletedAt },
	})

	documents, err := service.CreateDirectory(ctx, owner, space.ID, "", "文档")
	if err != nil {
		t.Fatal(err)
	}
	projects, err := service.CreateDirectory(ctx, owner, space.ID, documents.ID, "项目")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.Upload(ctx, owner, space.ID, projects.ID, "计划.txt", bytes.NewBufferString("twelve bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Upload(ctx, owner, space.ID, projects.ID, "预算.txt", bytes.NewBufferString("four")); err != nil {
		t.Fatal(err)
	}

	file, err := service.Delete(ctx, owner, space.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Kind != files.EntryKindFile || file.SizeBytes != 12 || file.OriginalPath != "文档/项目" || file.OriginalGone {
		t.Fatalf("deleted file = %+v, want a 12-byte file from 文档/项目", file)
	}
	if want := deletedAt.Add(30 * 24 * time.Hour); !file.PurgeAt.Equal(want) {
		t.Fatalf("PurgeAt = %v, want %v", file.PurgeAt, want)
	}

	// Renaming the original folder moves where the file goes back.
	if _, err := service.Move(ctx, owner, documents.ID, "", "", "资料"); err != nil {
		t.Fatal(err)
	}
	items, err := service.ListTrash(ctx, owner)
	if err != nil || len(items) != 1 || items[0].OriginalPath != "资料/项目" {
		t.Fatalf("ListTrash() = %+v, %v; want the file from 资料/项目", items, err)
	}

	// Deleting the folder makes it an item of its own, and the file's
	// original folder is gone.
	folder, err := service.Delete(ctx, owner, space.ID, projects.ID)
	if err != nil {
		t.Fatal(err)
	}
	if folder.Kind != files.EntryKindDirectory || folder.SizeBytes != 4 || folder.OriginalPath != "资料" {
		t.Fatalf("deleted folder = %+v, want a folder holding 4 bytes from 资料", folder)
	}
	items, err = service.ListTrash(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var orphan files.TrashItem
	for _, item := range items {
		if item.ID == file.ID {
			orphan = item
		}
	}
	if !orphan.OriginalGone || orphan.OriginalPath != "资料/项目" {
		t.Fatalf("file after its folder was deleted = %+v, want its folder gone from 资料/项目", orphan)
	}

	// Restoring puts it at the space root instead of failing.
	restored, err := service.Restore(ctx, owner, file.ID, "", "")
	if err != nil {
		t.Fatalf("Restore() with the original folder gone: %v", err)
	}
	if restored.ParentID != "" || restored.Name != "计划.txt" {
		t.Fatalf("restored = %+v, want 计划.txt at the space root", restored)
	}

	// A name taken at the original place is a conflict, never an overwrite.
	if _, err := service.Upload(ctx, owner, space.ID, documents.ID, "项目", bytes.NewBufferString("a file named like the folder")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restore(ctx, owner, folder.ID, "", ""); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("Restore() onto a taken name error = %v, want ErrConflict", err)
	}
	if _, err := service.Restore(ctx, owner, folder.ID, "", "项目 (恢复)"); err != nil {
		t.Fatalf("Restore() under another name: %v", err)
	}
}
