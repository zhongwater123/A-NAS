package linux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

// Prune deletes subvolumes, so it acts only on the mounted data volume; a
// bare mount point, as before the volume is mounted, is left alone.
func TestPruneRefusesWithoutTheDataVolume(t *testing.T) {
	runner := &recordingRunner{}
	mount := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(mount, ".a-nas-snapshots", "0123456789abcdef0123456789abcdef"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: mount})
	if _, err := executor.Prune(context.Background(), nil); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("Prune() error = %v, want ErrVolumeUnavailable", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("Prune() ran %v without the data volume", runner.lines())
	}
}

func TestSnapshotsOfUnknownSpacesAreNotFound(t *testing.T) {
	runner := &recordingRunner{}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.Create(context.Background(), "space:gone", "snapshot:1"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Create() error = %v, want ErrNotFound", err)
	}
	if err := executor.Delete(context.Background(), "space:gone", "snapshot:1"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
	if err := executor.Objects(context.Background(), "snapshot:1", func(files.SnapshotObject) error { return nil }); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Objects() error = %v, want ErrNotFound", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran %v for an unknown space", runner.lines())
	}
}
