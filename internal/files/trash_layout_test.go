package files_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestSambaKeeptreeDeletionsBecomeSeparateRestorableItems(t *testing.T) {
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
	privateSpace := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volumeRoot := filepath.Join(t.TempDir(), "volume")
	service := files.NewService(catalog, volumeRoot, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	spaceRoot := filepath.Join(volumeRoot, "spaces", "private", "owner")

	docs, err := service.CreateDirectory(ctx, owner, privateSpace.ID, "", "docs")
	if err != nil {
		t.Fatal(err)
	}
	webFile, err := service.Upload(ctx, owner, privateSpace.ID, "", "web.txt", strings.NewReader("from web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, owner, privateSpace.ID, webFile.ID); err != nil {
		t.Fatalf("Web Delete() error = %v", err)
	}
	// Samba recycle with keeptree mirrors docs/ inside the per-user trash.
	userTrash := filepath.Join(spaceRoot, ".a-nas-trash", "owner")
	if err := os.MkdirAll(filepath.Join(userTrash, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(userTrash, "docs", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	items, err := service.ListTrash(ctx, owner)
	if err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	var names []string
	var second files.TrashItem
	for _, item := range items {
		names = append(names, item.Name)
		if item.Name == "second.txt" {
			second = item
		}
	}
	slices.Sort(names)
	if want := []string{"first.txt", "second.txt", "web.txt"}; !slices.Equal(names, want) {
		t.Fatalf("trash items = %v, want %v", names, want)
	}
	if again, err := service.ListTrash(ctx, owner); err != nil || len(again) != 3 {
		t.Fatalf("second import produced %d items (err %v), want 3", len(again), err)
	}

	restored, err := service.Restore(ctx, owner, second.ID, "", "")
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.ParentID != docs.ID {
		t.Fatalf("restored parent = %q, want the original docs folder %q", restored.ParentID, docs.ID)
	}
	if info, err := os.Stat(userTrash); err != nil || !info.IsDir() {
		t.Fatalf("restoring removed the per-user trash directory: %v", err)
	}
}
