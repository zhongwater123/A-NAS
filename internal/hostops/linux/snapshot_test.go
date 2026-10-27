package linux_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/hostops/linux"
)

// Survey reads only the mounted data volume: a bare mount point, as before
// the volume is mounted, holds another directory's entries.
func TestSurveyRefusesWithoutTheDataVolume(t *testing.T) {
	runner := &recordingRunner{}
	mount := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(mount, ".a-nas-snapshots", "0123456789abcdef0123456789abcdef"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: mount})
	if _, err := executor.Survey(context.Background(), nil); !errors.Is(err, filesystem.ErrVolumeUnavailable) {
		t.Fatalf("Survey() error = %v, want ErrVolumeUnavailable", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("Survey() ran %v", runner.lines())
	}
}

func TestSnapshotsOfUnknownSpacesAreNotFound(t *testing.T) {
	runner := &recordingRunner{}
	executor := linux.NewExecutor(nil, runner, linux.Options{SystemRoot: t.TempDir(), MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.Create(context.Background(), "space:gone", "snapshot:1"); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("Create() error = %v, want ErrNotFound", err)
	}
	if err := executor.Delete(context.Background(), "space:gone", "snapshot:1"); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
	if err := executor.Objects(context.Background(), "snapshot:1", func(filesystem.SnapshotObject) error { return nil }); !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("Objects() error = %v, want ErrNotFound", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ran %v for an unknown space", runner.lines())
	}
}

// Objects skips symbolic links, so Open must not follow one either, in the
// key's last part or on the way to it (issue #176).
func TestOpenReadsOnlyObjectsBeneathTheSnapshot(t *testing.T) {
	mount := filepath.Join(t.TempDir(), "data")
	digest := sha256.Sum256([]byte("a-nas:snapshot:v1\x00snapshot:1"))
	root := filepath.Join(mount, ".a-nas-snapshots", hex.EncodeToString(digest[:16]))
	outside := t.TempDir()
	for path, contents := range map[string]string{
		filepath.Join(root, "docs", "a.txt"): "inside",
		filepath.Join(outside, "secret.txt"): "outside",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(root, "linked"):           outside,
		filepath.Join(root, "docs", "link.txt"): filepath.Join(outside, "secret.txt"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	executor := linux.NewExecutor(nil, &recordingRunner{}, linux.Options{SystemRoot: t.TempDir(), MountPoint: mount})
	ctx := context.Background()

	reader, object, err := executor.Open(ctx, "snapshot:1", "docs/a.txt")
	if err != nil {
		t.Fatalf("Open(docs/a.txt) error = %v", err)
	}
	contents, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(contents) != "inside" || object.Name != "a.txt" || object.SizeBytes != 6 {
		t.Fatalf("Open(docs/a.txt) = %q, %+v, %v", contents, object, err)
	}
	for _, key := range []string{"linked/secret.txt", "docs/link.txt", "../../secret.txt", "docs/../../x", "/etc/passwd", "missing", "docs/a.txt/x", "a\x00b"} {
		if reader, _, err := executor.Open(ctx, "snapshot:1", key); !errors.Is(err, filesystem.ErrNotFound) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Errorf("Open(%q) error = %v, want ErrNotFound", key, err)
		}
	}
	if _, _, err := executor.Open(ctx, "snapshot:1", "docs"); !errors.Is(err, filesystem.ErrUnsupportedType) {
		t.Errorf("Open(docs) error = %v, want ErrUnsupportedType", err)
	}
}
