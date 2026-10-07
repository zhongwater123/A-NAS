package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestMaterializeRegisteredSpacesProtectsPrivateSpaceBeforeIdentitySync(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	privateRoot := filepath.Join(mountPoint, "spaces", "private", "bootstrap-admin")
	if err := os.MkdirAll(privateRoot, 0o750); err != nil {
		t.Fatalf("create existing private space: %v", err)
	}
	runner := &unknownACLUserRunner{username: "bootstrap-admin"}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	executor.spaceRoots = map[string]string{"space:bootstrap-admin": privateRoot}

	if _, err := executor.materializeRegisteredSpaces(context.Background()); err != nil {
		t.Fatalf("materialize before identity sync: %v", err)
	}
	if !runner.ran(spacePermissionCommand{name: "setfacl", args: []string{"--set", inheritedACL(), privateRoot}}) {
		t.Fatal("the private space was not protected by a root-only inheritable ACL while its identity was unavailable")
	}
}

func TestMaterializeRegisteredSpacesAppliesTheACLLayout(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	privateRoot := filepath.Join(mountPoint, "spaces", "private", "alice")
	sharedRoot := filepath.Join(mountPoint, "spaces", "shared")
	for _, root := range []string{privateRoot, sharedRoot} {
		// An rc.5 volume already has the space roots.
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("create existing layout: %v", err)
		}
	}
	runner := &spacePermissionRunner{}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	executor.spaceRoots = map[string]string{"space:alice": privateRoot, sharedSpaceID: sharedRoot}
	executor.identities = map[string]identityRecord{
		"alice": {UID: 20101, Role: accounts.RoleMember, Enabled: true},
		"bob":   {UID: 20102, Role: accounts.RoleMember, Enabled: true},
	}

	repaired, err := executor.materializeRegisteredSpaces(context.Background())
	if err != nil {
		t.Fatalf("materializeRegisteredSpaces() error = %v", err)
	}

	spacesRoot := filepath.Join(mountPoint, "spaces")
	privateContainer := filepath.Join(spacesRoot, "private")
	privateTrash := filepath.Join(privateRoot, ".a-nas-trash")
	sharedTrash := filepath.Join(sharedRoot, ".a-nas-trash")
	want := map[string]string{
		spacesRoot:       "user::rwx,group::---,other::---,group:a-nas-users:--x,mask::--x",
		privateContainer: "user::rwx,group::---,other::---,group:a-nas-users:--x,mask::--x",
		privateRoot: "user::rwx,group::---,other::---,mask::rwx,user:alice:rwx," +
			"default:user::rwx,default:group::---,default:other::---,default:mask::rwx,default:user:alice:rwx",
		privateTrash: "user::rwx,group::---,other::---,user:alice:--x,mask::--x",
		filepath.Join(privateTrash, "alice"): "user::rwx,group::---,other::---,mask::rwx,user:alice:rwx," +
			"default:user::rwx,default:group::---,default:other::---,default:mask::rwx,default:user:alice:rwx",
		sharedRoot: "user::rwx,group::---,other::---,mask::rwx,group:a-nas-users:rwx," +
			"default:user::rwx,default:group::---,default:other::---,default:mask::rwx,default:group:a-nas-users:rwx",
		sharedTrash:                         "user::rwx,group::---,other::---,group:a-nas-users:--x,mask::--x",
		filepath.Join(sharedTrash, "alice"): userTrashACL("alice"),
		filepath.Join(sharedTrash, "bob"):   userTrashACL("bob"),
	}
	for path, acl := range want {
		for _, command := range []spacePermissionCommand{
			{name: "chown", args: []string{"root:root", path}},
			{name: "chmod", args: []string{"g-s", path}},
			{name: "setfacl", args: []string{"--set", acl, path}},
		} {
			if !runner.ran(command) {
				t.Errorf("missing %s %v", command.name, command.args)
			}
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Errorf("%s was not materialized as a directory: %v", path, err)
		}
	}
	if runner.ran(spacePermissionCommand{name: "setfacl", args: []string{"--set", userTrashACL("bob"), filepath.Join(privateTrash, "bob")}}) {
		t.Error("another member received a trash directory inside alice's private space")
	}
	for _, path := range []string{filepath.Dir(mountPoint), mountPoint} {
		if !runner.ran(spacePermissionCommand{name: "setfacl", args: []string{"--modify", "group:a-nas-users:--x", path}}) {
			t.Errorf("A-NAS accounts cannot traverse %s on the way to their spaces", path)
		}
	}
	for _, command := range runner.commands {
		joined := strings.Join(command.args, " ")
		if strings.Contains(joined, "a-nas-members") || strings.Contains(joined, "user:a-nas:") {
			t.Errorf("the Product Service account or the legacy group is still granted access: %v", command)
		}
	}
	slices.Sort(repaired)
	if wantRepaired := []string{sharedRoot, privateRoot, spacesRoot, privateContainer}; !sameStrings(repaired, wantRepaired) {
		t.Errorf("repaired = %v, want the pre-existing directories %v", repaired, wantRepaired)
	}
}

