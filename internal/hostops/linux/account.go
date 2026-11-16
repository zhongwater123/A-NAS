package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// EnableCredential undoes DisableCredential for an account that is enabled
// again with the password it had: it unlocks the Linux account and, unless the
// account must choose a new password first, enables its SMB credential and its
// assistant. Each step is the same when it is already done.
func (e *Executor) EnableCredential(ctx context.Context, request accountid.EnableRequest) error {
	if !accountid.ValidUsername(request.Username) {
		return errors.New("invalid username")
	}
	if e.identityError != nil {
		return e.identityError
	}
	if !e.identityKnown(request.Username) {
		return fmt.Errorf("%w: %s has no A-NAS identity", accountid.ErrUserNotFound, request.Username)
	}
	if err := e.unlockAccount(ctx, request.Username); err != nil {
		return err
	}
	if !request.SMB {
		return nil
	}
	if output, err := e.runner.Run(ctx, "smbpasswd", []string{"-e", request.Username}, ""); err != nil {
		return commandError("enable Samba credential", err, output)
	}
	if err := e.setIdentityEnabled(request.Username, true); err != nil {
		return err
	}
	return errors.Join(e.mirrorIdentityManifest(), e.enableAssistant(ctx, request.Username))
}

// handoverPrefix names the directory a transferred personal space is copied
// into, inside the receiving administrator's personal space: root's alone
// while it is filled, then renamed to the folder the administrator sees.
const handoverPrefix = ".a-nas-handover-"

// RemoveAccount removes a deleted account from the host for good
// (docs/specs/account-enable-and-delete.md): its assistant, its personal
// space (copied into the receiving administrator's personal space first when
// asked), its folders in Shared, its viewing grants, and its Linux, group and
// Samba accounts. Its UID is never reused, so files it left in Shared keep it
// as their owner.
//
// The account leaves the identity and space registries before its folders
// are deleted, so the reconciliation that recreates missing roots and trash
// folders no longer knows them. A request that failed part way is sent
// again; every step treats what is already gone as done.
func (e *Executor) RemoveAccount(ctx context.Context, removal accountid.AccountRemoval) error {
	if err := e.validRemoval(removal); err != nil {
		return err
	}
	if e.identityError != nil {
		return e.identityError
	}
	if e.registryError != nil {
		return e.registryError
	}
	if e.viewingError != nil {
		return e.viewingError
	}
	// Folders are copied or deleted on the volume; with it offline nothing
	// would be, and the registries would forget them.
	if !e.dataVolumeReady() {
		return accountid.ErrVolumeUnavailable
	}
	privateRoot := filepath.Join(e.mountPoint, "spaces", "private", removal.Username)
	if registered, err := e.privateSpaceRoot(removal.PrivateSpaceID); err == nil && registered != privateRoot {
		return errors.New("the private space is registered at another path")
	}
	var adminRoot string
	if removal.TransferTo != "" {
		var err error
		if adminRoot, err = e.handoverRoot(removal); err != nil {
			return err
		}
	}
	// The account is disabled already; make sure nothing of it still runs.
	if err := errors.Join(e.disableSambaIfPresent(ctx, removal.Username), e.disableAssistant(ctx, removal.Username)); err != nil {
		return err
	}
	if err := e.revokeSpaceViewing(ctx, removal.PrivateSpaceID); err != nil {
		return err
	}
	if err := e.forgetIdentity(removal.Username); err != nil {
		return err
	}
	if err := e.unregisterSpace(removal.PrivateSpaceID); err != nil {
		return err
	}
	shared := filepath.Join(e.mountPoint, "spaces", "shared")
	agentFolder := filepath.Join(shared, "Agent空间", removal.Username)
	if adminRoot != "" {
		if err := e.handOver(ctx, removal, adminRoot, privateRoot, agentFolder); err != nil {
			return err
		}
	}
	for _, path := range []string{privateRoot, agentFolder, filepath.Join(shared, ".a-nas-trash", removal.Username)} {
		if err := e.removeTree(ctx, path); err != nil {
			return err
		}
	}
	if err := e.removeAssistant(ctx, removal.Username); err != nil {
		return err
	}
	if err := e.removeHostAccount(ctx, removal.Username, removal.UID); err != nil {
		return err
	}
	return e.applySambaConfiguration(ctx)
}

