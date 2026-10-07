package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// viewingRecord is an Administrative Viewing Mode grant the Host Agent must
// revoke on time, including after a restart.
type viewingRecord struct {
	SpaceID   string    `json:"spaceId"`
	Admin     string    `json:"admin"`
	UID       int       `json:"uid"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// GrantViewing gives an administrator read-only ACL entries on every file in
// a member's private space until the grant expires (ADR 0008). Files the
// owner creates meanwhile inherit the entry through the default ACL.
func (e *Executor) GrantViewing(ctx context.Context, request accounts.ViewingRequest) error {
	if e.viewingError != nil {
		return e.viewingError
	}
	if strings.TrimSpace(request.ID) == "" || !validUsername(request.AdminUsername) ||
		request.AdminUID < accounts.FirstUserUID || request.AdminUID > accounts.LastUserUID {
		return errors.New("invalid viewing request")
	}
	now := e.now().UTC()
	if !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(accounts.ViewingDuration+time.Hour)) {
		return errors.New("viewing request has an invalid expiry")
	}
	e.identitiesMu.Lock()
	identity, known := e.identities[request.AdminUsername]
	e.identitiesMu.Unlock()
	if !known || identity.Role != accounts.RoleAdmin || identity.UID != request.AdminUID || !identity.Enabled {
		return errors.New("viewing is only granted to enabled A-NAS administrators")
	}
	root, err := e.privateSpaceRoot(request.SpaceID)
	if err != nil {
		return err
	}
	if filepathBase(root) == request.AdminUsername {
		return errors.New("an administrator cannot view their own private space")
	}
	// Record first, so a crash part-way through is still revoked on time.
	if err := e.saveViewing(request.ID, &viewingRecord{
		SpaceID: request.SpaceID, Admin: request.AdminUsername, UID: request.AdminUID, ExpiresAt: request.ExpiresAt,
	}); err != nil {
		return err
	}
	if !e.dataVolumeReady() {
		return nil
	}
	if err := applyViewerTree(root, uint32(request.AdminUID), true); err != nil {
		return fmt.Errorf("grant viewing ACL: %w", err)
	}
	_, err = e.materializeRegisteredSpaces(ctx)
	return err
}

// RevokeViewing removes a grant's ACL entries. With the data volume offline
// the grant is marked expired and revoked once the volume returns.
func (e *Executor) RevokeViewing(ctx context.Context, id string) error {
	if e.viewingError != nil {
		return e.viewingError
	}
	e.viewingMu.Lock()
	record, ok := e.viewing[id]
	e.viewingMu.Unlock()
	if !ok {
		return nil
	}
	if !e.dataVolumeReady() {
		record.ExpiresAt = time.Time{}
		return e.saveViewing(id, &record)
	}
	root, err := e.privateSpaceRoot(record.SpaceID)
	if err != nil {
		return err
	}
	if err := applyViewerTree(root, uint32(record.UID), false); err != nil {
		return fmt.Errorf("revoke viewing ACL: %w", err)
	}
	if err := e.saveViewing(id, nil); err != nil {
		return err
	}
	_, err = e.materializeRegisteredSpaces(ctx)
	return err
}

// ExpireViewing revokes every grant whose time has passed. The Host Agent
// calls it at startup and every minute.
func (e *Executor) ExpireViewing(ctx context.Context) ([]string, error) {
	if e.viewingError != nil {
		return nil, e.viewingError
	}
	if !e.dataVolumeReady() {
		return nil, nil
	}
	now := e.now().UTC()
	e.viewingMu.Lock()
	var expired []string
	for id, record := range e.viewing {
		if !record.ExpiresAt.After(now) {
			expired = append(expired, id)
		}
	}
	e.viewingMu.Unlock()
	slices.Sort(expired)
	var failures []error
	var revoked []string
	for _, id := range expired {
		if err := e.RevokeViewing(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
			continue
		}
		revoked = append(revoked, id)
	}
	return revoked, errors.Join(failures...)
}

// viewersOf lists administrators with an unexpired grant on a space.
func (e *Executor) viewersOf(spaceID string) []string {
	now := e.now().UTC()
	e.viewingMu.Lock()
	defer e.viewingMu.Unlock()
	var viewers []string
	for _, record := range e.viewing {
		if record.SpaceID == spaceID && record.ExpiresAt.After(now) && !slices.Contains(viewers, record.Admin) {
			viewers = append(viewers, record.Admin)
		}
	}
	slices.Sort(viewers)
	return viewers
}

func (e *Executor) privateSpaceRoot(spaceID string) (string, error) {
	if spaceID == sharedSpaceID {
		return "", errors.New("shared spaces are readable by every account")
	}
	e.spaceRootsMu.RLock()
	root, ok := e.spaceRoots[spaceID]
	e.spaceRootsMu.RUnlock()
	if !ok {
		return "", errors.New("unknown private space")
	}
	return root, nil
}

func (e *Executor) loadViewingRegistry() error {
	contents, err := os.ReadFile(e.viewingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, &e.viewing); err != nil {
		return fmt.Errorf("decode viewing registry: %w", err)
	}
	return nil
}

// saveViewing stores or, with a nil record, deletes a grant.
func (e *Executor) saveViewing(id string, record *viewingRecord) error {
	e.viewingMu.Lock()
	defer e.viewingMu.Unlock()
	if record == nil {
		delete(e.viewing, id)
	} else {
		e.viewing[id] = *record
	}
	contents, err := json.MarshalIndent(e.viewing, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.viewingPath, append(contents, '\n'), 0o600)
}

func filepathBase(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// applyViewerTree adds (grant) or removes the read-only entry for uid on the
// directory tree at root. The walk uses descriptors opened with O_NOFOLLOW,
// so a member cannot swap in a symlink and make root change the ACL of a file
// outside the space. Masks are kept, so nobody else's access changes.
func applyViewerTree(root string, uid uint32, grant bool) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: root, Err: err}
	}
	return applyViewerDirectory(fd, uid, grant)
}

// applyViewerDirectory updates the directory open at fd, then its children,
// and closes fd.
func applyViewerDirectory(fd int, uid uint32, grant bool) error {
	directory := os.NewFile(uintptr(fd), "")
	defer directory.Close()
	if err := updateViewerACL(fd, uid, grant, true); err != nil {
		return err
	}
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				continue
			}
			if err != nil {
				return err
			}
			if err := applyViewerDirectory(child, uid, grant); err != nil {
				return err
			}
		case unix.S_IFREG:
			child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOATIME|unix.O_CLOEXEC, 0)
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
				continue
			}
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			err = unix.Fstat(child, &opened)
			if err == nil && opened.Mode&unix.S_IFMT == unix.S_IFREG {
				err = updateViewerACL(child, uid, grant, false)
			}
			unix.Close(child)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func updateViewerACL(fd int, uid uint32, grant, directory bool) error {
	perm := uint16(aclRead)
	if directory {
		perm |= aclExecute
	}
	if err := updateACLAttribute(fd, aclAccessAttribute, uid, perm, grant, true); err != nil {
		return err
	}
	if directory {
		return updateACLAttribute(fd, aclDefaultAttribute, uid, perm, grant, false)
	}
	return nil
}

// updateACLAttribute edits one ACL attribute. A missing access ACL is the
// file mode; a missing default ACL stays missing.
func updateACLAttribute(fd int, attribute string, uid uint32, perm uint16, grant, access bool) error {
	buffer := make([]byte, 4+8*64)
	size, err := unix.Fgetxattr(fd, attribute, buffer)
	if errors.Is(err, unix.ERANGE) {
		if size, err = unix.Fgetxattr(fd, attribute, nil); err == nil {
			buffer = make([]byte, size)
			size, err = unix.Fgetxattr(fd, attribute, buffer)
		}
	}
	var entries []aclEntry
	switch {
	case errors.Is(err, unix.ENODATA):
		if !access || !grant {
			return nil
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		entries = minimalACL(stat.Mode & 0o777)
	case err != nil:
		return err
	default:
		if entries, err = decodeACL(buffer[:size]); err != nil {
			return err
		}
	}
	if grant {
		entries = withUser(entries, uid, perm)
	} else if hasUser(entries, uid) {
		entries = withoutUser(entries, uid)
	} else {
		return nil
	}
	return unix.Fsetxattr(fd, attribute, encodeACL(entries), 0)
}