func TestSharedFolderACLGrantsReadToAllAndWriteToListedPrincipals(t *testing.T) {
	acl := sharedFolderACL("user:alice")
	for _, entry := range []string{
		"group:a-nas-users:r-x", "user:alice:rwx", "default:group:a-nas-users:r-x", "default:user:alice:rwx",
	} {
		if !slices.Contains(strings.Split(acl, ","), entry) {
			t.Errorf("read-only shared folder ACL %q is missing %q", acl, entry)
		}
	}
	if strings.Contains(sharedFolderACL("group:a-nas-users"), "group:a-nas-users:r-x") {
		t.Error("a writable shared folder still lists a read-only entry for all accounts")
	}
}

func TestSameACLIgnoresOrderAndEffectiveComments(t *testing.T) {
	getfacl := "user::rwx\nuser:alice:rwx\t\t#effective:r-x\ngroup::---\nmask::r-x\nother::---\n\n"
	if !sameACL(getfacl, "other::---,mask::r-x,group::---,user:alice:rwx,user::rwx") {
		t.Fatal("equivalent ACLs compared as different")
	}
	if sameACL(getfacl, "user::rwx,group::---,mask::r-x,other::---") {
		t.Fatal("an extra entry was ignored")
	}
}

func TestMaterializeRegisteredSpacesRejectsSymlinkContainer(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(mountPoint, 0o750); err != nil {
		t.Fatalf("create mount point: %v", err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(mountPoint, "spaces")); err != nil {
		t.Fatalf("create spaces symlink: %v", err)
	}
	runner := &spacePermissionRunner{}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})

	_, err := executor.materializeRegisteredSpaces(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a safe directory") {
		t.Fatalf("materializeRegisteredSpaces() error = %v, want unsafe-directory rejection", err)
	}
	for _, command := range runner.commands {
		if slices.Contains(command.args, filepath.Join(mountPoint, "spaces")) || slices.Contains(command.args, target) {
			t.Fatalf("unsafe container reached a privileged command: %#v", command)
		}
	}
}

func TestMaterializeRegisteredSpacesMovesAsidePlantedTrashLink(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "data")
	sharedRoot := filepath.Join(mountPoint, "spaces", "shared")
	if err := os.MkdirAll(sharedRoot, 0o750); err != nil {
		t.Fatalf("create Shared: %v", err)
	}
	// A member or an app that can write Shared replaces the trash root.
	target := t.TempDir()
	trashRoot := filepath.Join(sharedRoot, ".a-nas-trash")
	if err := os.Symlink(target, trashRoot); err != nil {
		t.Fatalf("plant trash link: %v", err)
	}
	runner := &spacePermissionRunner{}
	executor := NewExecutor(nil, runner, Options{SystemRoot: t.TempDir(), MountPoint: mountPoint})
	executor.spaceRoots = map[string]string{sharedSpaceID: sharedRoot}
	executor.identities = map[string]identityRecord{"alice": {UID: 20101, Role: accounts.RoleMember, Enabled: true}}

	repaired, err := executor.materializeRegisteredSpaces(context.Background())
	if err != nil {
		t.Fatalf("materializeRegisteredSpaces() error = %v", err)
	}
	for _, command := range runner.commands {
		for _, arg := range command.args {
			if strings.HasPrefix(arg, target) {
				t.Fatalf("the link target reached a privileged command: %#v", command)
			}
		}
	}
	if info, err := os.Lstat(filepath.Join(trashRoot, "alice")); err != nil || !info.IsDir() {
		t.Fatalf("trash directory was not recreated: %v", err)
	}
	displaced, _ := filepath.Glob(trashRoot + ".displaced-*")
	if len(displaced) != 1 {
		t.Fatalf("displaced entries = %v, want the planted link kept aside", displaced)
	}
	if link, err := os.Readlink(displaced[0]); err != nil || link != target {
		t.Errorf("displaced link = %q, %v", link, err)
	}
	if !slices.Contains(repaired, trashRoot) {
		t.Errorf("repaired = %v, want %s", repaired, trashRoot)
	}
}

func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

type spacePermissionCommand struct {
	name string
	args []string
}

type spacePermissionRunner struct {
	commands []spacePermissionCommand
}

type unknownACLUserRunner struct {
	spacePermissionRunner
	username string
}

func (r *unknownACLUserRunner) Run(ctx context.Context, name string, args []string, input string) ([]byte, error) {
	output, err := r.spacePermissionRunner.Run(ctx, name, args, input)
	if name == "setfacl" && len(args) >= 2 && strings.Contains(args[1], "user:"+r.username+":") {
		return []byte("setfacl: Option -s: invalid argument near character 48"), errors.New("exit status 2")
	}
	return output, err
}

func (r *spacePermissionRunner) ran(command spacePermissionCommand) bool {
	return slices.ContainsFunc(r.commands, func(got spacePermissionCommand) bool {
		return got.name == command.name && slices.Equal(got.args, command.args)
	})
}

// Run records commands with opened directories replaced by the paths they
// were opened at, so tests can name the directories they expect.
func (r *spacePermissionRunner) Run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	recorded := append([]string(nil), args...)
	for i, arg := range recorded {
		if strings.HasPrefix(arg, "/proc/") {
			if target, err := os.Readlink(arg); err == nil {
				recorded[i] = target
			}
		}
	}
	r.commands = append(r.commands, spacePermissionCommand{name: name, args: recorded})
	return nil, nil
}
