package atomicfile_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
)

func TestWriteCreatesTheFileWithExactlyItsMode(t *testing.T) {
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	path := filepath.Join(t.TempDir(), "unit.mount")
	if err := atomicfile.Write(path, []byte("[Mount]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "[Mount]\n", 0o644)
}

func TestWriteReplacesAFileAndItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(path, []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, `{"new":true}`, 0o600)
}

func TestWriteLeavesNothingElseInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.json")
	for _, data := range []string{"first", "second"} {
		if err := atomicfile.Write(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "job.json" {
		t.Fatalf("directory holds %v", entries)
	}
	assertFile(t, path, "second", 0o600)
}

func TestWriteFailsWithoutItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "changes.json")
	if err := atomicfile.Write(path, []byte("[]"), 0o600); err == nil {
		t.Fatal("wrote into a directory that does not exist")
	}
}

func TestSyncDirReportsAMissingDirectory(t *testing.T) {
	if err := atomicfile.SyncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("synced a directory that does not exist")
	}
}

func assertFile(t *testing.T, path, want string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("content %q, want %q", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("mode %v, want %v", info.Mode().Perm(), mode)
	}
}