func (e *Executor) validRemoval(removal accountid.AccountRemoval) error {
	validUID := func(uid int) bool { return uid >= accountid.FirstUserUID && uid <= accountid.LastUserUID }
	if !accountid.ValidUsername(removal.Username) || !validUID(removal.UID) || strings.TrimSpace(removal.PrivateSpaceID) == "" ||
		removal.PrivateSpaceID == sharedSpaceID {
		return errors.New("invalid account removal")
	}
	if removal.TransferTo == "" {
		return nil
	}
	if !accountid.ValidUsername(removal.TransferTo) || removal.TransferTo == removal.Username || !validUID(removal.TransferToUID) ||
		!validFolderName(removal.Folder) {
		return errors.New("invalid account removal")
	}
	return nil
}

// validFolderName accepts a folder name for the handed-over files: one path
// element that is not hidden and not one of the reserved folders.
func validFolderName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && !strings.ContainsAny(name, "/\x00") && !strings.HasPrefix(name, ".") &&
		utf8.ValidString(name) && len(name) <= 200
}

// handoverRoot is the receiving administrator's personal space, which must be
// an enabled administrator's.
func (e *Executor) handoverRoot(removal accountid.AccountRemoval) (string, error) {
	e.identitiesMu.Lock()
	admin, known := e.identities[removal.TransferTo]
	e.identitiesMu.Unlock()
	if !known || admin.Role != accountid.RoleAdmin || !admin.Enabled || admin.UID != removal.TransferToUID {
		return "", errors.New("the personal space can only be handed to an enabled administrator")
	}
	root := filepath.Join(e.mountPoint, "spaces", "private", removal.TransferTo)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("the administrator's personal space is missing: %w", err)
	}
	return root, nil
}

