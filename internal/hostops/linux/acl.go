package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// transitionalServiceAccount keeps the Product Service able to read and write
// spaces until the File Broker performs Web file operations as the signed-in
// user (ADR 0008, issue #13). Remove every use of it with that change.
const transitionalServiceAccount = "a-nas"

const sharedSpaceID = "space:shared"

// Space roots, trash directories, and their containers are owned by
// root:root and authorized only by POSIX ACLs. Owners cannot change the ACL of
// a space root; the access and default entries below are the whole policy.

func containerACL() string {
	return strings.Join([]string{
		"user::rwx", "group::---", "other::---",
		"group:" + accounts.UsersGroup + ":--x", "user:" + transitionalServiceAccount + ":--x", "mask::--x",
	}, ",")
}

// inheritedACL grants entries on a directory and, through default entries,
// on everything created below it.
func inheritedACL(entries ...string) string {
	access := append([]string{"user::rwx", "group::---", "other::---", "mask::rwx"}, entries...)
	all := slices.Clone(access)
	for _, entry := range access {
		all = append(all, "default:"+entry)
	}
	return strings.Join(all, ",")
}

func privateSpaceACL(username string) string {
	return inheritedACL("user:"+username+":rwx", "user:"+transitionalServiceAccount+":rwx")
}

// sharedFolderACL lets every A-NAS account read a shared folder and grants
// write access to the listed users or groups (for example "group:a-nas-users").
func sharedFolderACL(writers ...string) string {
	entries := []string{"group:" + accounts.UsersGroup + ":r-x"}
	for _, writer := range writers {
		if writer == "group:"+accounts.UsersGroup {
			entries[0] = writer + ":rwx"
			continue
		}
		entries = append(entries, writer+":rwx")
	}
	return inheritedACL(append(entries, "user:"+transitionalServiceAccount+":rwx")...)
}

// trashRootACL lets the space's users reach their own trash directory without
// listing other users' deleted files.
func trashRootACL(principal string) string {
	return strings.Join([]string{
		"user::rwx", "group::---", "other::---",
		principal + ":--x", "user:" + transitionalServiceAccount + ":rwx", "mask::rwx",
	}, ",")
}

func userTrashACL(username string) string {
	return inheritedACL("user:"+username+":rwx", "user:"+transitionalServiceAccount+":rwx")
}

func (e *Executor) materializeRegisteredSpaces(ctx context.Context) ([]string, error) {
	e.materializeMu.Lock()
	defer e.materializeMu.Unlock()
	var repaired []string
	apply := func(path, acl string, created bool) error {
		changed, existed, err := e.ensureDirectoryACL(ctx, path, acl)
		if changed && existed && !created {
			repaired = append(repaired, path)
		}
		return err
	}
	if err := e.ensureTraversal(ctx, filepath.Dir(e.mountPoint)); err != nil {
		return nil, err
	}
	for _, path := range []string{filepath.Join(e.mountPoint, "spaces"), filepath.Join(e.mountPoint, "spaces", "private")} {
		if err := apply(path, containerACL(), false); err != nil {
			return repaired, err
		}
	}
	e.spaceRootsMu.RLock()
	spaces := make(map[string]string, len(e.spaceRoots))
	for spaceID, root := range e.spaceRoots {
		spaces[spaceID] = root
	}
	e.spaceRootsMu.RUnlock()
	spaceIDs := make([]string, 0, len(spaces))
	for spaceID := range spaces {
		spaceIDs = append(spaceIDs, spaceID)
	}
	slices.Sort(spaceIDs)
	for _, spaceID := range spaceIDs {
		root := spaces[spaceID]
		created, err := e.ensureSpaceSubvolume(ctx, root)
		if err != nil {
			return repaired, err
		}
		spaceACL := sharedFolderACL("group:" + accounts.UsersGroup)
		principal := "group:" + accounts.UsersGroup
		trashUsers := e.identityUsernames()
		if spaceID != sharedSpaceID {
			username := filepath.Base(root)
			if !validUsername(username) {
				return repaired, errors.New("registered private space has an invalid owner")
			}
			spaceACL = privateSpaceACL(username)
			principal = "user:" + username
			trashUsers = []string{username}
		}
		if err := apply(root, spaceACL, created); err != nil {
			return repaired, err
		}
		trashRoot := filepath.Join(root, ".a-nas-trash")
		if err := apply(trashRoot, trashRootACL(principal), false); err != nil {
			return repaired, err
		}
		for _, username := range trashUsers {
			if err := apply(filepath.Join(trashRoot, username), userTrashACL(username), false); err != nil {
				return repaired, err
			}
		}
	}
	return repaired, nil
}

func (e *Executor) ensureSpaceSubvolume(ctx context.Context, root string) (bool, error) {
	if _, err := os.Lstat(root); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if output, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "create", root}, ""); err != nil {
		return false, commandError("create space subvolume", err, output)
	}
	return true, nil
}

// ensureDirectoryACL makes path a root-owned directory with exactly acl. It
// reports whether anything changed and whether the directory already existed,
// so callers can log drift repairs separately from first creation.
func (e *Executor) ensureDirectoryACL(ctx context.Context, path, acl string) (changed, existed bool, err error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(path, 0o700); err != nil {
			return false, false, fmt.Errorf("create %s: %w", path, err)
		}
		if info, err = os.Lstat(path); err != nil {
			return false, false, err
		}
	case err != nil:
		return false, false, err
	default:
		existed = true
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, existed, fmt.Errorf("%s is not a safe directory", path)
	}
	current, err := e.runner.Run(ctx, "getfacl", []string{"--absolute-names", "--omit-header", path}, "")
	if err != nil {
		return false, existed, commandError("read ACL", err, current)
	}
	if rootOwned(info) && info.Mode()&os.ModeSetgid == 0 && sameACL(string(current), acl) {
		return false, existed, nil
	}
	commands := []struct {
		name string
		args []string
	}{
		{name: "chown", args: []string{"root:root", path}},
		{name: "chmod", args: []string{"g-s", path}},
	}
	if !strings.Contains(acl, "default:") {
		// A directory created below a space inherits its default ACL, and
		// setfacl --set without default entries keeps it.
		commands = append(commands, struct {
			name string
			args []string
		}{name: "setfacl", args: []string{"--remove-default", path}})
	}
	commands = append(commands, struct {
		name string
		args []string
	}{name: "setfacl", args: []string{"--set", acl, path}})
	for _, command := range commands {
		if output, err := e.runner.Run(ctx, command.name, command.args, ""); err != nil {
			return false, existed, commandError("protect "+path, err, output)
		}
	}
	return true, existed, nil
}

// ensureTraversal lets A-NAS accounts pass through the directory that holds
// the data-volume mount point; smbd needs it after switching to the user.
func (e *Executor) ensureTraversal(ctx context.Context, path string) error {
	if path == "/" || path == "." {
		return nil
	}
	if output, err := e.runner.Run(ctx, "setfacl", []string{"--modify", "group:" + accounts.UsersGroup + ":--x", path}, ""); err != nil {
		return commandError("allow traversal to the data volume", err, output)
	}
	return nil
}

func rootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0
}

// sameACL compares getfacl output with a setfacl specification as sets of
// entries, ignoring comments such as "#effective:".
func sameACL(getfacl, spec string) bool {
	var current []string
	for _, line := range strings.Split(getfacl, "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line != "" {
			current = append(current, line)
		}
	}
	want := strings.Split(spec, ",")
	slices.Sort(current)
	slices.Sort(want)
	return slices.Equal(current, want)
}
