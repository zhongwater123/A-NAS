//go:build rootintegration

package linux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// TestRootPhotosStoreBelongsToThePhotoServiceAlone creates the photos
// subvolume on a real Btrfs volume and checks who can reach it (ADR 0011).
func TestRootPhotosStoreBelongsToThePhotoServiceAlone(t *testing.T) {
	ctx := context.Background()
	mount := "/srv/a-nas/data"
	resetAgentState(t)
	setUpDataVolume(t, mount)
	startSamba(t)
	executor := NewExecutor(nil, nil, Options{SystemRoot: "/", MountPoint: mount})
	if err := executor.SetCredential(ctx, accounts.CredentialRequest{
		UserID: "user:petra", PrivateSpaceID: "space:petra", Username: "petra",
		Password: "petra password for tests", Role: accounts.RoleAdmin, UID: 20131, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	photos := filepath.Join(mount, "photos")
	private := filepath.Join(mount, "spaces", "private", "petra")
	expect := func(what string, allowed bool, result outcome) {
		t.Helper()
		if result.ok != allowed {
			t.Errorf("%s: allowed=%v, want %v\n%s", what, result.ok, allowed, result.output)
		}
	}

	if _, err := executor.EnsurePhotosStore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(photos); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("photos store created before the photo service identity exists: %v", err)
	}

	uid := strconv.Itoa(accounts.PhotoServiceUID)
	run(t, "groupadd", "--system", "--gid", uid, accounts.PhotoServiceUser)
	run(t, "useradd", "--system", "--uid", uid, "--gid", uid, "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", accounts.PhotoServiceUser)
	t.Cleanup(func() {
		_ = exec.Command("userdel", accounts.PhotoServiceUser).Run()
		_ = exec.Command("groupdel", accounts.PhotoServiceUser).Run()
	})
	if _, err := executor.EnsurePhotosStore(ctx); err != nil {
		t.Fatal(err)
	}
	var stat syscall.Stat_t
	if err := syscall.Lstat(photos, &stat); err != nil || stat.Uid != accounts.PhotoServiceUID || stat.Gid != accounts.PhotoServiceUID || stat.Mode&0o7777 != 0o700 {
		t.Fatalf("photos store = uid %d gid %d mode %o, %v; want a-nas-photos 0700", stat.Uid, stat.Gid, stat.Mode&0o7777, err)
	}
	run(t, "btrfs", "subvolume", "show", photos)

	writeSecret := asUser("petra", "sh", "-c", "echo secret > "+filepath.Join(private, "secret.txt"))
	expect("petra writes her space", true, writeSecret)
	expect("photo service writes its store", true, asUser(accounts.PhotoServiceUser, "sh", "-c", "echo catalog > "+filepath.Join(photos, "probe")))
	expect("photo service reads the volume marker", true, asUser(accounts.PhotoServiceUser, "cat", filepath.Join(mount, ".a-nas-volume.json")))
	expect("photo service reads a member's private space", false, asUser(accounts.PhotoServiceUser, "cat", filepath.Join(private, "secret.txt")))
	expect("photo service lists the Shared folder", false, asUser(accounts.PhotoServiceUser, "ls", filepath.Join(mount, "spaces", "shared")))
	expect("an administrator lists the photo store", false, asUser("petra", "ls", photos))
	expect("the Product Service lists the photo store", false, asUser("a-nas", "ls", photos))
	expect("an administrator's terminal reads a photo file", false, asUser("petra", "cat", filepath.Join(photos, "probe")))

	// Someone with root loosens the store; the next repair restores it.
	run(t, "chmod", "0755", photos)
	run(t, "setfacl", "--modify", "user:petra:rwx", photos)
	repaired, err := executor.EnsurePhotosStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(repaired, photos) {
		t.Errorf("repairs = %v, want the photos store", repaired)
	}
	expect("an administrator lists the repaired store", false, asUser("petra", "ls", photos))
	expect("photo service still reads its store", true, asUser(accounts.PhotoServiceUser, "cat", filepath.Join(photos, "probe")))
}