// revokeSpaceViewing ends every viewing grant on a space being removed. A
// grant whose space is already gone only loses its record.
func (e *Executor) revokeSpaceViewing(ctx context.Context, spaceID string) error {
	e.viewingMu.Lock()
	var grants []string
	for id, record := range e.viewing {
		if record.SpaceID == spaceID {
			grants = append(grants, id)
		}
	}
	e.viewingMu.Unlock()
	slices.Sort(grants)
	for _, id := range grants {
		if _, err := e.privateSpaceRoot(spaceID); err != nil {
			if err := e.saveViewing(id, nil); err != nil {
				return err
			}
			continue
		}
		if err := e.RevokeViewing(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// forgetIdentity removes an account from the identity registry and its copy
// on the data volume.
func (e *Executor) forgetIdentity(username string) error {
	e.identitiesMu.Lock()
	_, known := e.identities[username]
	if known {
		delete(e.identities, username)
		if err := e.writeIdentityRegistryLocked(e.identityPath); err != nil {
			e.identitiesMu.Unlock()
			return err
		}
	}
	e.identitiesMu.Unlock()
	return e.mirrorIdentityManifest()
}

func (e *Executor) unregisterSpace(spaceID string) error {
	e.spaceRootsMu.Lock()
	defer e.spaceRootsMu.Unlock()
	if _, ok := e.spaceRoots[spaceID]; !ok {
		return nil
	}
	delete(e.spaceRoots, spaceID)
	contents, err := json.MarshalIndent(e.spaceRoots, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.registryPath, append(contents, '\n'), 0o600)
}

// handOver copies the account's personal space, without its trash, and its
// Agent空间 folder into a new folder of the administrator's personal space.
// The copy shares the files' data on the volume (reflink), so it takes no
// more space. It is made in a directory only root can enter, given to the
// administrator with the access files created there get, and renamed into
// place only after the originals are deleted: a retry then finds either the
// originals or the finished copy, never half of each.
func (e *Executor) handOver(ctx context.Context, removal accountid.AccountRemoval, adminRoot, privateRoot, agentFolder string) error {
	ready := filepath.Join(adminRoot, handoverPrefix+strconv.Itoa(removal.UID))
	partial := ready + ".partial"
	if _, err := os.Lstat(ready); errors.Is(err, os.ErrNotExist) {
		if err := e.copyForHandover(ctx, removal, partial, privateRoot, agentFolder); err != nil {
			return err
		}
		if err := os.Rename(partial, ready); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(ready); errors.Is(err, os.ErrNotExist) {
		// Nothing was left to hand over.
		return nil
	}
	for _, path := range []string{privateRoot, agentFolder} {
		if err := e.removeTree(ctx, path); err != nil {
			return err
		}
	}
	folder, err := freeName(adminRoot, removal.Folder)
	if err != nil {
		return err
	}
	return os.Rename(ready, filepath.Join(adminRoot, folder))
}

// copyForHandover fills partial with what the account leaves, owned by the
// administrator. With nothing left to copy, partial is not made.
func (e *Executor) copyForHandover(ctx context.Context, removal accountid.AccountRemoval, partial, privateRoot, agentFolder string) error {
	if err := e.removeTree(ctx, partial); err != nil {
		return err
	}
	var sources []string
	for _, path := range []string{privateRoot, agentFolder} {
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			sources = append(sources, path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(sources) == 0 {
		return nil
	}
	if err := os.Mkdir(partial, 0o700); err != nil {
		return err
	}
	// It inherits the administrator's default ACL; nobody but root may enter
	// it until it is complete.
	if err := unix.Removexattr(partial, aclDefaultAttribute); err != nil && !errors.Is(err, unix.ENODATA) {
		return err
	}
	if err := unix.Removexattr(partial, aclAccessAttribute); err != nil && !errors.Is(err, unix.ENODATA) {
		return err
	}
	if err := os.Chmod(partial, 0o700); err != nil {
		return err
	}
	if slices.Contains(sources, privateRoot) {
		if err := e.copyTree(ctx, privateRoot, partial); err != nil {
			return err
		}
		// Deleted files stay deleted.
		if err := os.RemoveAll(filepath.Join(partial, ".a-nas-trash")); err != nil {
			return err
		}
	}
	if slices.Contains(sources, agentFolder) {
		name, err := freeName(partial, "Agent空间")
		if err != nil {
			return err
		}
		target := filepath.Join(partial, name)
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		if err := e.copyTree(ctx, agentFolder, target); err != nil {
			return err
		}
	}
	return adoptTree(partial, uint32(removal.TransferToUID))
}

// copyTree copies the contents of source into target. Links are copied as
// links, never followed; data is shared with the original.
func (e *Executor) copyTree(ctx context.Context, source, target string) error {
	if output, err := e.runner.Run(ctx, "cp", []string{
		"--recursive", "--no-dereference", "--reflink=always", "--preserve=timestamps,links", "--", source + "/.", target + "/",
	}, ""); err != nil {
		return commandError("copy files for the administrator", err, output)
	}
	return nil
}

// freeName returns name, or name with a number after it, whichever is not
// taken in directory.
func freeName(directory, name string) (string, error) {
	for n := 1; n < 1000; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)", name, n)
		}
		if _, err := os.Lstat(filepath.Join(directory, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no free name for %q", name)
}

// The access a handed-over file gets: what a file the administrator creates
// in their personal space inherits from its default ACL (privateSpaceACL),
// with the modes Web copies use (0770 folders, 0660 files).
func handoverACL(uid uint32, directory bool) []aclEntry {
	perm := uint16(6)
	if directory {
		perm = 7
	}
	return []aclEntry{
		{tag: aclUserObj, perm: perm, id: aclUndefinedID},
		{tag: aclUser, perm: 7, id: uid},
		{tag: aclGroupObj, perm: 0, id: aclUndefinedID},
		{tag: aclMask, perm: perm, id: aclUndefinedID},
		{tag: aclOther, perm: 0, id: aclUndefinedID},
	}
}

// adoptTree gives the tree at root to uid as files created in its personal
// space are: owned by it and its group, with the access the space's default
// ACL gives. Only folders, files and links are kept. Nobody else can enter
// the tree meanwhile; the walk still follows no link.
func adoptTree(root string, uid uint32) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: root, Err: err}
	}
	return adoptDirectory(fd, uid)
}

// adoptDirectory adopts the directory open at fd and its children, and closes
// fd.
func adoptDirectory(fd int, uid uint32) error {
	directory := os.NewFile(uintptr(fd), "")
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			if err := adoptDirectory(child, uid); err != nil {
				return err
			}
		case unix.S_IFREG:
			child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			err = adoptEntry(child, uid, false)
			unix.Close(child)
			if err != nil {
				return err
			}
		case unix.S_IFLNK:
			if err := unix.Fchownat(fd, name, int(uid), int(uid), unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
		default:
			// Pipes, sockets and device files hold no data to keep.
			if err := unix.Unlinkat(fd, name, 0); err != nil {
				return err
			}
		}
	}
	return adoptEntry(fd, uid, true)
}

func adoptEntry(fd int, uid uint32, directory bool) error {
	mode := uint32(0o660)
	if directory {
		mode = 0o770
	}
	if err := unix.Fchown(fd, int(uid), int(uid)); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return err
	}
	acl := encodeACL(handoverACL(uid, directory))
	if err := unix.Fsetxattr(fd, aclAccessAttribute, acl, 0); err != nil {
		return err
	}
	if directory {
		return unix.Fsetxattr(fd, aclDefaultAttribute, encodeACL(handoverACL(uid, true)), 0)
	}
	return nil
}

// removeTree deletes a directory tree on the data volume, a subvolume with
// btrfs. A missing tree is already removed.
func (e *Executor) removeTree(ctx context.Context, path string) error {
	if !withinDirectory(e.mountPoint, path) || filepath.Clean(path) == filepath.Clean(e.mountPoint) {
		return errors.New("refusing to remove a path outside the data volume")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	if isSubvolume(path) {
		// A subvolume a member made inside it keeps btrfs from deleting it
		// at once; removing it file by file still works.
		if _, err := e.runner.Run(ctx, "btrfs", []string{"subvolume", "delete", path}, ""); err == nil {
			return nil
		}
	}
	// RemoveAll walks with directory descriptors and follows no link.
	return os.RemoveAll(path)
}

// isSubvolume reports whether path is the root of a btrfs subvolume, whose
// inode number is always 256.
func isSubvolume(path string) bool {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil || uint64(stats.Type) != unix.BTRFS_SUPER_MAGIC {
		return false
	}
	var stat unix.Stat_t
	return unix.Lstat(path, &stat) == nil && stat.Ino == 256
}

// removeAssistant removes the account's assistant for good: its units stop
// and lose their mask, so an account given the name later starts clean, and
// its data, secret and settings are deleted, as assistant-layout.sh remove
// does.
func (e *Executor) removeAssistant(ctx context.Context, username string) error {
	for _, path := range []string{
		filepath.Join(e.mountPoint, "assistant", username),
		filepath.Join(e.mountPoint, "assistant", ".changes", username),
	} {
		if err := e.removeTree(ctx, path); err != nil {
			return err
		}
	}
	// The cron waker's own run directory is in /run, which this sandbox
	// cannot write; it goes at the next boot.
	for _, path := range []string{
		e.assistantTokenPath(username),
		filepath.Join(e.systemRoot, "etc", "a-nas-assistant", "members", username),
		filepath.Join(e.systemRoot, "run", "a-nas", "assistant-runs", username),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	units, err := e.assistantUnits(username, assistantTemplates)
	if err != nil || len(units) == 0 {
		return err
	}
	if output, err := e.runner.Run(ctx, "systemctl", append([]string{"unmask"}, units...), ""); err != nil {
		return commandError("unmask assistant units", err, output)
	}
	return nil
}

// removeHostAccount deletes the account's Samba entry, Linux account and
// private group, but only when they are the ones A-NAS made for it.
func (e *Executor) removeHostAccount(ctx context.Context, username string, uid int) error {
	if present, err := e.sambaAccountPresent(ctx, username); err != nil {
		return err
	} else if present {
		if output, err := e.runner.Run(ctx, "smbpasswd", []string{"-x", username}, ""); err != nil {
			return commandError("remove Samba account", err, output)
		}
	}
	id := strconv.Itoa(uid)
	fields, found, err := e.getent(ctx, "passwd", username)
	if err != nil {
		return err
	}
	if found {
		if len(fields) < 4 || fields[2] != id || fields[3] != id {
			return fmt.Errorf("%w: %s", accountid.ErrIdentityConflict, username)
		}
		if output, err := e.runner.Run(ctx, "userdel", []string{username}, ""); err != nil {
			return commandError("remove account", err, output)
		}
	}
	group, found, err := e.getent(ctx, "group", username)
	if err != nil || !found {
		return err
	}
	if len(group) < 3 || group[2] != id {
		return fmt.Errorf("%w: group %s", accountid.ErrIdentityConflict, username)
	}
	if output, err := e.runner.Run(ctx, "groupdel", []string{username}, ""); err != nil {
		return commandError("remove account group", err, output)
	}
	return nil
}

var (
	_ accountid.CredentialEnabler = (*Executor)(nil)
	_ accountid.AccountRemover    = (*Executor)(nil)
)
