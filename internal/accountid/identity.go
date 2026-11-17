// Package accountid is what the Product Service and the root Host Agent share
// about accounts: Linux identities and roles, the requests that provision
// them, session tokens and the users behind them (ADR 0008). The accounts
// service and its store stay in internal/accounts, which the Host Agent does
// not link (#221).
package accountid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrSessionNotFound = errors.New("session is invalid or expired")
	ErrUserNotFound    = errors.New("user not found")
	// ErrIdentityConflict reports that the host already has an account,
	// group, or UID that A-NAS did not allocate for this user.
	ErrIdentityConflict  = errors.New("username or Linux identity conflicts with an existing host account")
	ErrVolumeUnavailable = errors.New("the data volume is offline")
)

// Linux identity layout shared with the Host Agent (ADR 0008). Fixed group
// GIDs and user UIDs are recorded on the data volume, so they must never change.
const (
	UsersGroup   = "a-nas-users"
	AdminsGroup  = "a-nas-admins"
	UsersGID     = 20000
	AdminsGID    = 20001
	FirstUserUID = 20100
	LastUserUID  = 29999
	// PhotoServiceUser owns the data volume's photos subvolume alone
	// (ADR 0011). Its UID sits outside the account (20100–29999) and app
	// (30000–30999) ranges and is fixed, so a reinstalled system disk owns the
	// same photos again.
	PhotoServiceUser = "a-nas-photos"
	PhotoServiceUID  = 31000
	ShareServiceUser = "a-nas-shares"
	ShareServiceUID  = 31001
	// FamilyServiceUser owns the family store (ADR 0029).
	FamilyServiceUser = "a-nas-family"
	FamilyServiceUID  = 31002
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type UserStatus string

const (
	UserStatusPending  UserStatus = "pending"
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
	UserStatusError    UserStatus = "error"
)

// ViewingDuration is how long Administrative Viewing Mode lasts unless the
// administrator ends it sooner (ADR 0008).
const ViewingDuration = 24 * time.Hour

// ViewingScope is what an Administrative Viewing grant opens: a member's
// private space (through ACL entries the Host Agent adds) or their private
// photo library (enforced by the photo service's Policy, ADR 0011). Each
// scope is granted, audited, notified and ended on its own.
type ViewingScope string

const (
	ViewingScopeSpace   ViewingScope = "space"
	ViewingScopeLibrary ViewingScope = "library"
)

// LibraryViewing is an unexpired grant to view one member's private photo
// library read-only.
type LibraryViewing struct {
	GrantID     string    `json:"grantId"`
	OwnerUserID string    `json:"ownerUserId"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// ViewingRequest asks the Host Agent to give an administrator temporary
// read-only ACL entries on a private space.
type ViewingRequest struct {
	ID            string
	SpaceID       string
	AdminUsername string
	AdminUID      int
	ExpiresAt     time.Time
}

type ViewingProvisioner interface {
	GrantViewing(context.Context, ViewingRequest) error
	RevokeViewing(context.Context, string) error
}

type CredentialRequest struct {
	UserID         string
	PrivateSpaceID string
	Username       string
	Password       string
	Role           Role
	UID            int
	// Enabled is false when an administrator resets the credential of a
	// disabled account; the SMB credential must stay disabled.
	Enabled bool
}

type CredentialProvisioner interface {
	SetCredential(context.Context, CredentialRequest) error
	DisableCredential(context.Context, string) error
}

// EnableRequest gives a disabled account its host access back with the
// password it had. SMB is false while the account must choose a new password:
// its SMB credential and assistant then stay off until it does.
type EnableRequest struct {
	Username string
	SMB      bool
}

// CredentialEnabler undoes DisableCredential.
type CredentialEnabler interface {
	EnableCredential(context.Context, EnableRequest) error
}

// AccountRemoval is a deleted account the Host Agent removes for good
// (docs/specs/account-enable-and-delete.md). TransferTo names the
// administrator who receives the account's personal space and Agent空间
// folder; empty deletes them. Folder is the name the transferred files get in
// that administrator's personal space, with a number added when it is taken.
//
// The Host Agent reads all of it from the control plane, as the photo, share
// and family services read AccountDeletion: of the Product Service's request
// it takes only UserID.
type AccountRemoval struct {
	UserID         string
	Username       string
	UID            int
	PrivateSpaceID string
	TransferTo     string
	TransferToUID  int
	Folder         string
}

// ErrNotDeleting answers a service that asks about an account whose deletion
// has not started.
var ErrNotDeleting = errors.New("the account is not being deleted")

// AccountDeletion is what the Host Agent tells the photo, share and family
// services about an account whose deletion has started, read from the
// control plane: they act on it, never on the Product Service's word. Each
// of PersonalSpace and Library is "transfer" or "delete"; an administrator
// is named only when one of them is a transfer and that administrator still
// administers the device.
type AccountDeletion struct {
	UserID        string `json:"userId"`
	Username      string `json:"username"`
	PersonalSpace string `json:"personalSpace"`
	Library       string `json:"library"`
	AdminID       string `json:"adminId,omitempty"`
	AdminUsername string `json:"adminUsername,omitempty"`
}

// HandoverFolder and HandoverAlbum name what an administrator receives from
// an account deleted with a transfer: a folder in their personal space and an
// album in their private photo library.
func HandoverFolder(username string) string { return username + " 的文件" }

func HandoverAlbum(username string) string { return username + " 的照片" }

// AccountRemover removes an account's host identity and the data kept under
// its name, and returns the folder of the administrator's personal space that
// holds its handed-over files, as named now; empty when nothing was handed
// over. Every step treats what is already gone as done, so a request that
// failed part way is sent again, and it still returns the folder.
type AccountRemover interface {
	RemoveAccount(context.Context, AccountRemoval) (string, error)
}

// Identity is the Linux account the Host Agent keeps for one A-NAS user.
type Identity struct {
	Username string
	UID      int
	Role     Role
	Enabled  bool
}

// IdentitySynchronizer converges host accounts and groups on the control plane
// without changing any password.
type IdentitySynchronizer interface {
	SyncIdentities(context.Context, []Identity) error
}

// SessionUser is what services outside the Product Service learn about the
// signed-in user behind a session token.
type SessionUser struct {
	UserID             string `json:"userId"`
	Username           string `json:"username"`
	Role               Role   `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
	// Viewing lists an administrator's unexpired grants to view members'
	// private photo libraries.
	Viewing []LibraryViewing `json:"viewing,omitempty"`
}

// Profile is how one account appears to everyone signed in: the name to show
// and the picture. It leaves out role and status, which only administrators
// manage.
type Profile struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Nickname      string `json:"nickname,omitempty"`
	AvatarVersion string `json:"avatarVersion,omitempty"`
}

type sessionTokenKey struct{}

// WithSessionToken attaches the caller's session token so the File Broker can
// verify on its own who is asking (ADR 0008).
func WithSessionToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionTokenKey{}, token)
}

// SessionToken returns the token attached by WithSessionToken, or "".
func SessionToken(ctx context.Context) string {
	token, _ := ctx.Value(sessionTokenKey{}).(string)
	return token
}

// TokenHash is how the session table stores a session token.
func TokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
