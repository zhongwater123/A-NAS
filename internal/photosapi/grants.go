package photosapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The montage center reaches members' videos through project grants that
// the photo library holds (ADR 0038 decision 3), on a socket only its
// account can connect to. HTTP cannot pass a descriptor, so the protocol is
// unixmsg: one request and one answer per connection, the original's
// descriptor beside the answer to open. These types are both ends'.

// Grant operations.
const (
	GrantRegister = "register"
	GrantLookup   = "lookup"
	GrantOpen     = "open"
	GrantRemove   = "remove"
	GrantList     = "list"
	GrantRevoke   = "revoke"
)

// Grant error codes. GrantOwnerInactive is a grant whose owner's account is
// not in use now, as while an administrator has disabled it: the grant
// works again once the account does, so the montage center waits rather
// than giving the project up, as it does on GrantNotFound.
const (
	GrantInvalid        = "invalid_request"
	GrantUnauthorized   = "authentication_required"
	GrantNotFound       = "grant_not_found"
	GrantOwnerInactive  = "owner_inactive"
	GrantAssetsRefused  = "assets_not_grantable"
	GrantTooMany        = "too_many_assets"
	GrantAssetNotFound  = "not_found"
	GrantUnavailable    = "photos_unavailable"
	grantMessageLimit   = 256 << 10
	grantRequestTimeout = time.Minute
)

// GrantListPage is how many grants one list answer carries at most. A grant
// encodes to under 1 KiB even when every byte of its 128-byte project ID
// needs escaping, so a page stays well inside GrantMessageLimit.
const GrantListPage = 200

// NewGrantPrincipal builds the photo Principal the montage center acts as
// for a member under a project grant: it sees videos and opens originals,
// adds only to the member's private library and changes nothing else, and
// never acts as an administrator or through a viewing grant.
func NewGrantPrincipal(userID, username string) photos.Principal {
	return photos.Principal{UserID: userID, Username: username, Grant: true}
}

// GrantRequest is one request on the grant socket. Session is the member's
// session token, on register only: it is used for that request and not kept.
// After, on list, is the Next of the page before.
type GrantRequest struct {
	Op        string   `json:"op"`
	Session   string   `json:"session,omitempty"`
	ProjectID string   `json:"projectId,omitempty"`
	GrantID   string   `json:"grantId,omitempty"`
	AssetIDs  []string `json:"assetIds,omitempty"`
	AssetID   string   `json:"assetId,omitempty"`
	After     string   `json:"after,omitempty"`
}

// GrantResponse answers a GrantRequest. Next, on list, is set when more
// grants may follow: the next page is asked for after it.
type GrantResponse struct {
	OK       bool                  `json:"ok"`
	Code     string                `json:"code,omitempty"`
	Message  string                `json:"message,omitempty"`
	Grant    *photos.Grant         `json:"grant,omitempty"`
	Assets   []photos.GrantedAsset `json:"assets,omitempty"`
	Grants   []photos.Grant        `json:"grants,omitempty"`
	Next     string                `json:"next,omitempty"`
	ObjectID string                `json:"objectId,omitempty"`
}

// GrantError is a refused request.
type GrantError struct{ Code, Message string }

func (e *GrantError) Error() string { return "photo grant: " + e.Code + ": " + e.Message }

// GrantErrorCode is err's code, or "" when the request did not reach an
// answer.
func GrantErrorCode(err error) string {
	if refused, ok := errors.AsType[*GrantError](err); ok {
		return refused.Code
	}
	return ""
}

// GrantClient calls the photo library's grant socket.
type GrantClient struct {
	Socket string
}

func (c GrantClient) call(ctx context.Context, request GrantRequest) (GrantResponse, *os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, grantRequestTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	unix := conn.(*net.UnixConn)
	if err := unixmsg.Write(unix, request, nil, grantMessageLimit); err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant request: %w", err)
	}
	var response GrantResponse
	file, err := unixmsg.Read(unix, &response, grantMessageLimit)
	if err != nil {
		return GrantResponse{}, nil, fmt.Errorf("photo grant answer: %w", err)
	}
	if !response.OK {
		unixmsg.CloseFile(file)
		return GrantResponse{}, nil, &GrantError{Code: response.Code, Message: response.Message}
	}
	return response, file, nil
}

func (c GrantClient) plain(ctx context.Context, request GrantRequest) (GrantResponse, error) {
	response, file, err := c.call(ctx, request)
	unixmsg.CloseFile(file)
	return response, err
}

// Register registers, or extends, the grant of a member's project with
// assetIDs, as the member signed in with session.
func (c GrantClient) Register(ctx context.Context, session, projectID string, assetIDs []string) (photos.Grant, error) {
	response, err := c.plain(ctx, GrantRequest{Op: GrantRegister, Session: session, ProjectID: projectID, AssetIDs: assetIDs})
	if err != nil {
		return photos.Grant{}, err
	}
	if response.Grant == nil {
		return photos.Grant{}, errors.New("photo grant: register answered no grant")
	}
	return *response.Grant, nil
}

// Lookup says where each asset of the grant stands.
func (c GrantClient) Lookup(ctx context.Context, grantID string, assetIDs []string) ([]photos.GrantedAsset, error) {
	response, err := c.plain(ctx, GrantRequest{Op: GrantLookup, GrantID: grantID, AssetIDs: assetIDs})
	return response.Assets, err
}

// Open returns a granted video's original, read-only, and its object.
func (c GrantClient) Open(ctx context.Context, grantID, assetID string) (*os.File, string, error) {
	response, file, err := c.call(ctx, GrantRequest{Op: GrantOpen, GrantID: grantID, AssetID: assetID})
	if err != nil {
		return nil, "", err
	}
	if file == nil {
		return nil, "", errors.New("photo grant: open answered no descriptor")
	}
	return file, response.ObjectID, nil
}

// Remove takes assets out of a grant.
func (c GrantClient) Remove(ctx context.Context, grantID string, assetIDs []string) error {
	_, err := c.plain(ctx, GrantRequest{Op: GrantRemove, GrantID: grantID, AssetIDs: assetIDs})
	return err
}

// List lists every grant that is not revoked, a page per request.
func (c GrantClient) List(ctx context.Context) ([]photos.Grant, error) {
	var grants []photos.Grant
	after := ""
	for {
		response, err := c.plain(ctx, GrantRequest{Op: GrantList, After: after})
		if err != nil {
			return nil, err
		}
		grants = append(grants, response.Grants...)
		if response.Next == "" {
			return grants, nil
		}
		if response.Next <= after {
			return nil, errors.New("photo grant: list answered a page that does not move on")
		}
		after = response.Next
	}
}

// Revoke revokes a grant; revoking again changes nothing.
func (c GrantClient) Revoke(ctx context.Context, grantID string) error {
	_, err := c.plain(ctx, GrantRequest{Op: GrantRevoke, GrantID: grantID})
	return err
}

// GrantMessageLimit bounds a request or answer on the grant socket.
const GrantMessageLimit = grantMessageLimit
