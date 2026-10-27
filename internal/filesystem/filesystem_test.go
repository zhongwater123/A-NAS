package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootFileSystemStaysBeneathItsBase(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewRootFileSystem(base, false)
	space := filepath.Join(base, "spaces", "alice")
	if err := os.MkdirAll(space, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, outside := range []string{"/etc/passwd", filepath.Join(base, "..", "secret")} {
		if _, err := r.Open(ctx, outside); !errors.Is(err, ErrForbidden) {
			t.Errorf("Open(%q) error = %v, want ErrForbidden", outside, err)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(space, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open(ctx, filepath.Join(space, "escape", "passwd")); err == nil {
		t.Fatal("Open() followed a symlinked directory out of the base")
	}
	if err := os.Symlink(filepath.Join(space, "a.txt"), filepath.Join(space, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open(ctx, filepath.Join(space, "link.txt")); err == nil {
		t.Fatal("Open() followed a symlinked file")
	}
	if info, err := r.Lstat(ctx, filepath.Join(space, "link.txt")); err != nil || info.Kind != KindSymlink {
		t.Fatalf("Lstat(symlink) = %#v, %v", info, err)
	}
	if err := r.RemoveAll(ctx, base); !errors.Is(err, ErrForbidden) {
		t.Fatalf("RemoveAll(base) error = %v, want ErrForbidden", err)
	}
}

func TestRootFileSystemPassesTraverseOnlyDirectories(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewRootFileSystem(base, false)
	container := filepath.Join(base, "spaces")
	space := filepath.Join(container, "alice")
	if err := os.MkdirAll(filepath.Join(space, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "docs", "a.txt"), []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Like spaces/ and .a-nas-trash/, the container may be passed but not listed.
	if err := os.Chmod(container, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(container, 0o700) })

	if _, err := r.Scan(ctx, container, ScanSkip{}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Scan(traverse-only container) error = %v, want permission denied", err)
	}
	if entries, err := r.Scan(ctx, space, ScanSkip{}); err != nil || len(entries) != 2 {
		t.Fatalf("Scan(space) = %#v, %v", entries, err)
	}
	if err := r.MkdirAll(ctx, filepath.Join(space, "trash", "item")); err != nil {
		t.Fatalf("MkdirAll() through a traverse-only container error = %v", err)
	}
	if err := r.Rename(ctx, filepath.Join(space, "docs", "a.txt"), filepath.Join(space, "trash", "item", "content")); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	file, err := r.Open(ctx, filepath.Join(space, "trash", "item", "content"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_ = file.Close()
	if size, err := r.Usage(ctx, space); err != nil || size != int64(len("contents")) {
		t.Fatalf("Usage() = %d, %v", size, err)
	}
}

func TestRootFileSystemRenameAndCopyNeverReplace(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewRootFileSystem(base, false)
	for name, contents := range map[string]string{"a.txt": "a", "b.txt": "b"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Rename(ctx, filepath.Join(base, "a.txt"), filepath.Join(base, "b.txt")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Rename() onto an existing file error = %v, want fs.ErrExist", err)
	}
	if err := r.Copy(ctx, filepath.Join(base, "a.txt"), filepath.Join(base, "b.txt")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Copy() onto an existing file error = %v, want fs.ErrExist", err)
	}
	if contents, _ := os.ReadFile(filepath.Join(base, "b.txt")); string(contents) != "b" {
		t.Fatalf("b.txt was overwritten with %q", contents)
	}
}

// Copies get the modes of files created through the Web, so they inherit the
// target's default ACL rather than having it masked by the original's modes.
func TestRootFileSystemCopiesAreCreatedLikeNewFiles(t *testing.T) {
	// On the data volume a default ACL replaces the umask.
	umask := unix.Umask(0)
	t.Cleanup(func() { unix.Umask(umask) })
	ctx := context.Background()
	base := t.TempDir()
	r := NewRootFileSystem(base, false)
	source := filepath.Join(base, "private", "album")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.jpg"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(source, 0o700) })
	if err := os.MkdirAll(filepath.Join(base, "shared"), 0o770); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "shared", "album")
	if err := r.Copy(ctx, source, target); err != nil {
		t.Fatalf("Copy() of a read-only folder error = %v", err)
	}
	for path, want := range map[string]os.FileMode{target: 0o770, filepath.Join(target, "a.jpg"): 0o660} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %v, want %v", path, got, want)
		}
	}
}
