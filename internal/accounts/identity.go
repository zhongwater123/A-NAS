package accounts

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
)

// The Linux identity contract lives in internal/accountid, and the root
// reader of sessions in internal/sessiondir, so the Host Agent links neither
// this package nor its store (#221); these names keep this package's callers
// as they were.
type (
	Role                  = accountid.Role
	UserStatus            = accountid.UserStatus
	Identity              = accountid.Identity
	IdentitySynchronizer  = accountid.IdentitySynchronizer
	CredentialRequest     = accountid.CredentialRequest
	CredentialProvisioner = accountid.CredentialProvisioner
	ViewingScope          = accountid.ViewingScope
	ViewingRequest        = accountid.ViewingRequest
	ViewingProvisioner    = accountid.ViewingProvisioner
	LibraryViewing        = accountid.LibraryViewing
	SessionUser           = accountid.SessionUser
	Profile               = accountid.Profile
	SessionDirectory      = sessiondir.Directory
)

const (
	RoleAdmin           = accountid.RoleAdmin
	RoleMember          = accountid.RoleMember
	UserStatusPending   = accountid.UserStatusPending
	UserStatusActive    = accountid.UserStatusActive
	UserStatusDisabled  = accountid.UserStatusDisabled
	UserStatusError     = accountid.UserStatusError
	ViewingScopeSpace   = accountid.ViewingScopeSpace
	ViewingScopeLibrary = accountid.ViewingScopeLibrary
	ViewingDuration     = accountid.ViewingDuration
	UsersGroup          = accountid.UsersGroup
	AdminsGroup         = accountid.AdminsGroup
	UsersGID            = accountid.UsersGID
	AdminsGID           = accountid.AdminsGID
	FirstUserUID        = accountid.FirstUserUID
	LastUserUID         = accountid.LastUserUID
	PhotoServiceUser    = accountid.PhotoServiceUser
	PhotoServiceUID     = accountid.PhotoServiceUID
	ShareServiceUser    = accountid.ShareServiceUser
	ShareServiceUID     = accountid.ShareServiceUID
	FamilyServiceUser   = accountid.FamilyServiceUser
	FamilyServiceUID    = accountid.FamilyServiceUID
)

var (
	ErrSessionNotFound   = accountid.ErrSessionNotFound
	ErrUserNotFound      = accountid.ErrUserNotFound
	ErrIdentityConflict  = accountid.ErrIdentityConflict
	ErrVolumeUnavailable = accountid.ErrVolumeUnavailable
)

// WithSessionToken is accountid.WithSessionToken.
func WithSessionToken(ctx context.Context, token string) context.Context {
	return accountid.WithSessionToken(ctx, token)
}

// SessionToken is accountid.SessionToken.
func SessionToken(ctx context.Context) string { return accountid.SessionToken(ctx) }

// NewSessionDirectory is sessiondir.New.
func NewSessionDirectory(path string) *SessionDirectory { return sessiondir.New(path) }
