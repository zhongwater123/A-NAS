package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// The photo service owns the data volume's photos subvolume alone
// (ADR 0011): no account, group, app or ACL entry other than its own identity
// reaches it. Photo assets are authorized in its Catalog, not by ACLs.

const (
	photosDirectory = "photos"
	photosACL       = "user::rwx,group::---,other::---"
)

// ensurePhotosStore creates the photos subvolume for the photo service
// identity and repairs drift in its owner, mode and ACL. It does nothing
// while that identity is absent, refuses one whose UID differs from the fixed
// one, and never walks the contents. It reports whether an existing store had
// drifted.
func (e *Executor) ensurePhotosStore(ctx context.Context) (bool, error) {
	exists, _, err := e.accountState(ctx, accounts.PhotoServiceUser, accounts.PhotoServiceUID)
	if err != nil {
		return false, fmt.Errorf("photo service identity: %w", err)
	}
	if !exists {
		return false, nil
	}
	// The service must pass through the parent of the mount point, like smbd.
	if output, err := e.runner.Run(ctx, "setfacl", []string{
		"--modify", "user:" + accounts.PhotoServiceUser + ":--x", filepath.Dir(e.mountPoint),
	}, ""); err != nil {
		return false, commandError("allow the photo service to reach the data volume", err, output)
	}
	photos := filepath.Join(e.mountPoint, photosDirectory)
	created := false
	if _, err := os.Lstat(photos); errors.Is(err, os.ErrNotExist) {
		if output, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", photos}, ""); err != nil {
			return false, commandError("create photos subvolume", err, output)
		}
		created = true
	} else if err != nil {
		return false, err
	}
	directory, _, err := e.openVolumeDirectory(photos, 0o700, false)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return false, err
	}
	target := openedPath(directory)
	current, err := e.runner.Run(ctx, "getfacl", []string{"--absolute-names", "--omit-header", target}, "")
	if err != nil {
		return false, commandError("read photos ACL", err, current)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && stat.Uid == accounts.PhotoServiceUID && stat.Gid == accounts.PhotoServiceUID &&
		info.Mode().Perm() == 0o700 && info.Mode()&(os.ModeSetgid|os.ModeSetuid|os.ModeSticky) == 0 && sameACL(string(current), photosACL) {
		return false, nil
	}
	owner := strconv.Itoa(accounts.PhotoServiceUID)
	for _, command := range []struct {
		name string
		args []string
	}{
		{name: "chown", args: []string{owner + ":" + owner, target}},
		{name: "setfacl", args: []string{"--remove-all", "--remove-default", target}},
		{name: "chmod", args: []string{"0700", target}},
	} {
		if output, err := e.runner.Run(ctx, command.name, command.args, ""); err != nil {
			return false, commandError("protect "+photos, err, output)
		}
	}
	return !created, nil
}
