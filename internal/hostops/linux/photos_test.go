package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// photosRunner answers getent for the photo service account and creates
// directories for btrfs subvolume create; everything else is recorded.
type photosRunner struct {
	spacePermissionRunner
	passwd string
}

func (r *photosRunner) Run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	if _, err := r.spacePermissionRunner.Run(ctx, name, args, stdin); err != nil {
		return nil, err
	}
	switch {
	case name == "getent" && args[0] == "passwd" && args[1] == accounts.PhotoServiceUser && r.passwd != "":
		return []byte(r.passwd + "\n"), nil
	case name == "getent":
		return nil, notFound{}
	case name == "btrfs" && args[0] == "subvolume" && args[1] == "create":
		return nil, os.Mkdir(args[2], 0o755)
	default:
		return nil, nil
	}
}

// notFound is getent's exit status for an unknown key.
type notFound struct{}

func (notFound) Error() string { return "exit status 2" }
func (notFound) ExitCode() int { return 2 }

func TestPhotosStoreWaitsForThePhotoServiceIdentity(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &photosRunner{}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	if drifted, err := executor.ensurePhotosStore(context.Background()); err != nil || drifted {
		t.Fatalf("ensurePhotosStore() = %v, %v", drifted, err)
	}
	if _, err := os.Lstat(filepath.Join(mountPoint, photosDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("photos store created without its service identity")
	}
}

func TestPhotosStoreRefusesAnUnexpectedPhotoServiceUID(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	runner := &photosRunner{passwd: "a-nas-photos:x:998:998::/nonexistent:/usr/sbin/nologin"}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	if _, err := executor.ensurePhotosStore(context.Background()); !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("ensurePhotosStore() error = %v, want ErrIdentityConflict", err)
	}
	for _, command := range runner.commands {
		if command.name != "getent" {
			t.Fatalf("ran %s %v for a conflicting identity", command.name, command.args)
		}
	}
}

func TestPhotosStoreIsCreatedForThePhotoServiceAlone(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &photosRunner{passwd: "a-nas-photos:x:31000:31000::/nonexistent:/usr/sbin/nologin"}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	drifted, err := executor.ensurePhotosStore(context.Background())
	if err != nil || drifted {
		t.Fatalf("ensurePhotosStore() = %v, %v; a new store is not drift", drifted, err)
	}
	photos := filepath.Join(mountPoint, photosDirectory)
	for _, command := range []spacePermissionCommand{
		{name: "setfacl", args: []string{"--modify", "user:a-nas-photos:--x", filepath.Dir(mountPoint)}},
		{name: "setfacl", args: []string{"--modify", "user:a-nas-photos:--x", mountPoint}},
		{name: "btrfs", args: []string{"subvolume", "create", photos}},
		{name: "chown", args: []string{"31000:31000", photos}},
		{name: "setfacl", args: []string{"--remove-all", "--remove-default", photos}},
		{name: "chmod", args: []string{"0700", photos}},
	} {
		if !runner.ran(command) {
			t.Errorf("missing %s %s; ran %v", command.name, strings.Join(command.args, " "), runner.commands)
		}
	}

	// The store exists now; finding it with the wrong owner is drift.
	runner.commands = nil
	if drifted, err := executor.ensurePhotosStore(context.Background()); err != nil || !drifted {
		t.Fatalf("second ensurePhotosStore() = %v, %v; want drift repaired", drifted, err)
	}
	if runner.ran(spacePermissionCommand{name: "btrfs", args: []string{"subvolume", "create", photos}}) {
		t.Fatalf("existing photos store was created again")
	}
}

func TestSpacesReconcileDespiteABrokenPhotoServiceIdentity(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	sharedRoot := filepath.Join(mountPoint, "spaces", "shared")
	if err := os.MkdirAll(sharedRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &photosRunner{passwd: "a-nas-photos:x:998:998::/nonexistent:/usr/sbin/nologin"}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	executor.spaceRoots = map[string]string{sharedSpaceID: sharedRoot}
	if _, err := executor.materializeRegisteredSpaces(context.Background()); err != nil {
		t.Fatalf("materializeRegisteredSpaces() error = %v; a photo store problem must not block spaces", err)
	}
	if runner.ran(spacePermissionCommand{name: "getent", args: []string{"passwd", accounts.PhotoServiceUser}}) {
		t.Fatalf("space reconciliation consulted the photo service identity")
	}
}
