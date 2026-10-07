package photos

import "time"

// The photo Policy (ADR 0011). Private libraries belong to one member; an
// administrator sees another member's private library only through an
// unexpired, read-only viewing grant. Every member sees and may add to the
// shared library, but only the uploader or an administrator may change a
// shared photo asset, and only the creator or an administrator may change a
// shared virtual directory.
//
// Callers turn "cannot view" into ErrNotFound so that private content is not
// revealed by error codes, and "can view but not change" into ErrForbidden.

func (p Principal) valid() bool { return p.UserID != "" }

func (p Principal) viewing(ownerUserID string, now time.Time) bool {
	for _, grant := range p.Viewing {
		if grant.OwnerUserID == ownerUserID && grant.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}

func canView(p Principal, lib library, now time.Time) bool {
	if lib.kind == LibraryKindShared {
		return true
	}
	return lib.ownerUserID == p.UserID || (p.Admin && p.viewing(lib.ownerUserID, now))
}

// viewingOnly reports whether p sees lib only through Administrative Viewing
// Mode.
func viewingOnly(p Principal, lib library) bool {
	return lib.kind == LibraryKindPrivate && lib.ownerUserID != p.UserID
}

// canAdd covers importing into, copying into and creating directories in lib.
func canAdd(p Principal, lib library) bool {
	if lib.kind == LibraryKindShared {
		return true
	}
	return lib.ownerUserID == p.UserID
}

// canChange covers renaming, moving, trashing, restoring and purging an item
// that createdBy uploaded or created in lib.
func canChange(p Principal, lib library, createdBy string) bool {
	if lib.kind == LibraryKindShared {
		return p.Admin || createdBy == p.UserID
	}
	return lib.ownerUserID == p.UserID
}
