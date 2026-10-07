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
	"time"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const sharedSpaceID = "space:shared"

// Space roots, trash directories, and their containers are owned by
// root:root and authorized only by POSIX ACLs. Owners cannot change the ACL of
// a space root; the access and default entries below are the whole policy.

func containerACL() string {
	return strings.Join([]string{
		"user::rwx", "group::---", "other::---",
		"group:" + accounts.UsersGroup + ":--x", "mask::--x",
	}, ",")
}

func rootOnlyACL() string {
	return strings.Join([]string{"user::rwx", "group::---", "other::---"}, ",")
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

// privateSpaceACL grants the owner, plus read-only access for administrators
// in Administrative Viewing Mode.
func privateSpaceACL(username string, viewers ...string) string {
	return inheritedACL(append([]string{"user:" + username + ":rwx"}, viewerEntries(viewers)...)...)
}

func viewerEntries(viewers []string) []string {
	entries := make([]string, 0, len(viewers))
	for _, viewer := range viewers {
		entries = append(entries, "user:"+viewer+":r-x")
	}
	return entries
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
	return inheritedACL(entries...)
}

// trashRootACL lets the space's users reach their own trash directory without
// listing other users' deleted files.
func trashRootACL(principal string, viewers ...string) string {
	mask := "mask::--x"
	if len(viewers) > 0 {
		mask = "mask::r-x"
	}
	entries := append([]string{"user::rwx", "group::---", "other::---", principal + ":--x"}, viewerEntries(viewers)...)
	return strings.Join(append(entries, mask), ",")
}

func userTrashACL(username string, viewers ...string) string {
	return inheritedACL(append([]string{"user:" + username + ":rwx"}, viewerEntries(viewers)...)...)
}

func (e *Executor) materializeRegisteredSpaces(ctx context.Context) ([]string, error) {
	e.materializeMu.Lock()
	defer e.materializeMu.Unlock()
	var repaired []string
	apply := func(path, acl string, created, reserved bool) error {
		changed, existed, err := e.ensureDirectoryACL(ctx, path, acl, reserved)
		if changed && existed && !created {
			repaired = append(repaired, path)
		}
		return err
	}
	// The ACLs below name the fixed groups. A volume from an earlier release
	// reaches this at Host Agent startup, before the Product Service has
	// synchronized any identity.
	if err := e.ensureFixedGroups(ctx); err != nil {
		return nil, err
	}
	if err := e.ensureTraversal(ctx, filepath.Dir(e.mountPoint)); err != nil {
		return nil, err
	}
	for _, path := range []string{filepath.Join(e.mountPoint, "spaces"), filepath.Join(e.mountPoint, "spaces", "private")} {
		if err := apply(path, containerACL(), false, false); err != nil {
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
		var viewers []string
		if spaceID != sharedSpaceID {
			username := filepath.Base(root)
			if !validUsername(username) {
				return repaired, errors.New("registered private space has an invalid owner")
			}
			if e.identityKnown(username) {
				viewers = e.knownIdentities(e.viewersOf(spaceID))
				spaceACL = privateSpaceACL(username, viewers...)
				principal = "user:" + username
				trashUsers = []string{username}
			} else {
				// On the first ADR 0008 boot, the space registry already
				// names the product account but the Product Service has not
				// synchronized its Linux identity yet. setfacl rejects a
				// named entry for that missing account. Protect the space
				// fail-closed until SyncIdentities reapplies its full ACL.
				spaceACL = inheritedACL()
				principal = ""
				trashUsers = nil
			}
		}
		if err := apply(root, spaceACL, created, false); err != nil {
			return repaired, err
		}
		// Trash directories sit in folders their users can write, so
		// anything else found at these names is moved aside.
		trashRoot := filepath.Join(root, ".a-nas-trash")
		trashACL := rootOnlyACL()
		if principal != "" {
			trashACL = trashRootACL(principal, viewers...)
		}
		if err := apply(trashRoot, trashACL, false, true); err != nil {
			return repaired, err
		}
		for _, username := range trashUsers {
			if err := apply(filepath.Join(trashRoot, username), userTrashACL(username, viewers...), false, true); err != nil {
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
// reports whether anything changed and whether something already existed at
// path, so callers can log drift repairs separately from first creation.
//
// Every command acts on the directory opened without following symbolic
// links, never on path again: whoever can rename entries next to it cannot
// redirect root to another file between the check and the change.
func (e *Executor) ensureDirectoryACL(ctx context.Context, path, acl string, reserved bool) (changed, existed bool, err error) {
	directory, existed, err := e.openVolumeDirectory(path, 0o700, reserved)
	if err != nil {
		return false, existed, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return false, existed, err
	}
	target := openedPath(directory)
	current, err := e.runner.Run(ctx, "getfacl", []string{"--absolute-names", "--omit-header", target}, "")
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
		{name: "chown", args: []string{"root:root", target}},
		{name: "chmod", args: []string{"g-s", target}},
	}
	if !strings.Contains(acl, "default:") {
		// A directory created below a space inherits its default ACL, and
		// setfacl --set without default entries keeps it.
		commands = append(commands, struct {
			name string
			args []string
		}{name: "setfacl", args: []string{"--remove-default", target}})
	}
	commands = append(commands, struct {
		name string
		args []string
	}{name: "setfacl", args: []string{"--set", acl, target}})
	for _, command := range commands {
		if output, err := e.runner.Run(ctx, command.name, command.args, ""); err != nil {
			return false, existed, commandError("protect "+path, err, output)
		}
	}
	return true, existed, nil
}

// openVolumeDirectory opens the directory at path below the data-volume mount
// point without following symbolic links, creating it with mode when nothing
// is there. existed reports whether anything was at path. When reserved is
// set, a non-directory at path, such as a symbolic link planted by a user who
// can write the parent, is renamed aside and replaced by a new directory.
func (e *Executor) openVolumeDirectory(path string, mode uint32, reserved bool) (*os.File, bool, error) {
	relative, err := filepath.Rel(e.mountPoint, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return nil, false, fmt.Errorf("%s is not below the data volume", path)
	}
	unsafe := fmt.Errorf("%s is not a safe directory", path)
	mount, err := os.Open(e.mountPoint)
	if err != nil {
		return nil, false, err
	}
	defer mount.Close()
	parentFD, err := unix.Openat2(int(mount.Fd()), filepath.Dir(relative), &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EXDEV) {
		return nil, false, unsafe
	}
	if err != nil {
		return nil, false, fmt.Errorf("open the parent of %s: %w", path, err)
	}
	defer unix.Close(parentFD)
	name := filepath.Base(relative)
	existed := true
	// A concurrent writer of the parent can keep changing the entry; give up
	// after a few rounds rather than loop.
	for attempt := range 3 {
		fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case err == nil:
			return os.NewFile(uintptr(fd), path), existed, nil
		case errors.Is(err, unix.ENOENT):
			existed = existed && attempt > 0
			if err := unix.Mkdirat(parentFD, name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, false, fmt.Errorf("create %s: %w", path, err)
			}
		case (errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)) && reserved:
			aside := fmt.Sprintf("%s.displaced-%d", name, time.Now().UnixNano())
			if err := unix.Renameat2(parentFD, name, parentFD, aside, unix.RENAME_NOREPLACE); err != nil && !errors.Is(err, unix.ENOENT) {
				return nil, true, fmt.Errorf("move aside %s: %w", path, err)
			}
		case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
			return nil, true, unsafe
		default:
			return nil, existed, fmt.Errorf("open %s: %w", path, err)
		}
	}
	return nil, existed, unsafe
}

// openedPath names an open file for a command the Host Agent runs. The kernel
// resolves it to the opened inode instead of walking a path again.
func openedPath(file *os.File) string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd())
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
