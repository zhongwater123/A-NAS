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
	service := files.NewLocalService(catalog, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{
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
	restored, err := service.Restore(ctx, owner, file.ID, "", "", files.ConflictAsk)
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
	if _, err := service.Restore(ctx, owner, folder.ID, "", "", files.ConflictAsk); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("Restore() onto a taken name error = %v, want ErrConflict", err)
	}
	if _, err := service.Restore(ctx, owner, folder.ID, "", "项目 (恢复)", files.ConflictAsk); err != nil {
		t.Fatalf("Restore() under another name: %v", err)
	}
}

// A name taken where an item goes back is the user's choice: keep both under
// a "(恢复)" name, or put the item in place of a file, which goes to the
// recycle bin. Nothing is overwritten.
func TestRestoreOntoATakenNameKeepsBothOrReplaces(t *testing.T) {
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
	service := files.NewLocalService(catalog, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true,
	})
	trashed := func(name, contents string) files.TrashItem {
		t.Helper()
		entry, err := service.Upload(ctx, owner, space.ID, "", name, bytes.NewBufferString(contents))
		if err != nil {
			t.Fatal(err)
		}
		item, err := service.Delete(ctx, owner, space.ID, entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	upload := func(name, contents string) {
		t.Helper()
		if _, err := service.Upload(ctx, owner, space.ID, "", name, bytes.NewBufferString(contents)); err != nil {
			t.Fatal(err)
		}
	}
	entries := func() map[string]files.Entry {
		t.Helper()
		listed, err := service.List(ctx, owner, space.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]files.Entry{}
		for _, entry := range listed {
			found[entry.Name] = entry
		}
		return found
	}

	plan := trashed("计划.txt", "the old plan")
	upload("计划.txt", "new")
	if _, err := service.Restore(ctx, owner, plan.ID, "", "", files.ConflictAsk); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("Restore(ask) onto a taken name error = %v, want ErrConflict", err)
	}
	kept, err := service.Restore(ctx, owner, plan.ID, "", "", files.ConflictRename)
	if err != nil {
		t.Fatalf("Restore(rename): %v", err)
	}
	if kept.Name != "计划 (恢复).txt" || kept.Replaced {
		t.Fatalf("Restore(rename) = %+v, want 计划 (恢复).txt beside the new one", kept)
	}
	second, err := service.Delete(ctx, owner, space.ID, entries()["计划.txt"].ID)
	if err != nil {
		t.Fatal(err)
	}
	upload("计划.txt", "newer")
	if kept, err = service.Restore(ctx, owner, second.ID, "", "", files.ConflictRename); err != nil || kept.Name != "计划 (恢复 2).txt" {
		t.Fatalf("Restore(rename) with 计划 (恢复).txt taken = %+v, %v; want 计划 (恢复 2).txt", kept, err)
	}

	report := trashed("报告.txt", "the old report")
	upload("报告.txt", "new")
	replaced, err := service.Restore(ctx, owner, report.ID, "", "", files.ConflictReplace)
	if err != nil {
		t.Fatalf("Restore(replace): %v", err)
	}
	if replaced.Name != "报告.txt" || !replaced.Replaced || replaced.SizeBytes != int64(len("the old report")) {
		t.Fatalf("Restore(replace) = %+v, want the old 报告.txt in place", replaced)
	}
	items, err := service.ListTrash(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var displaced bool
	for _, item := range items {
		displaced = displaced || (item.Name == "报告.txt" && item.SizeBytes == int64(len("new")))
	}
	if !displaced {
		t.Fatalf("trash = %+v, want the replaced 报告.txt in it", items)
	}

	// The file in the way may differ in letter case only.
	notes := trashed("Notes.txt", "the old notes")
	upload("notes.txt", "new")
	if restored, err := service.Restore(ctx, owner, notes.ID, "", "", files.ConflictReplace); err != nil || restored.Name != "Notes.txt" || !restored.Replaced {
		t.Fatalf("Restore(replace) over a case twin = %+v, %v; want Notes.txt in its place", restored, err)
	}
	if _, taken := entries()["notes.txt"]; taken {
		t.Fatal("the case twin stayed beside the restored file")
	}

	// A folder is never replaced: a file in its way comes back beside it,
	// and a trashed folder cannot replace anything.
	projectFile := trashed("项目", "a file named like a folder")
	folder, err := service.CreateDirectory(ctx, owner, space.ID, "", "项目")
	if err != nil {
		t.Fatal(err)
	}
	if restored, err := service.Restore(ctx, owner, projectFile.ID, "", "", files.ConflictReplace); err != nil || restored.Name != "项目 (恢复)" || restored.Replaced {
		t.Fatalf("Restore(replace) onto a folder = %+v, %v; want 项目 (恢复) beside it", restored, err)
	}
	folderItem, err := service.Delete(ctx, owner, space.ID, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restore(ctx, owner, folderItem.ID, "", "", files.ConflictReplace); !errors.Is(err, files.ErrUnsupportedType) {
		t.Fatalf("Restore(replace) of a folder error = %v, want ErrUnsupportedType", err)
	}
	want := map[string]int64{
		"计划.txt": int64(len("newer")), "计划 (恢复).txt": int64(len("the old plan")), "计划 (恢复 2).txt": int64(len("new")),
		"报告.txt": int64(len("the old report")), "Notes.txt": int64(len("the old notes")), "项目 (恢复)": int64(len("a file named like a folder")),
	}
	got := entries()
	for name, size := range want {
		if entry, ok := got[name]; !ok || entry.SizeBytes != size {
			t.Fatalf("space holds %v, want %s with %d bytes", got, name, size)
		}
	}
}
