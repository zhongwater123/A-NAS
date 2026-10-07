package linux

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMaterializeRegisteredSpacesRepairsContainerPermissions(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	privateRoot := filepath.Join(mountPoint, "spaces", "private", "alice")
	sharedRoot := filepath.Join(mountPoint, "spaces", "shared")
	for _, root := range []string{privateRoot, sharedRoot} {
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("create existing rc4 layout: %v", err)
		}
	}
	runner := &spacePermissionRunner{}
	executor := NewExecutor(nil, runner, Options{
		SystemRoot: t.TempDir(),
		MountPoint: mountPoint,
	})
	executor.spaceRoots = map[string]string{
		"space:alice":  privateRoot,
		"space:shared": sharedRoot,
	}

	if err := executor.materializeRegisteredSpaces(context.Background()); err != nil {
		t.Fatalf("materializeRegisteredSpaces() error = %v", err)
	}

	spacesRoot := filepath.Join(mountPoint, "spaces")
	privateContainer := filepath.Join(spacesRoot, "private")
	privateTrash := filepath.Join(privateRoot, ".a-nas-trash")
	sharedTrash := filepath.Join(sharedRoot, ".a-nas-trash")
	privateACL := "user::rwx,user:alice:rwx,group::rwx,mask::rwx,other::---," +
		"default:user::rwx,default:user:alice:rwx,default:group::rwx,default:mask::rwx,default:other::---"
	for _, command := range []spacePermissionCommand{
		{name: "chown", args: []string{"root:a-nas-members", spacesRoot}},
		{name: "chmod", args: []string{"0710", spacesRoot}},
		{name: "chown", args: []string{"root:a-nas-members", privateContainer}},
		{name: "chmod", args: []string{"0710", privateContainer}},
		{name: "chown", args: []string{"alice:a-nas", privateRoot}},
		{name: "chmod", args: []string{"2770", privateRoot}},
		{name: "setfacl", args: []string{"--modify", privateACL, privateRoot}},
		{name: "chown", args: []string{"alice:a-nas", privateTrash}},
		{name: "chmod", args: []string{"2770", privateTrash}},
		{name: "setfacl", args: []string{"--modify", privateACL, privateTrash}},
		{name: "chown", args: []string{"root:a-nas-members", sharedRoot}},
		{name: "chmod", args: []string{"2770", sharedRoot}},
		{name: "chown", args: []string{"root:a-nas-members", sharedTrash}},
		{name: "chmod", args: []string{"2770", sharedTrash}},
	} {
		if !slices.ContainsFunc(runner.commands, func(got spacePermissionCommand) bool {
			return got.name == command.name && slices.Equal(got.args, command.args)
		}) {
			t.Errorf("missing command %s %v; got %#v", command.name, command.args, runner.commands)
		}
	}
	for _, path := range []string{privateTrash, sharedTrash} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Errorf("trash root %q was not materialized as a directory: %v", path, err)
		}
	}
}

func TestMaterializeRegisteredSpacesRejectsSymlinkContainer(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mountPoint, 0o750); err != nil {
		t.Fatalf("create mount point: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(mountPoint, "spaces")); err != nil {
		t.Fatalf("create spaces symlink: %v", err)
	}
	runner := &spacePermissionRunner{}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})

	err := executor.materializeRegisteredSpaces(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a safe directory") {
		t.Fatalf("materializeRegisteredSpaces() error = %v, want unsafe-directory rejection", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("unsafe container reached privileged commands: %#v", runner.commands)
	}
}

type spacePermissionCommand struct {
	name string
	args []string
}

type spacePermissionRunner struct {
	commands []spacePermissionCommand
}

func (r *spacePermissionRunner) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.commands = append(r.commands, spacePermissionCommand{name: name, args: append([]string(nil), args...)})
	return nil, nil
}
