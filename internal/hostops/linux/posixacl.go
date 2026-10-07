package linux

import (
	"encoding/binary"
	"errors"
	"slices"
)

// POSIX ACLs as the kernel stores them in the system.posix_acl_* extended
// attributes (include/uapi/linux/posix_acl_xattr.h).
const (
	aclAccessAttribute  = "system.posix_acl_access"
	aclDefaultAttribute = "system.posix_acl_default"
	aclXattrVersion     = 2
	aclUndefinedID      = 0xffffffff

	aclUserObj  = 0x01
	aclUser     = 0x02
	aclGroupObj = 0x04
	aclGroup    = 0x08
	aclMask     = 0x10
	aclOther    = 0x20

	aclRead    = 4
	aclExecute = 1
)

type aclEntry struct {
	tag  uint16
	perm uint16
	id   uint32
}

func decodeACL(data []byte) ([]aclEntry, error) {
	if len(data) < 4 || (len(data)-4)%8 != 0 || binary.LittleEndian.Uint32(data) != aclXattrVersion {
		return nil, errors.New("malformed POSIX ACL")
	}
	entries := make([]aclEntry, 0, (len(data)-4)/8)
	for offset := 4; offset < len(data); offset += 8 {
		entries = append(entries, aclEntry{
			tag:  binary.LittleEndian.Uint16(data[offset:]),
			perm: binary.LittleEndian.Uint16(data[offset+2:]),
			id:   binary.LittleEndian.Uint32(data[offset+4:]),
		})
	}
	return entries, nil
}

func encodeACL(entries []aclEntry) []byte {
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b aclEntry) int {
		if a.tag != b.tag {
			return int(a.tag) - int(b.tag)
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	data := make([]byte, 4+8*len(sorted))
	binary.LittleEndian.PutUint32(data, aclXattrVersion)
	for i, entry := range sorted {
		offset := 4 + 8*i
		binary.LittleEndian.PutUint16(data[offset:], entry.tag)
		binary.LittleEndian.PutUint16(data[offset+2:], entry.perm)
		binary.LittleEndian.PutUint32(data[offset+4:], entry.id)
	}
	return data
}

// minimalACL is the ACL equivalent of a file mode without extended entries.
func minimalACL(mode uint32) []aclEntry {
	return []aclEntry{
		{tag: aclUserObj, perm: uint16(mode >> 6 & 7), id: aclUndefinedID},
		{tag: aclGroupObj, perm: uint16(mode >> 3 & 7), id: aclUndefinedID},
		{tag: aclOther, perm: uint16(mode & 7), id: aclUndefinedID},
	}
}

// withUser sets the named-user entry for uid to perm. An existing mask is
// left unchanged so nobody else's effective access changes; an ACL without a
// mask gets one covering the owning group and the new entry, which leaves the
// owning group's access as it was.
func withUser(entries []aclEntry, uid uint32, perm uint16) []aclEntry {
	result := withoutUser(entries, uid)
	hasMask := false
	var groupPerm uint16
	for _, entry := range result {
		switch entry.tag {
		case aclMask:
			hasMask = true
		case aclGroupObj:
			groupPerm = entry.perm
		}
	}
	result = append(result, aclEntry{tag: aclUser, perm: perm, id: uid})
	if !hasMask {
		result = append(result, aclEntry{tag: aclMask, perm: groupPerm | perm, id: aclUndefinedID})
	}
	return result
}

// withoutUser removes the named-user entry for uid, if any.
func withoutUser(entries []aclEntry, uid uint32) []aclEntry {
	return slices.DeleteFunc(slices.Clone(entries), func(entry aclEntry) bool {
		return entry.tag == aclUser && entry.id == uid
	})
}

func hasUser(entries []aclEntry, uid uint32) bool {
	return slices.ContainsFunc(entries, func(entry aclEntry) bool { return entry.tag == aclUser && entry.id == uid })
}
