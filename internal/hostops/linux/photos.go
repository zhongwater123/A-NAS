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

// EnsurePhotosStore creates and repairs the photo service's store while the
// data volume is mounted, and returns the store if it had drifted. It stays
// apart from space reconciliation: a broken photo service identity keeps
// photos unavailable but never blocks spaces, accounts or viewing.
func (e *Executor) EnsurePhotosStore(ctx context.Context) ([]string, error) {
	if !e.dataVolumeReady() {
		return nil, nil
	}
	e.materializeMu.Lock()
	defer e.materializeMu.Unlock()
	drifted, err := e.ensurePhotosStore(ctx)
	if drifted {
		return []string{filepath.Join(e.mountPoint, photosDirectory)}, err
	}
	return nil, err
}

// ensurePhotosStore creates the photos subvolume for the photo service
// identity and repairs drift in its owner, mode and ACL. It does nothing
// while that identity is absent, refuses one whose UID differs from the fixed
// one, and never walks the contents. It reports whether an existing store had
// drifted.
func (e *Executor) ensurePhotosStore(ctx context.Context) (bool, error) {
	return e.ensureOwnedStore(ctx, photosDirectory, accounts.PhotoServiceUser, accounts.PhotoServiceUID)
}

// EnsureSharesStore owns only the immutable sharing copies, outside member spaces.
func (e *Executor) EnsureSharesStore(ctx context.Context) ([]string, error) {
	if !e.dataVolumeReady() {
		return nil, nil
	}
	e.materializeMu.Lock()
	defer e.materializeMu.Unlock()
	drifted, err := e.ensureOwnedStore(ctx, "shares", accounts.ShareServiceUser, accounts.ShareServiceUID)
	if drifted {
		return []string{filepath.Join(e.mountPoint, "shares")}, err
	}
	return nil, err
}

func (e *Executor) ensureOwnedStore(ctx context.Context, directoryName, serviceUser string, serviceUID int) (bool, error) {
	exists, _, err := e.accountState(ctx, serviceUser, serviceUID)
	if err != nil {
		return false, fmt.Errorf("owned store identity: %w", err)
	}
	if !exists {
		return false, nil
	}
	// Like smbd, the service passes through the mount point and its parent,
	// neither of which it may list.
	for _, path := range []string{filepath.Dir(e.mountPoint), e.mountPoint} {
		if output, err := e.runner.Run(ctx, "setfacl", []string{
			"--modify", "user:" + serviceUser + ":--x", path,
		}, ""); err != nil {
			return false, commandError("allow the store service to reach the data volume", err, output)
		}
	}
	photos := filepath.Join(e.mountPoint, directoryName)
	created := false
	if _, err := os.Lstat(photos); errors.Is(err, os.ErrNotExist) {
		if output, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", photos}, ""); err != nil {
			return false, commandError("create owned subvolume", err, output)
		}
		created = true
	} else if err != nil {
		return false, err
	}
	directory, _, err := e.openVolumeDirectory(photos, 0o700, ordinaryDirectory)
	if err != nil {
		return false, err
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil {
		return false, err
	}
	target := openedPath(directory)
	current, err := e.runner.Output(ctx, "getfacl", []string{"--absolute-names", "--omit-header", target}, "")
	if err != nil {
		return false, commandError("read owned store ACL", err, current)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && stat.Uid == uint32(serviceUID) && stat.Gid == uint32(serviceUID) &&
		info.Mode().Perm() == 0o700 && info.Mode()&(os.ModeSetgid|os.ModeSetuid|os.ModeSticky) == 0 && sameACL(string(current), photosACL) {
		return false, nil
	}
	owner := strconv.Itoa(serviceUID)
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
