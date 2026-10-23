package files_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestShareSelectionDeduplicatesDescendantsAndKeepsEmptyDirectories(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	users := accounts.NewService(store, acceptingCredentials{}, accounts.Options{})
	owner, err := users.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, _ := users.ListSpaces(ctx, owner)
	space := findPrivateSpace(t, spaces)
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	volume := t.TempDir()
	service := files.NewService(catalog, volume, users, files.Options{AllowUnverifiedVolume: true, DisableCapacityReserve: true})
	dir, err := service.CreateDirectory(ctx, owner, space.ID, "", "photos")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := service.CreateDirectory(ctx, owner, space.ID, dir.ID, "empty")
	if err != nil {
		t.Fatal(err)
	}
	file, err := service.Upload(ctx, owner, space.ID, dir.ID, "hello.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := service.ShareSelection(ctx, owner, []string{file.ID, dir.ID, empty.ID, dir.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection) != 3 {
		t.Fatalf("selection = %+v", selection)
	}
	seen := map[string]bool{}
	for _, item := range selection {
		seen[item.Path] = true
	}
	for _, name := range []string{"photos", "photos/empty", "photos/hello.txt"} {
		if !seen[name] {
			t.Errorf("missing %s", name)
		}
	}
	content, err := service.OpenShareContent(ctx, owner, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = content.Reader.Close() }()
	data, err := io.ReadAll(content.Reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("content %q: %v", data, err)
	}
	stranger, err := users.CreateMember(ctx, owner, "stranger", "another secure password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ShareSelection(ctx, stranger, []string{file.ID}); err == nil {
		t.Fatal("private source leaked")
	}
	if _, err := service.OpenShareContent(ctx, stranger, file.ID); err == nil {
		t.Fatal("private source descriptor leaked")
	}
	// A stale directory identity must not silently become an empty share when
	// SMB replaced it with a regular file before preparation.
	emptyPath := filepath.Join(volume, "spaces", "private", "owner", "photos", "empty")
	if err := os.Remove(emptyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyPath, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ShareSelection(ctx, owner, []string{empty.ID}); !errors.Is(err, files.ErrInvalidSelection) {
		t.Fatalf("stale kind accepted: %v", err)
	}
}

func TestStrictShareScanBoundsDirectoryEnumeration(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fs := files.NewRootFileSystem(root, true)
	if _, err := fs.Scan(context.Background(), root, files.ScanSkip{Strict: true, Limit: 2}); !errors.Is(err, files.ErrInvalidSelection) {
		t.Fatalf("scan limit: %v", err)
	}
	if got, err := fs.Scan(context.Background(), root, files.ScanSkip{Strict: true, Limit: 3}); err != nil || len(got) != 3 {
		t.Fatalf("exact limit: %v %v", got, err)
	}
}
