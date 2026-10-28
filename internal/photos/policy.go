package photos

import (
	"strings"
	"time"
)

// The photo Policy (ADR 0011). Private libraries belong to one member; an
// administrator sees another member's private library only through an
// unexpired, read-only viewing grant. Every member sees and may add to the
// shared library, but only the uploader or an administrator may change a
// shared photo asset, and only the creator or an administrator may change a
// shared virtual directory. A member's assistant only reads what the member
// owns or shares with everyone (ADR 0024).
//
// Callers turn "cannot view" into ErrNotFound so that private content is not
// revealed by error codes, and "can view but not change" into ErrForbidden.
//
// Within a library p can view, asset rules decide which assets p sees. Each
// is written once, in assetRules, and read two ways: as SQL that every
// listing applies through listedAssets, and on one asset already read
// through seesAsset. A listing that wrote its own conditions could show an
// asset that opening it refuses, or the reverse.

func (p Principal) valid() bool { return p.UserID != "" }

// viewing returns the caller's unexpired grant on a member's private library
// that lasts longest. An assistant never views through a grant.
func (p Principal) viewing(ownerUserID string, now time.Time) (ViewingGrant, bool) {
	if p.Assistant {
		return ViewingGrant{}, false
	}
	var found ViewingGrant
	for _, grant := range p.Viewing {
		if grant.OwnerUserID == ownerUserID && grant.ExpiresAt.After(now) && grant.ExpiresAt.After(found.ExpiresAt) {
			found = grant
		}
	}
	return found, !found.ExpiresAt.IsZero()
}

func canView(p Principal, lib library, now time.Time) bool {
	if lib.kind == LibraryKindShared {
		return true
	}
	if lib.ownerUserID == p.UserID {
		return true
	}
	_, viewing := p.viewing(lib.ownerUserID, now)
	return p.Admin && viewing
}

// viewingOnly reports whether p sees lib only through Administrative Viewing
// Mode.
func viewingOnly(p Principal, lib library) bool {
	return lib.kind == LibraryKindPrivate && lib.ownerUserID != p.UserID
}

// canAdd covers importing into, copying into and creating directories in lib.
func canAdd(p Principal, lib library) bool {
	if p.Assistant {
		return false
	}
	if lib.kind == LibraryKindShared {
		return true
	}
	return lib.ownerUserID == p.UserID
}

// canChange covers renaming, moving, trashing, restoring and purging an item
// that createdBy uploaded or created in lib.
func canChange(p Principal, lib library, createdBy string) bool {
	if p.Assistant {
		return false
	}
	if lib.kind == LibraryKindShared {
		return p.Admin || createdBy == p.UserID
	}
	return lib.ownerUserID == p.UserID
}

// assetRule is one rule of which assets in a viewable library p sees.
type assetRule struct {
	// binds reports whether the rule applies to p.
	binds func(Principal) bool
	// allows reports whether the rule lets p see an asset already read, and
	// allowsSQL is the same as a condition on the assets row named alias.
	allows    func(assetRecord) bool
	allowsSQL func(alias string) string
}

// assistantSeesNoVideos: videos take no part in local AI, and so none in
// what a member's assistant reads (ADR 0024, docs/specs/photo-video.md).
var assistantSeesNoVideos = assetRule{
	binds:     func(p Principal) bool { return p.Assistant },
	allows:    func(record assetRecord) bool { return record.Video == nil },
	allowsSQL: func(alias string) string { return notVideo(alias + ".object_id") },
}

var assetRules = []assetRule{assistantSeesNoVideos}

// seesAsset reports whether every asset rule that binds p lets it see
// record, an asset of a library it can view.
func seesAsset(p Principal, record assetRecord) bool {
	for _, rule := range assetRules {
		if rule.binds(p) && !rule.allows(record) {
			return false
		}
	}
	return true
}

// listedAssets is the SQL condition that the assets row named alias, in a
// library p can view, is one a listing shows p: outside the trash and let
// through by every asset rule that binds p. Listings of a scope that
// canView admitted apply it, including when they recheck ranked hits.
func listedAssets(p Principal, alias string) string {
	conditions := []string{alias + ".trashed_at IS NULL"}
	for _, rule := range assetRules {
		if rule.binds(p) {
			conditions = append(conditions, rule.allowsSQL(alias))
		}
	}
	return strings.Join(conditions, " AND ")
}
