package linux

import (
	"slices"
	"testing"
)

func TestPOSIXACLRoundTripsAndKeepsMasks(t *testing.T) {
	// user::rw-, user:20111:rwx, group::---, mask::rw-, other::---
	extended := []aclEntry{
		{tag: aclUserObj, perm: 6, id: aclUndefinedID},
		{tag: aclUser, perm: 7, id: 20111},
		{tag: aclGroupObj, perm: 0, id: aclUndefinedID},
		{tag: aclMask, perm: 6, id: aclUndefinedID},
		{tag: aclOther, perm: 0, id: aclUndefinedID},
	}
	decoded, err := decodeACL(encodeACL(extended))
	if err != nil || !slices.Equal(decoded, extended) {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	granted := withUser(extended, 20110, aclRead)
	if !hasUser(granted, 20110) {
		t.Fatal("viewer entry missing")
	}
	for _, entry := range granted {
		if entry.tag == aclMask && entry.perm != 6 {
			t.Fatalf("existing mask changed to %o", entry.perm)
		}
	}
	revoked := withoutUser(granted, 20110)
	sorted, _ := decodeACL(encodeACL(revoked))
	if !slices.Equal(sorted, extended) {
		t.Fatalf("revocation did not restore the ACL: %#v", sorted)
	}

	// A file without extended entries keeps its group class unchanged.
	minimal := minimalACL(0o640)
	viewed := withUser(minimal, 20110, aclRead)
	for _, entry := range viewed {
		if entry.tag == aclMask && entry.perm != 4 {
			t.Fatalf("new mask = %o, want the owning group's r-- plus the viewer's r--", entry.perm)
		}
	}
	if _, err := decodeACL([]byte{1, 0, 0, 0}); err == nil {
		t.Fatal("decodeACL accepted an unknown version")
	}
}
